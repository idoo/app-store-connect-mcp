package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func TestHTTPServerLimits(t *testing.T) {
	srv := newHTTPServer(context.Background(), http.NotFoundHandler())
	if srv.ReadTimeout != 60*time.Second || srv.WriteTimeout != 0 || srv.IdleTimeout != 120*time.Second || srv.MaxHeaderBytes != 64<<10 || srv.BaseContext == nil {
		t.Errorf("server limits = read %v write %v idle %v headers %d", srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
}

func TestConnLimitFromEnv(t *testing.T) {
	if n, err := connLimit(HTTPOptions{}); err != nil || n != DefaultMaxConns {
		t.Errorf("default = %d, %v", n, err)
	}
	t.Setenv(EnvHTTPMaxConns, "-1")
	if n, err := connLimit(HTTPOptions{}); err != nil || n != 0 {
		t.Errorf("negative = %d, %v; want unlimited", n, err)
	}
	t.Setenv(EnvHTTPMaxConns, "x")
	if _, err := connLimit(HTTPOptions{}); err == nil {
		t.Error("bad env accepted")
	}
	if n, _ := connLimit(HTTPOptions{MaxConns: 3}); n != 3 {
		t.Errorf("explicit = %d", n)
	}
}

func TestLimitListenerCapsConnections(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newLimitListener(raw, 1)
	defer ln.Close()
	accepted := make(chan net.Conn, 2)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	for range 2 {
		c, err := net.Dial("tcp", raw.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
	}
	first := <-accepted
	select {
	case <-accepted:
		t.Fatal("second connection accepted over the cap")
	case <-time.After(150 * time.Millisecond):
	}
	first.Close()
	first.Close() // a second Close must not free another slot
	select {
	case c := <-accepted:
		c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("slot not freed after Close")
	}
}

// startServer serves h on a loopback listener until the returned cancel
// func (the signal stand-in) runs; errc gets serveListener's result.
func startServer(t *testing.T, h http.Handler) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serveListener(ctx, ln, h, HTTPOptions{}) }()
	t.Cleanup(cancel)
	return ln.Addr().String(), cancel, errc
}

func TestRejectionWithUnreadBodyClosesConnection(t *testing.T) {
	h, err := HTTPHandler(HTTPOptions{Token: strongToken, NewServer: SessionFactory(Deps{Tools: []tool.Tool{}, Version: "test"})})
	if err != nil {
		t.Fatal(err)
	}
	addr, _, _ := startServer(t, h)
	for _, tc := range []struct{ name, extra string }{
		{"no token", ""},
		{"bad origin", "Authorization: Bearer " + strongToken + "\r\nOrigin: https://evil.example\r\n"},
	} {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		// A chunked body that never ends: the slow peer.
		pr, pw := io.Pipe()
		go func() { _, _ = io.Copy(c, pr) }()
		_, _ = io.WriteString(pw, "POST /mcp HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n"+tc.extra+"\r\n5\r\nhel")
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatalf("%s: no response to a refused request with an unread body: %v", tc.name, err)
		}
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden || !resp.Close {
			t.Errorf("%s: status %d close %v; want 401/403 with Connection: close", tc.name, resp.StatusCode, resp.Close)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Errorf("%s: connection still open after rejection", tc.name)
		}
		pw.Close()
		c.Close()
	}
}

