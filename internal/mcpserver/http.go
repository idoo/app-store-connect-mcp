package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// EnvHTTPToken is the environment variable holding the HTTP bearer token.
const EnvHTTPToken = "ASC_MCP_HTTP_TOKEN" //nolint:gosec // variable name, not a credential.

// Session limits for HTTP mode, overridable by HTTPOptions or env.
const (
	EnvHTTPSessionTimeout = "ASC_MCP_HTTP_SESSION_TIMEOUT"
	EnvHTTPMaxSessions    = "ASC_MCP_HTTP_MAX_SESSIONS"
	// DefaultSessionTimeout closes idle sessions; it outlives PlanTTL so a
	// pending plan never loses its session first.
	DefaultSessionTimeout = 30 * time.Minute
	// DefaultMaxSessions caps concurrently open sessions.
	DefaultMaxSessions = 64
	// sessionEvictIdle is how long a session must sit idle before a new
	// session at the cap may evict it.
	sessionEvictIdle = 10 * time.Minute
)

// Connection and request limits for HTTP mode.
const (
	EnvHTTPMaxConns = "ASC_MCP_HTTP_MAX_CONNS"
	// DefaultMaxConns caps concurrently open TCP connections.
	DefaultMaxConns = 256
	// readTimeout bounds reading one request including its body; the
	// server clears it once the body is read, so long calls and SSE streams
	// are unaffected.
	readTimeout    = 60 * time.Second
	idleTimeout    = 120 * time.Second
	maxHeaderBytes = 64 << 10
	// rejectWriteTimeout bounds writing a rejection to a slow reader.
	rejectWriteTimeout = 10 * time.Second
	// shutdownGrace is how long in-flight requests get after cancellation
	// before connections are closed.
	shutdownGrace = 10 * time.Second
	// maxBodyBytes matches the SDK's own POST body cap.
	maxBodyBytes = 4 << 20
	// maxBatchLen caps the messages in one JSON-RPC batch.
	maxBatchLen = 100
	// batchMaxProtocol is the last protocol revision with JSON-RPC batches.
	batchMaxProtocol = "2025-03-26"
)

// HTTP header names the transport inspects.
const (
	sessionIDHeader       = "Mcp-Session-Id"
	protocolVersionHeader = "Mcp-Protocol-Version"
)

// statelessProtocolVersion is the first protocol revision served by the
// stateless handler; older clients keep the sessionful one.
const statelessProtocolVersion = "2026-07-28"

// minTokenLen is the shortest configured bearer token accepted.
const minTokenLen = 32

// CORS answers for allow-listed browser origins. Never "*".
const (
	corsAllowHeaders  = "authorization, content-type, mcp-session-id, mcp-protocol-version, mcp-method, mcp-name, last-event-id"
	corsAllowMethods  = "POST, GET, DELETE"
	corsMaxAge        = "600"
	corsExposeHeaders = sessionIDHeader
)

// ErrTokenRequired means HTTP mode has no token and cannot print a generated
// one because stderr is not a terminal.
var ErrTokenRequired = errors.New("mcpserver: http: set " + EnvHTTPToken + " (a token is generated only when stderr is a terminal)")

// HTTPOptions configures [ServeHTTP].
type HTTPOptions struct {
	// Token is the required bearer token; empty generates one (TTY only).
	Token string
	// AllowRemote permits binding a non-loopback address; it requires TLS.
	AllowRemote bool
	// TLSCert and TLSKey are PEM file paths; both or neither.
	TLSCert, TLSKey string
	// AllowedOrigins lists extra exact Origin values (scheme://host[:port])
	// besides loopback origins.
	AllowedOrigins []string
	// NewServer builds one MCP server per client session (protocol
	// revisions before 2026-07-28).
	NewServer func(*http.Request) *mcp.Server
	// NewStatelessServer returns the server for 2026-07-28 requests, which
	// are served without sessions; nil leaves them to the sessionful handler,
	// which rejects them.
	NewStatelessServer func(*http.Request) *mcp.Server
	// Logger receives transport logs; nil discards.
	Logger *slog.Logger
	// Stderr receives the generated token; nil means os.Stderr.
	Stderr io.Writer
	// StderrIsTTY overrides terminal detection of os.Stderr (tests).
	StderrIsTTY *bool
	// SessionTimeout closes sessions idle this long; 0 reads
	// ASC_MCP_HTTP_SESSION_TIMEOUT (a Go duration), else DefaultSessionTimeout.
	// Negative never expires sessions.
	SessionTimeout time.Duration
	// MaxSessions caps open sessions (new ones get 503); 0 reads
	// ASC_MCP_HTTP_MAX_SESSIONS, else DefaultMaxSessions. Negative is unlimited.
	MaxSessions int
	// MaxConns caps open TCP connections (further ones wait to be
	// accepted); 0 reads ASC_MCP_HTTP_MAX_CONNS, else DefaultMaxConns.
	// Negative is unlimited.
	MaxConns int
}

