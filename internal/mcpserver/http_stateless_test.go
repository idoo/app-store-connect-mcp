package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// strongToken passes the HTTP token strength check (32+ chars, 8+ distinct).
var strongToken = strings.Repeat("stateless-mcp-token-", 2)

// headerRecorder sends the bearer token and records every response's
// Mcp-Session-Id header.
type headerRecorder struct {
	token string
	mu    sync.Mutex
	ids   []string
}

func (h *headerRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+h.token)
	res, err := http.DefaultTransport.RoundTrip(r)
	if err == nil {
		h.mu.Lock()
		h.ids = append(h.ids, res.Header.Get(sessionIDHeader))
		h.mu.Unlock()
	}
	return res, err //nolint:wrapcheck // test transport passes errors through.
}

// statelessHarness serves deps over HTTP with both handlers and connects a
// 2026-07-28 client that returns input-required results unanswered.
func statelessHarness(t *testing.T, deps Deps) (*harness, *headerRecorder) {
	t.Helper()
	h, err := HTTPHandler(HTTPOptions{Token: strongToken, NewServer: SessionFactory(deps), NewStatelessServer: StatelessFactory(deps)})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	rec := &headerRecorder{token: strongToken}
	tr := &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: &http.Client{Transport: rec}}
	opts := &mcp.ClientOptions{
		ElicitationHandler: answerWith("accept", new(int)),
		MultiRoundTrip:     &mcp.MultiRoundTripOptions{Disabled: true},
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, opts).Connect(context.Background(), tr, &mcp.ClientSessionOptions{ProtocolVersion: statelessProtocolVersion})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &harness{cs: cs}, rec
}

func TestHTTPStatelessApproval(t *testing.T) {
	deps, fake, _ := approvalDeps(t, "work", false)
	h, rec := statelessHarness(t, deps)
	if got := h.cs.InitializeResult().ProtocolVersion; got != statelessProtocolVersion {
		t.Fatalf("negotiated %q, want %s", got, statelessProtocolVersion)
	}
	args := map[string]any{"app": "t1", "confirm": true}
	state := ask(t, h, "release_submit", args)
	if n := len(fake.Mutations()); n != 0 {
		t.Fatalf("mutations before approval = %d", n)
	}
	res, body := retry(t, h, "release_submit", args, state, "accept")
	if res.IsError || approvedBy(body) != tool.ApprovedHuman || len(fake.Mutations()) != 1 {
		t.Fatalf("accepted retry = %v, mutations = %d", body, len(fake.Mutations()))
	}
	res, body = retry(t, h, "release_submit", args, state, "accept")
	if !res.IsError || dataOf(body)["error"] != tool.ErrInvalidArgs.Code || len(fake.Mutations()) != 1 {
		t.Fatalf("replay = %v, mutations = %d", body, len(fake.Mutations()))
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.ids) == 0 || slices.ContainsFunc(rec.ids, func(id string) bool { return id != "" }) {
		t.Fatalf("stateless responses carried session ids: %q", rec.ids)
	}
}

func TestHTTPStatelessRefusesRuntimeToggles(t *testing.T) {
	deps := Deps{Tools: (&fixture{}).tools(), Profile: team(false)}
	h, _ := statelessHarness(t, deps)
	for _, args := range []map[string]any{
		{"action": "enable_writes"},
		{"action": "enable", "toolsets": []string{"signing"}},
		{"action": "disable", "toolsets": []string{"release"}},
	} {
		res, body := h.call(t, toolsetsName, args)
		d := dataOf(body)
		if !res.IsError || d["error"] != errStatelessFixed.Code || !strings.Contains(d["hint"].(string), "--allow-writes") {
			t.Fatalf("%v = %v", args, body)
		}
	}
	if res, _ := h.call(t, toolsetsName, map[string]any{"action": "list"}); res.IsError {
		t.Fatal("list refused on the stateless path")
	}
	if got := h.names(t); slices.Contains(got, "release_submit") {
		t.Fatalf("write tools listed in read-only stateless mode: %v", got)
	}
	// Calling a hidden tool never steers to toolsets: it can't help here.
	for name, code := range map[string]string{"release_submit": "writes_disabled", "signing_bundle_ids_list": "toolset_disabled"} {
		res, body := h.call(t, name, map[string]any{})
		d := dataOf(body)
		if !res.IsError || d["error"] != code || !strings.Contains(d["hint"].(string), "restart") || strings.Contains(fmt.Sprint(body), "tool:toolsets") {
			t.Fatalf("%s = %v", name, body)
		}
	}
}