func TestShutdownCancelsInFlightRequests(t *testing.T) {
	started := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		_, _ = io.WriteString(w, "cancelled")
	})
	addr, cancel, errc := startServer(t, h)
	type result struct {
		body string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		got <- result{string(b), err}
	}()
	<-started
	cancel()
	select {
	case r := <-got:
		if r.err != nil || r.body != "cancelled" {
			t.Errorf("in-flight response = %q, %v; want the handler's cancelled answer", r.body, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request not cancelled")
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("serve after shutdown = %v, want nil (clean exit)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestBearerSchemeCaseInsensitive(t *testing.T) {
	h := testHTTPHandler(t)
	for _, auth := range []string{"Bearer s3cret", "bearer s3cret", "BEARER s3cret", "  Bearer   s3cret  "} {
		if rec := doHTTP(t, h, "/mcp", auth, ""); rec.Code == http.StatusUnauthorized {
			t.Errorf("%q: 401", auth)
		}
	}
	for _, auth := range []string{"Basic s3cret", "Bearer", "Bearer ", "Bearers3cret", "Bearer s3cre", "Token s3cret"} {
		rec := doHTTP(t, h, "/mcp", auth, "")
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("Connection") != "close" {
			t.Errorf("%q: %d close=%q; want 401 with Connection: close", auth, rec.Code, rec.Header().Get("Connection"))
		}
	}
	if tok, ok := bearerToken("bEaReR  abc "); !ok || tok != "abc" {
		t.Errorf("bearerToken = %q %v", tok, ok)
	}
}

// fakeClock is a settable httpNow.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

func TestSessionCapEvictsIdleAndHonestRetryAfter(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	old := httpNow
	httpNow = clock.Now
	t.Cleanup(func() { httpNow = old })
	h, err := HTTPHandler(HTTPOptions{
		Token: "t", MaxSessions: 1, SessionTimeout: time.Hour,
		NewServer: SessionFactory(Deps{Tools: []tool.Tool{}, Version: "test"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	first := doHTTP(t, h, "/mcp", "Bearer t", "")
	sid := first.Header().Get(sessionIDHeader)
	if first.Code != http.StatusOK || sid == "" {
		t.Fatalf("first session = %d", first.Code)
	}
	clock.Add(4 * time.Minute)
	rec := doHTTP(t, h, "/mcp", "Bearer t", "")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "360" || rec.Header().Get("Connection") != "close" {
		t.Fatalf("over cap = %d Retry-After %q; want 503 with 360 s until the idle session is evictable", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Activity resets the idle clock.
	ping := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"ping"}`))
	ping.Header.Set("Authorization", "Bearer t")
	ping.Header.Set("Content-Type", "application/json")
	ping.Header.Set("Accept", "application/json, text/event-stream")
	ping.Header.Set(sessionIDHeader, sid)
	ping.Header.Set(protocolVersionHeader, "2025-06-18")
	h.ServeHTTP(httptest.NewRecorder(), ping)
	clock.Add(9 * time.Minute)
	if rec := doHTTP(t, h, "/mcp", "Bearer t", ""); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("after activity = %d Retry-After %q; want 503 60", rec.Code, rec.Header().Get("Retry-After"))
	}
	clock.Add(time.Minute)
	if rec := doHTTP(t, h, "/mcp", "Bearer t", ""); rec.Code != http.StatusOK || rec.Header().Get(sessionIDHeader) == sid {
		t.Fatalf("idle ≥ 10 min not evicted: %d", rec.Code)
	}
}

func TestSessionCapRetryAfterBoundedByIdleTimeout(t *testing.T) {
	l := &sessionLimiter{max: 1, idleTimeout: 30 * time.Second, now: time.Now, pending: 1}
	if ok, d := l.acquire(); ok || d != 30*time.Second {
		t.Errorf("pending only = %v %v; want refused, 30s", ok, d)
	}
}

func postBatch(t *testing.T, h http.Handler, body, version, sid string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if version != "" {
		req.Header.Set(protocolVersionHeader, version)
	}
	if sid != "" {
		req.Header.Set(sessionIDHeader, sid)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func batchOf(n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = `{"jsonrpc":"2.0","id":` + strings.Repeat("1", 1) + `,"method":"ping"}`
	}
	return " \n[" + strings.Join(items, ",") + "]"
}

func TestJSONRPCBatchGating(t *testing.T) {
	h := testHTTPHandler(t)
	isRPCReject := func(rec *httptest.ResponseRecorder) bool {
		var body struct {
			Error struct{ Code int } `json:"error"`
		}
		return rec.Code == http.StatusBadRequest && json.Unmarshal(rec.Body.Bytes(), &body) == nil && body.Error.Code == -32600
	}
	for _, tc := range []struct {
		name, version string
		n             int
		reject        bool
	}{
		{"no version", "", 2, true},
		{"2025-06-18", "2025-06-18", 2, true},
		{"2025-03-26 small", "2025-03-26", 2, false},
		{"2025-03-26 at cap", "2025-03-26", 100, false},
		{"2025-03-26 over cap", "2025-03-26", 101, true},
	} {
		rec := postBatch(t, h, batchOf(tc.n), tc.version, "")
		if got := isRPCReject(rec); got != tc.reject {
			t.Errorf("%s: %d %s; reject=%v want %v", tc.name, rec.Code, rec.Body.String(), got, tc.reject)
		}
	}
	// A single message still reaches the SDK with its body intact.
	if rec := doHTTP(t, h, "/mcp", "Bearer s3cret", ""); rec.Code != http.StatusOK {
		t.Errorf("single initialize = %d", rec.Code)
	}
}

func TestJSONRPCBatchUsesSessionVersion(t *testing.T) {
	h, err := HTTPHandler(HTTPOptions{Token: "s3cret", NewServer: SessionFactory(Deps{Tools: []tool.Tool{}, Version: "test"})})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		version string
		reject  bool
	}{{"2025-03-26", false}, {"2025-06-18", true}} {
		init := strings.Replace(initBody, "2025-06-18", v.version, 1)
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(init))
		req.Header.Set("Authorization", "Bearer s3cret")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		sid := rec.Header().Get(sessionIDHeader)
		if rec.Code != http.StatusOK || sid == "" {
			t.Fatalf("%s initialize = %d", v.version, rec.Code)
		}
		got := postBatch(t, h, batchOf(2), "", sid)
		if (got.Code == http.StatusBadRequest) != v.reject {
			t.Errorf("session %s batch = %d %s; reject want %v", v.version, got.Code, got.Body.String(), v.reject)
		}
	}
}