// ServeHTTP serves MCP over Streamable HTTP on addr until ctx ends. It binds
// loopback only unless AllowRemote (with TLS), requires a bearer token,
// rejects non-loopback Origins, and exposes an unauthenticated /healthz.
func ServeHTTP(ctx context.Context, addr string, opts HTTPOptions) error {
	if err := CheckListenAddr(addr, opts); err != nil {
		return err
	}
	if opts.Token != "" {
		if err := CheckToken(opts.Token); err != nil {
			return err
		}
	}
	if opts.Token == "" {
		tok, err := generateToken(opts)
		if err != nil {
			return err
		}
		opts.Token = tok
	}
	h, err := HTTPHandler(opts)
	if err != nil {
		return err
	}
	maxConns, err := connLimit(opts)
	if err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mcpserver: http: listen %s: %w", addr, err)
	}
	if maxConns > 0 {
		ln = newLimitListener(ln, maxConns)
	}
	return serveListener(ctx, ln, h, opts)
}

// newHTTPServer returns the server for h: bounded request reads, idle
// keep-alives and headers, no global write timeout (SSE streams and long
// tool calls), and base as every request's parent context.
func newHTTPServer(base context.Context, h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		BaseContext:       func(net.Listener) context.Context { return base },
	}
}

// serveListener serves h on ln until ctx ends. Then it cancels every
// request's context, so in-flight tool calls end with a cancelled result
// the client still receives, waits up to shutdownGrace for them, and closes
// what remains. A shutdown is a clean exit: it returns nil.
func serveListener(ctx context.Context, ln net.Listener, h http.Handler, opts HTTPOptions) error {
	base, cancelBase := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelBase()
	srv := newHTTPServer(base, h)
	errc := make(chan error, 1)
	go func() {
		if opts.TLSCert != "" {
			errc <- srv.ServeTLS(ln, opts.TLSCert, opts.TLSKey)
		} else {
			errc <- srv.Serve(ln)
		}
	}()
	select {
	case err := <-errc:
		return fmt.Errorf("mcpserver: http: %w", err)
	case <-ctx.Done():
	}
	cancelBase()
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		if opts.Logger != nil {
			opts.Logger.Warn("http: shutdown grace expired; closing connections", "err", err)
		}
		_ = srv.Close()
	}
	return nil
}

// connLimit resolves the connection cap (0 = unlimited) from opts, then
// env, then DefaultMaxConns.
func connLimit(opts HTTPOptions) (int, error) {
	n := opts.MaxConns
	if n == 0 {
		n = DefaultMaxConns
		if v := os.Getenv(EnvHTTPMaxConns); v != "" {
			p, err := strconv.Atoi(v)
			if err != nil || p == 0 {
				return 0, fmt.Errorf("mcpserver: http: %s=%q: want a connection count (negative is unlimited)", EnvHTTPMaxConns, v)
			}
			n = p
		}
	}
	return max(n, 0), nil
}

// limitListener accepts at most cap(sem) open connections; Accept waits for
// one to close.
type limitListener struct {
	net.Listener
	sem       chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newLimitListener(ln net.Listener, n int) *limitListener {
	return &limitListener{Listener: ln, sem: make(chan struct{}, n), done: make(chan struct{})}
}

func (l *limitListener) Accept() (net.Conn, error) {
	select {
	case l.sem <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitConn{Conn: c, release: func() { <-l.sem }}, nil
}

func (l *limitListener) Close() error {
	err := l.Listener.Close()
	l.closeOnce.Do(func() { close(l.done) })
	return err
}

// limitConn frees its listener slot on the first Close.
type limitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// CheckListenAddr enforces the bind policy: loopback hosts only, unless
// AllowRemote is set together with a TLS certificate and key.
func CheckListenAddr(addr string, opts HTTPOptions) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("mcpserver: http: address %q: %w", addr, err)
	}
	if (opts.TLSCert == "") != (opts.TLSKey == "") {
		return errors.New("mcpserver: http: --tls-cert and --tls-key must be given together")
	}
	if isLoopbackHost(host) {
		return nil
	}
	if !opts.AllowRemote {
		return fmt.Errorf("mcpserver: http: refusing non-loopback address %q without --allow-remote", addr)
	}
	if opts.TLSCert == "" {
		return errors.New("mcpserver: http: --allow-remote requires --tls-cert and --tls-key")
	}
	return nil
}