func TestHTTPStatelessNeedsAuthAndOrigin(t *testing.T) {
	h, err := HTTPHandler(HTTPOptions{
		Token: strongToken, NewServer: SessionFactory(Deps{Tools: []tool.Tool{}}),
		NewStatelessServer: StatelessFactory(Deps{Tools: []tool.Tool{}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		auth, origin string
		want         int
	}{
		{"", "", http.StatusUnauthorized},
		{"Bearer " + strongToken, "https://evil.example", http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"server/discover"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set(protocolVersionHeader, statelessProtocolVersion)
		if tt.auth != "" {
			req.Header.Set("Authorization", tt.auth)
		}
		if tt.origin != "" {
			req.Header.Set("Origin", tt.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("auth %q origin %q: status %d, want %d", tt.auth, tt.origin, rec.Code, tt.want)
		}
	}
}

func TestCheckToken(t *testing.T) {
	for tok, ok := range map[string]bool{
		strongToken:                    true,
		"s3cret":                       false,
		strings.Repeat("a", 64):        false,
		strings.Repeat("abcd", 16):     false,
		strongToken[:minTokenLen-1]:    false,
		"Xk9#mP2$vL7qR4tW8zN1bC6dF3hJ": false,
	} {
		if err := CheckToken(tok); (err == nil) != ok {
			t.Errorf("CheckToken(%q) = %v, want ok=%v", tok, err, ok)
		}
	}
	err := ServeHTTP(context.Background(), "127.0.0.1:0", HTTPOptions{Token: "short", NewServer: func(*http.Request) *mcp.Server { return nil }})
	if err == nil || !strings.Contains(err.Error(), EnvHTTPToken) || !strings.Contains(err.Error(), "unset it") {
		t.Fatalf("weak token at startup: err = %v", err)
	}
}

func TestHTTPCORS(t *testing.T) {
	h := testHTTPHandler(t)
	preflightReq := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("Access-Control-Request-Method", "POST")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, origin := range []string{"https://app.example", "http://localhost:5173"} {
		rec := preflightReq(origin)
		hd := rec.Header()
		if rec.Code != http.StatusNoContent || hd.Get("Access-Control-Allow-Origin") != origin || hd.Get("Vary") != "Origin" ||
			hd.Get("Access-Control-Allow-Headers") != corsAllowHeaders || hd.Get("Access-Control-Allow-Methods") != corsAllowMethods ||
			hd.Get("Access-Control-Max-Age") == "" {
			t.Errorf("preflight %s = %d %v", origin, rec.Code, hd)
		}
	}
	for _, origin := range []string{"https://evil.example", ""} {
		if rec := preflightReq(origin); rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("preflight %q = %d %v", origin, rec.Code, rec.Header())
		}
	}
	rec := doHTTP(t, h, "/mcp", "Bearer s3cret", "https://app.example")
	hd := rec.Header()
	if rec.Code != http.StatusOK || hd.Get("Access-Control-Allow-Origin") != "https://app.example" || hd.Get("Access-Control-Expose-Headers") != sessionIDHeader {
		t.Errorf("actual response = %d %v", rec.Code, hd)
	}
	if rec := doHTTP(t, h, "/mcp", "Bearer s3cret", ""); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("no-origin response carries CORS: %v", rec.Header())
	}
}
