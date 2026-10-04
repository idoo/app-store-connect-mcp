package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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

	"github.com/idoo/hamn/internal/tool"
)

// EnvHTTPToken is the environment variable holding the HTTP bearer token.
const EnvHTTPToken = "HAMN_HTTP_TOKEN" //nolint:gosec // variable name, not a credential.

// Session limits for HTTP mode, overridable by HTTPOptions or env.
const (
	EnvHTTPSessionTimeout = "HAMN_HTTP_SESSION_TIMEOUT"
	EnvHTTPMaxSessions    = "HAMN_HTTP_MAX_SESSIONS"
	// DefaultSessionTimeout closes idle sessions; it outlives PlanTTL so a
	// pending plan never loses its session first.
	DefaultSessionTimeout = 30 * time.Minute
	// DefaultMaxSessions caps concurrently open sessions.
	DefaultMaxSessions = 64
)

// sessionIDHeader is the Streamable HTTP session header.
const sessionIDHeader = "Mcp-Session-Id"

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
	// NewServer builds one MCP server per client session.
	NewServer func(*http.Request) *mcp.Server
	// Logger receives transport logs; nil discards.
	Logger *slog.Logger
	// Stderr receives the generated token; nil means os.Stderr.
	Stderr io.Writer
	// StderrIsTTY overrides terminal detection of os.Stderr (tests).
	StderrIsTTY *bool
	// SessionTimeout closes sessions idle this long; 0 reads
	// HAMN_HTTP_SESSION_TIMEOUT (a Go duration), else DefaultSessionTimeout.
	// Negative never expires sessions.
	SessionTimeout time.Duration
	// MaxSessions caps open sessions (new ones get 503); 0 reads
	// HAMN_HTTP_MAX_SESSIONS, else DefaultMaxSessions. Negative is unlimited.
	MaxSessions int
}

// ServeHTTP serves MCP over Streamable HTTP on addr until ctx ends. It binds
// loopback only unless AllowRemote (with TLS), requires a bearer token,
// rejects non-loopback Origins, and exposes an unauthenticated /healthz.
func ServeHTTP(ctx context.Context, addr string, opts HTTPOptions) error {
	if err := CheckListenAddr(addr, opts); err != nil {
		return err
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
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mcpserver: http: listen %s: %w", addr, err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
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
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("mcpserver: http: shutdown: %w", err)
		}
		return nil
	}
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

// HTTPHandler returns the authenticated MCP handler. opts.Token must be set.
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
	limit := &sessionLimiter{max: maxSessions}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !originAllowed(r.Header.Get("Origin"), opts.AllowedOrigins) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(got))
		if !ok || subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="hamn"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get(sessionIDHeader) != "" {
			mcpHandler.ServeHTTP(w, r)
			return
		}
		if !limit.acquire() {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many open sessions; close one or wait for idle ones to expire", http.StatusServiceUnavailable)
			return
		}
		slot := &buildSlot{}
		mcpHandler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), buildSlotKey{}, slot)))
		limit.release(slot.srv)
	}))
	return mux, nil
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
// SDK drops sessions on DELETE and idle timeout).
type sessionLimiter struct {
	max     int // 0 = unlimited
	mu      sync.Mutex
	pending int
	servers []*mcp.Server
}

func (l *sessionLimiter) acquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.servers = slices.DeleteFunc(l.servers, func(s *mcp.Server) bool {
		for range s.Sessions() {
			return false
		}
		return true
	})
	if l.max > 0 && len(l.servers)+l.pending >= l.max {
		return false
	}
	l.pending++
	return true
}

func (l *sessionLimiter) release(srv *mcp.Server) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending--
	if srv != nil {
		l.servers = append(l.servers, srv)
	}
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
	probe := mcp.NewServer(&mcp.Implementation{Name: "hamn", Version: deps.Version}, &mcp.ServerOptions{
		Logger:                    logger,
		SupportedProtocolVersions: legacyProtocolVersions(),
	})
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
	if _, err := fmt.Fprintf(w, "hamn: HTTP bearer token (set %s to reuse): %s\n", EnvHTTPToken, tok); err != nil {
		return "", fmt.Errorf("mcpserver: http: print token: %w", err)
	}
	return tok, nil
}