// CheckToken refuses a configured bearer token that is short or obviously
// low-entropy (few distinct characters).
func CheckToken(tok string) error {
	distinct := map[rune]bool{}
	for _, r := range tok {
		distinct[r] = true
	}
	if len(tok) < minTokenLen || len(distinct) < 8 {
		return fmt.Errorf("mcpserver: http: %s is too weak (want at least %d random characters, e.g. `openssl rand -hex 32`); unset it to have a random token generated and printed when stderr is a terminal", EnvHTTPToken, minTokenLen)
	}
	return nil
}

// HTTPHandler returns the authenticated MCP handler. opts.Token must be set.
// Requests carrying Mcp-Protocol-Version 2026-07-28 or later go to the
// stateless handler (when configured); everything else to the sessionful one.
// CORS preflights are answered before authentication; Origin and bearer
// checks run before either handler.
func HTTPHandler(opts HTTPOptions) (http.Handler, error) {
	if opts.Token == "" {
		return nil, ErrTokenRequired
	}
	if opts.NewServer == nil {
		return nil, errors.New("mcpserver: http: NewServer is required")
	}
	want := sha256.Sum256([]byte(opts.Token))
	timeout, maxSessions, err := sessionLimits(opts)
	if err != nil {
		return nil, err
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(opts.NewServer, &mcp.StreamableHTTPOptions{Logger: opts.Logger, SessionTimeout: timeout})
	var stateless http.Handler
	if opts.NewStatelessServer != nil {
		stateless = mcp.NewStreamableHTTPHandler(opts.NewStatelessServer, &mcp.StreamableHTTPOptions{Logger: opts.Logger, Stateless: true})
	}
	limit := &sessionLimiter{max: maxSessions, idleTimeout: timeout, now: httpNow}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if r.Method == http.MethodOptions {
			preflight(w, origin, opts.AllowedOrigins)
			return
		}
		if !originAllowed(origin, opts.AllowedOrigins) {
			reject(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if origin != "" {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
		}
		got, ok := bearerToken(r.Header.Get("Authorization"))
		sum := sha256.Sum256([]byte(got))
		if !ok || subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="app-store-connect-mcp"`)
			reject(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if stateless != nil && r.Header.Get(protocolVersionHeader) >= statelessProtocolVersion {
			stateless.ServeHTTP(w, r)
			return
		}
		id := r.Header.Get(sessionIDHeader)
		if !gateBatch(w, r, func() string { return limit.protocolVersion(id) }) {
			return
		}
		if id != "" {
			limit.begin(id)
			defer limit.end(id)
			mcpHandler.ServeHTTP(w, r)
			return
		}
		if ok, retry := limit.acquire(); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry/time.Second)))
			reject(w, "too many open sessions; close one or wait for idle ones to expire", http.StatusServiceUnavailable)
			return
		}
		slot := &buildSlot{}
		mcpHandler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), buildSlotKey{}, slot)))
		limit.release(slot.srv)
	}))
	return mux, nil
}

// httpNow is the session limiter's clock (tests replace it).
var httpNow = time.Now

// reject answers a refused request and closes the connection afterwards,
// so an unread (possibly endless) body never pins it, with a write deadline
// against slow readers.
func reject(w http.ResponseWriter, msg string, code int) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(rejectWriteTimeout))
	w.Header().Set("Connection", "close")
	http.Error(w, msg, code)
}

// rejectRPC is reject with a JSON-RPC invalid-request error body.
func rejectRPC(w http.ResponseWriter, msg string, code int) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(rejectWriteTimeout))
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": nil,
		"error": map[string]any{"code": -32600, "message": msg},
	})
	h := w.Header()
	h.Set("Connection", "close")
	h.Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

// bearerToken extracts the token of a Bearer Authorization header; the
// scheme is case-insensitive (RFC 9110 §11.1) and the token trimmed.
func bearerToken(h string) (string, bool) {
	scheme, tok, ok := strings.Cut(strings.TrimSpace(h), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	return tok, tok != ""
}

// gateBatch peeks at a POST body: a JSON-RPC batch (top-level array) is
// refused unless the protocol (header, else the session's negotiated one)
// is 2025-03-26 or older, and is capped at maxBatchLen messages. r.Body is
// restored for the SDK. It reports whether the request may proceed.
func gateBatch(w http.ResponseWriter, r *http.Request, sessionVersion func() string) bool {
	if r.Method != http.MethodPost || r.Body == nil || r.Body == http.NoBody {
		return true
	}
	orig := r.Body
	br := bufio.NewReader(orig)
	r.Body = struct {
		io.Reader
		io.Closer
	}{br, orig}
	if !startsWithArray(br) {
		return true
	}
	v := r.Header.Get(protocolVersionHeader)
	if v == "" {
		v = sessionVersion()
	}
	if v == "" || v > batchMaxProtocol {
		rejectRPC(w, "JSON-RPC batches need protocol "+batchMaxProtocol+" or older", http.StatusBadRequest)
		return false
	}
	body, err := io.ReadAll(io.LimitReader(br, maxBodyBytes+1))
	switch {
	case err != nil:
		reject(w, "failed to read body", http.StatusBadRequest)
		return false
	case len(body) > maxBodyBytes:
		reject(w, fmt.Sprintf("request body exceeds %d bytes", maxBodyBytes), http.StatusRequestEntityTooLarge)
		return false
	}
	var items []json.RawMessage
	if json.Unmarshal(body, &items) == nil && len(items) > maxBatchLen {
		rejectRPC(w, fmt.Sprintf("JSON-RPC batch has %d messages; the limit is %d", len(items), maxBatchLen), http.StatusBadRequest)
		return false
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{bytes.NewReader(body), orig}
	return true
}

// startsWithArray reports whether the first non-whitespace byte is '['.
func startsWithArray(br *bufio.Reader) bool {
	for i := 1; ; i++ {
		b, _ := br.Peek(i)
		if len(b) < i {
			return false
		}
		switch b[i-1] {
		case ' ', '\t', '\r', '\n':
		case '[':
			return true
		default:
			return false
		}
	}
}

// preflight answers a CORS preflight: an allow-listed browser Origin gets
// the allowed headers and methods echoed for it, anything else 403. It runs
// before authentication because browsers never send credentials on it.
func preflight(w http.ResponseWriter, origin string, extra []string) {
	if origin == "" || !originAllowed(origin, extra) {
		reject(w, "forbidden origin", http.StatusForbidden)
		return
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Add("Vary", "Origin")
	h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
	h.Set("Access-Control-Allow-Methods", corsAllowMethods)
	h.Set("Access-Control-Max-Age", corsMaxAge)
	w.WriteHeader(http.StatusNoContent)
}

// sessionLimits resolves the idle timeout (0 = never, for the SDK) and the
// session cap (0 = unlimited) from opts, then env, then defaults.
func sessionLimits(opts HTTPOptions) (time.Duration, int, error) {
	timeout, maxSessions := opts.SessionTimeout, opts.MaxSessions
	if timeout == 0 {
		timeout = DefaultSessionTimeout
		if v := os.Getenv(EnvHTTPSessionTimeout); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d == 0 {
				return 0, 0, fmt.Errorf("mcpserver: http: %s=%q: want a duration such as 30m (negative never expires)", EnvHTTPSessionTimeout, v)
			}
			timeout = d
		}
	}
	if maxSessions == 0 {
		maxSessions = DefaultMaxSessions
		if v := os.Getenv(EnvHTTPMaxSessions); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n == 0 {
				return 0, 0, fmt.Errorf("mcpserver: http: %s=%q: want a session count (negative is unlimited)", EnvHTTPMaxSessions, v)
			}
			maxSessions = n
		}
	}
	return max(timeout, 0), max(maxSessions, 0), nil
}

// buildSlotKey carries a *buildSlot through one session-less HTTP request.
type buildSlotKey struct{}

// buildSlot memoizes the server built for one new-session request: the SDK
// asks for a server twice on it (protocol check, then session creation).
type buildSlot struct{ srv *mcp.Server }

// sessionLimiter counts open sessions: each new-session request reserves a
// place, and afterwards its server counts while it still has a session (the
// SDK drops sessions on DELETE and idle timeout). It tracks each session's
// last activity so a new session at the cap can evict the longest-idle one.
type sessionLimiter struct {
	max         int           // 0 = unlimited
	idleTimeout time.Duration // SDK idle expiry; 0 = never
	now         func() time.Time
	mu          sync.Mutex
	pending     int
	servers     []*mcp.Server
	activity    map[string]*sessionActivity
}

// sessionActivity is one session's in-flight request count and the time
// its last request started or ended.
type sessionActivity struct {
	last   time.Time
	active int
}

// prune drops servers without sessions and activity of gone sessions; it
// returns the live sessions by ID with their servers. l.mu must be held.
func (l *sessionLimiter) prune() map[string]*mcp.Server {
	live := map[string]*mcp.Server{}
	l.servers = slices.DeleteFunc(l.servers, func(s *mcp.Server) bool {
		n := 0
		for ss := range s.Sessions() {
			live[ss.ID()] = s
			n++
		}
		return n == 0
	})
	for id := range l.activity {
		if live[id] == nil {
			delete(l.activity, id)
		}
	}
	return live
}

// acquire reserves a place for a new session. At the cap it evicts the
// longest-idle session once it has been idle sessionEvictIdle; otherwise it
// refuses with the wait until that becomes possible (or the SDK expires it).
func (l *sessionLimiter) acquire() (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	live := l.prune()
	if l.max <= 0 || len(l.servers)+l.pending < l.max {
		l.pending++
		return true, 0
	}
	now := l.now()
	if l.activity == nil {
		l.activity = map[string]*sessionActivity{}
	}
	oldest, oldestIdle := "", time.Duration(-1)
	for id := range live {
		a := l.activity[id]
		if a == nil {
			a = &sessionActivity{last: now}
			l.activity[id] = a
		}
		if idle := now.Sub(a.last); a.active == 0 && idle > oldestIdle {
			oldest, oldestIdle = id, idle
		}
	}
	if oldest != "" && oldestIdle >= sessionEvictIdle {
		srv := live[oldest]
		for ss := range srv.Sessions() {
			if ss.ID() == oldest {
				_ = ss.Close()
			}
		}
		l.servers = slices.DeleteFunc(l.servers, func(s *mcp.Server) bool { return s == srv })
		delete(l.activity, oldest)
		l.pending++
		return true, 0
	}
	horizon := sessionEvictIdle
	if l.idleTimeout > 0 {
		horizon = min(horizon, l.idleTimeout)
	}
	wait := horizon
	if oldest != "" {
		wait = horizon - oldestIdle
	}
	wait = (wait + time.Second - 1).Truncate(time.Second)
	return false, min(max(wait, time.Second), horizon)
}

// release ends a new-session request; srv (if any) now holds its session.
func (l *sessionLimiter) release(srv *mcp.Server) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending--
	if srv == nil {
		return
	}
	l.servers = append(l.servers, srv)
	if l.activity == nil {
		l.activity = map[string]*sessionActivity{}
	}
	for ss := range srv.Sessions() {
		if l.activity[ss.ID()] == nil {
			l.activity[ss.ID()] = &sessionActivity{last: l.now()}
		}
	}
}

// begin and end bracket a request of a known session; unknown IDs are
// ignored (the SDK answers them 404).
func (l *sessionLimiter) begin(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a := l.activity[id]; a != nil {
		a.active++
		a.last = l.now()
	}
}

func (l *sessionLimiter) end(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a := l.activity[id]; a != nil && a.active > 0 {
		a.active--
		a.last = l.now()
	}
}

// protocolVersion returns the protocol a live session negotiated, or "".
func (l *sessionLimiter) protocolVersion(id string) string {
	if id == "" {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.servers {
		for ss := range s.Sessions() {
			if ss.ID() == id {
				if p := ss.InitializeParams(); p != nil {
					return p.ProtocolVersion
				}
				return ""
			}
		}
	}
	return ""
}

func originAllowed(origin string, extra []string) bool {
	if origin == "" {
		return true
	}
	if slices.Contains(extra, origin) {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SessionFactory returns an [HTTPOptions.NewServer] that builds one server
// per HTTP session from deps, so toolset and write-mode state stay per
// session. Build failures are logged and answered with 400 by the SDK.
//
// The SDK also asks for a server on every request only to read its protocol
// versions; requests of an existing session get a tool-less probe with the
// same versions, and a new session's two lookups share one Build.
//
// All sessions share deps.Env.ASC (one token cache and rate limit per
// profile), so each session wraps it in its own write guard: enable_writes in
// one session opens the shared client but only that session's guard.
func SessionFactory(deps Deps) func(*http.Request) *mcp.Server {
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	probe := mcp.NewServer(serverImplementation(deps.Version), &mcp.ServerOptions{Logger: logger})
	return func(req *http.Request) *mcp.Server {
		if req.Header.Get(sessionIDHeader) != "" {
			return probe
		}
		slot, _ := req.Context().Value(buildSlotKey{}).(*buildSlot)
		if slot != nil && slot.srv != nil {
			return slot.srv
		}
		srv, err := Build(req.Context(), sessionDeps(deps))
		if err != nil {
			logger.Error("build session server", "err", err)
			return nil
		}
		if slot != nil {
			slot.srv = srv
		}
		return srv
	}
}

// StatelessFactory returns an [HTTPOptions.NewStatelessServer] serving every
// 2026-07-28 request from one server built from the server-level config
// (flags, env, profile): its toolsets and write mode are fixed, so toolsets
// enable/disable and enable_writes are refused there. Approval states are
// signed per process and plans live on that one server, so multi-round
// approval and plan_id work without a session. A build failure is logged and
// answered with 400 by the SDK.
func StatelessFactory(deps Deps) func(*http.Request) *mcp.Server {
	deps.Stateless = true
	build := sync.OnceValues(func() (*mcp.Server, error) { return Build(context.Background(), deps) })
	return func(*http.Request) *mcp.Server {
		srv, err := build()
		if err != nil {
			if deps.Logger != nil {
				deps.Logger.Error("build stateless server", "err", err)
			}
			return nil
		}
		return srv
	}
}

// sessionDeps returns deps for one HTTP session: its ASC client is wrapped in
// a session write guard that starts at the profile's mode, and SetWrites
// flips only that guard (opening the shared client when enabling).
func sessionDeps(deps Deps) Deps {
	if deps.Env.ASC == nil {
		return deps
	}
	g := &sessionASC{ASC: deps.Env.ASC}
	g.writes.Store(deps.Profile.AllowWrites)
	shared := deps.Env.SetWrites
	deps.Env.ASC = g
	deps.Env.SetWrites = func(on bool) {
		g.writes.Store(on)
		if on && shared != nil {
			shared(true) // other sessions stay closed by their own guards
		}
	}
	return deps
}

// sessionASC is one HTTP session's view of the shared ASC client: non-GET
// requests fail with tool.ErrWritesDisabled unless this session enabled
// writes. The shared client's own guard still applies, but it is one-way:
// once any session opens it, only this wrapper keeps other sessions read-only,
// so every mutating tool.ASC method must be overridden here
// (TestSessionASCCoversMutators).
type sessionASC struct {
	tool.ASC
	writes atomic.Bool
}

// Do implements [tool.ASC].
func (s *sessionASC) Do(ctx context.Context, req tool.Request) (*tool.Response, error) {
	if m := strings.ToUpper(req.Method); m != "" && m != http.MethodGet && !s.writes.Load() {
		return nil, fmt.Errorf("session: %s %s: %w", m, req.Path, tool.ErrWritesDisabled)
	}
	return s.ASC.Do(ctx, req)
}

func generateToken(opts HTTPOptions) (string, error) {
	tty := false
	if opts.StderrIsTTY != nil {
		tty = *opts.StderrIsTTY
	} else if fi, err := os.Stderr.Stat(); err == nil {
		tty = fi.Mode()&os.ModeCharDevice != 0
	}
	if !tty {
		return "", ErrTokenRequired
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mcpserver: http: token: %w", err)
	}
	tok := hex.EncodeToString(b[:])
	w := opts.Stderr
	if w == nil {
		w = os.Stderr
	}
	if _, err := fmt.Fprintf(w, "app-store-connect-mcp: HTTP bearer token (set %s to reuse): %s\n", EnvHTTPToken, tok); err != nil {
		return "", fmt.Errorf("mcpserver: http: print token: %w", err)
	}
	return tok, nil
}
