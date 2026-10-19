package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/hamn/internal/tool"
)

func testHTTPHandler(t *testing.T) http.Handler {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "hamn", Version: "test"}, nil)
	h, err := HTTPHandler(HTTPOptions{
		Token:          "s3cret",
		AllowedOrigins: []string{"https://app.example"},
		NewServer:      func(*http.Request) *mcp.Server { return srv },
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func doHTTP(t *testing.T, h http.Handler, path, auth, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(initBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHTTPAuthAndOrigin(t *testing.T) {
	h := testHTTPHandler(t)
	tests := []struct {
		name, auth, origin string
		want               int
	}{
		{"no token", "", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", "", http.StatusUnauthorized},
		{"basic scheme", "Basic s3cret", "", http.StatusUnauthorized},
		{"token prefix", "Bearer s3cre", "", http.StatusUnauthorized},
		{"bad origin", "Bearer s3cret", "https://evil.example", http.StatusForbidden},
		{"bad origin no token", "", "http://attacker.test", http.StatusForbidden},
		{"localhost lookalike", "Bearer s3cret", "http://localhost.evil.test", http.StatusForbidden},
		{"ok", "Bearer s3cret", "", http.StatusOK},
		{"ok loopback origin", "Bearer s3cret", "http://127.0.0.1:3000", http.StatusOK},
		{"ok localhost origin", "Bearer s3cret", "http://localhost:5173", http.StatusOK},
		{"ok configured origin", "Bearer s3cret", "https://app.example", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doHTTP(t, h, "/mcp", tt.auth, tt.origin)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tt.want, rec.Body.String())
			}
			if tt.want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("missing WWW-Authenticate")
			}
		})
	}
}

func TestHTTPHealthz(t *testing.T) {
	h := testHTTPHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("healthz = %d %q, want 200 empty", rec.Code, rec.Body.String())
	}
}

func TestCheckListenAddr(t *testing.T) {
	tls := HTTPOptions{TLSCert: "c.pem", TLSKey: "k.pem"}
	remoteTLS := tls
	remoteTLS.AllowRemote = true
	tests := []struct {
		addr string
		opts HTTPOptions
		ok   bool
	}{
		{"127.0.0.1:8765", HTTPOptions{}, true},
		{"[::1]:8765", HTTPOptions{}, true},
		{"localhost:8765", HTTPOptions{}, true},
		{"0.0.0.0:8765", HTTPOptions{}, false},
		{":8765", HTTPOptions{}, false},
		{"192.168.1.5:8765", HTTPOptions{}, false},
		{"0.0.0.0:8765", HTTPOptions{AllowRemote: true}, false},
		{"0.0.0.0:8765", remoteTLS, true},
		{"127.0.0.1:8765", HTTPOptions{TLSCert: "c.pem"}, false},
		{"nonsense", HTTPOptions{}, false},
	}
	for _, tt := range tests {
		err := CheckListenAddr(tt.addr, tt.opts)
		if (err == nil) != tt.ok {
			t.Errorf("CheckListenAddr(%q, %+v) = %v, want ok=%v", tt.addr, tt.opts, err, tt.ok)
		}
	}
}

func TestServeHTTPRefusals(t *testing.T) {
	ctx := context.Background()
	newSrv := func(*http.Request) *mcp.Server { return nil }
	if err := ServeHTTP(ctx, "0.0.0.0:0", HTTPOptions{Token: "t", NewServer: newSrv}); err == nil {
		t.Error("non-loopback without AllowRemote accepted")
	}
	no := false
	if err := ServeHTTP(ctx, "127.0.0.1:0", HTTPOptions{NewServer: newSrv, StderrIsTTY: &no}); !errors.Is(err, ErrTokenRequired) {
		t.Errorf("no token, no TTY: err = %v, want ErrTokenRequired", err)
	}
	if _, err := HTTPHandler(HTTPOptions{NewServer: newSrv}); !errors.Is(err, ErrTokenRequired) {
		t.Errorf("HTTPHandler without token: err = %v", err)
	}
}

func TestGenerateTokenOnTTY(t *testing.T) {
	yes := true
	var buf strings.Builder
	tok, err := generateToken(HTTPOptions{StderrIsTTY: &yes, Stderr: &buf})
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 || !strings.Contains(buf.String(), tok) {
		t.Errorf("token %q, stderr %q", tok, buf.String())
	}
}

func TestServeHTTPShutsDownOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- ServeHTTP(ctx, "127.0.0.1:0", HTTPOptions{Token: "t", NewServer: func(*http.Request) *mcp.Server { return nil }})
	}()
	cancel()
	if err := <-errc; err != nil {
		t.Errorf("ServeHTTP after cancel = %v", err)
	}
}

func TestSessionFactoryServesInitialize(t *testing.T) {
	h, err := HTTPHandler(HTTPOptions{Token: "t", NewServer: SessionFactory(Deps{Tools: []tool.Tool{}, Version: "test"})})
	if err != nil {
		t.Fatal(err)
	}
	rec := doHTTP(t, h, "/mcp", "Bearer t", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"hamn"`) {
		t.Errorf("initialize = %d %q", rec.Code, rec.Body.String())
	}
}

// countingASC records Do calls; other methods are unused.
type countingASC struct {
	tool.ASC
	calls int
}

func (c *countingASC) Do(context.Context, tool.Request) (*tool.Response, error) {
	c.calls++
	return &tool.Response{}, nil
}

func TestSessionWritesAreSessionScoped(t *testing.T) {
	base := &countingASC{}
	var shared []bool
	deps := Deps{Profile: tool.ProfileInfo{Name: "p"}}
	deps.Env.ASC = base
	deps.Env.SetWrites = func(on bool) { shared = append(shared, on) }

	a, b := sessionDeps(deps), sessionDeps(deps)
	post := tool.Request{Method: http.MethodPost, Path: "/v1/things", Write: true}
	a.Env.SetWrites(true)
	if _, err := a.Env.ASC.Do(context.Background(), post); err != nil {
		t.Fatalf("session A after enable_writes: %v", err)
	}
	if _, err := b.Env.ASC.Do(context.Background(), post); !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("session B write err = %v, want ErrWritesDisabled", err)
	}
	if _, err := b.Env.ASC.Do(context.Background(), tool.Request{Path: "/v1/apps"}); err != nil {
		t.Fatalf("session B read: %v", err)
	}
	a.Env.SetWrites(false)
	if _, err := a.Env.ASC.Do(context.Background(), post); !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("session A after disable err = %v", err)
	}
	if base.calls != 2 || len(shared) != 1 || !shared[0] {
		t.Fatalf("base calls = %d, shared SetWrites = %v", base.calls, shared)
	}
}
