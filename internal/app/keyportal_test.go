package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// keyTestToken is the bearer the fake token source hands out; no error may
// ever contain it.
const keyTestToken = "eyJTESTSECRETJWT.payload.signature"

type fakeTokens struct{}

func (fakeTokens) Token(context.Context) (string, error) { return keyTestToken, nil }
func (fakeTokens) Invalidate(string)                     {}

// keySeen is one request the fake xcbuild host received.
type keySeen struct {
	method, path, override, ctype, auth, cookie, csrf, xcode, ua, body string
}

type keyHarness struct {
	c    *keyPortalClient
	mu   sync.Mutex
	seen []keySeen
}

func (h *keyHarness) requests() []keySeen {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]keySeen(nil), h.seen...)
}

// newKeyHarness serves fn under /xcbuild/ and records requests; sleeps are
// skipped.
func newKeyHarness(t *testing.T, fn http.HandlerFunc) *keyHarness {
	t.Helper()
	h := &keyHarness{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.seen = append(h.seen, keySeen{
			method: r.Method, path: r.URL.Path, override: r.Header.Get("X-HTTP-Method-Override"),
			ctype: r.Header.Get("Content-Type"), auth: r.Header.Get("Authorization"), cookie: r.Header.Get("Cookie"),
			csrf: r.Header.Get("csrf"), xcode: r.Header.Get("X-Xcode-Version"), ua: r.Header.Get("User-Agent"), body: string(b),
		})
		h.mu.Unlock()
		fn(w, r)
	}))
	t.Cleanup(srv.Close)
	h.c = newKeyPortalClient(fakeTokens{}, srv.URL+"/xcbuild/", "9.9")
	h.c.p.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return h
}

// fakeWeb is the web-session client stand-in: it counts calls and answers
// err.
type fakeWeb struct {
	mu    sync.Mutex
	calls []string
	err   error
	allow bool
}

func (f *fakeWeb) Do(_ context.Context, method, path string, _, out any) error {
	f.mu.Lock()
	f.calls = append(f.calls, method+" "+path)
	f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if p, ok := out.(*map[string]any); ok {
		*p = map[string]any{"from": "web"}
	}
	return nil
}

func (f *fakeWeb) SetAllowWrites(on bool) { f.allow = on }

func (f *fakeWeb) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func appleError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"errors": []map[string]string{{"code": "X", "message": msg}}})
	_, _ = w.Write(b)
}

func patchBody() map[string]any {
	return map[string]any{"data": map[string]any{"type": "bundleIds", "id": "B1"}}
}

func TestKeyPortalReadShaping(t *testing.T) {
	h := newKeyHarness(t, func(w http.ResponseWriter, _ *http.Request) { jsonOK(w, `{"data":[{"id":"B1"}]}`) })
	q := url.Values{"filter[identifier]": {"com.example.app"}}
	var out struct {
		Data []struct{ ID string } `json:"data"`
	}
	if err := tool.DevPortalGet(context.Background(), h.c, "v1/bundleIds", q, "TEAM123456", &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 || out.Data[0].ID != "B1" {
		t.Fatalf("out = %+v", out)
	}
	if err := tool.DevPortalGet(context.Background(), h.c, "v1/bundleIds/B1", q, "TEAM123456", nil); err != nil {
		t.Fatal(err)
	}
	cq := url.Values{"filter[bundleId]": {"B1"}}
	if err := tool.DevPortalGet(context.Background(), h.c, "v1/capabilities", cq, "TEAM123456", nil); err != nil {
		t.Fatal(err)
	}
	got := h.requests()
	if len(got) != 3 {
		t.Fatalf("requests = %+v", got)
	}
	// The exact bytes tool.DevPortalGet encodes (its struct's field order).
	want, _ := json.Marshal(struct {
		Q    string `json:"urlEncodedQueryParams"`
		Team string `json:"teamId"`
	}{q.Encode(), "TEAM123456"})
	wantPaths := []string{"/xcbuild/v1/bundleIds", "/xcbuild/v1/bundleIds/B1", "/xcbuild/v1/capabilities"}
	for i, r := range got {
		if r.method != http.MethodPost || r.override != http.MethodGet || r.path != wantPaths[i] || r.ctype != "application/vnd.api+json" {
			t.Fatalf("request %d = %+v", i, r)
		}
		if r.auth != "Bearer "+keyTestToken || r.cookie != "" || r.csrf != "" || r.ua != "app-store-connect-mcp/9.9" {
			t.Fatalf("request %d headers = %+v", i, r)
		}
		if i < 2 && r.body != string(want) {
			t.Fatalf("body %d = %s, want %s", i, r.body, want)
		}
		if wantX := i == 2; (r.xcode != "") != wantX {
			t.Fatalf("request %d X-Xcode-Version = %q", i, r.xcode)
		}
	}
	if got[2].xcode != keyPortalXcodeVersion {
		t.Fatalf("X-Xcode-Version = %q", got[2].xcode)
	}
}

func TestKeyPortalPatchShaping(t *testing.T) {
	h := newKeyHarness(t, func(w http.ResponseWriter, _ *http.Request) { jsonOK(w, `{"data":{"id":"B1"}}`) })
	h.c.SetAllowWrites(true)
	if err := h.c.Do(devWriteCtx(t), http.MethodPatch, "v1/bundleIds/B1", patchBody(), nil); err != nil {
		t.Fatal(err)
	}
	r := h.requests()[0]
	want, _ := json.Marshal(patchBody())
	if r.method != http.MethodPatch || r.override != "" || r.path != "/xcbuild/v1/bundleIds/B1" || r.ctype != "application/vnd.api+json" ||
		r.auth != "Bearer "+keyTestToken || r.cookie != "" || r.xcode != "" || r.body != string(want) {
		t.Fatalf("patch = %+v", r)
	}
}

func TestKeyPortalServes(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "v1/bundleIds", true},
		{http.MethodGet, "v1/bundleIds/B1", true},
		{http.MethodGet, "v1/capabilities", true},
		{http.MethodPatch, "v1/bundleIds/B1", true},
		{http.MethodPatch, "v1/bundleIds", false},
		{http.MethodPatch, "v1/capabilities", false},
		{http.MethodGet, "v1/bundleIds/B1/x", false},
		{http.MethodGet, "v1/bundleIds/", false},
		{http.MethodGet, "v1/bundleIds/..", false},
		{http.MethodGet, "/v1/bundleIds", false},
		{http.MethodGet, "v1/bundleIds?x=1", false},
		{http.MethodGet, "v1/bundleIds/B%31", false},
		{http.MethodGet, "v1/appGroups", false},
		{http.MethodPost, "v1/appGroups", false},
		{http.MethodPatch, "v1/bundleIdCapabilities/C1", false},
		{http.MethodDelete, "v1/bundleIds/B1", false},
		{http.MethodPost, devPortalLegacyPrefix + "listApplicationGroups.action", false},
	} {
		if got := keyPortalServes(tc.method, tc.path); got != tc.want {
			t.Errorf("keyPortalServes(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestKeyPortalUnservedGoesToWeb(t *testing.T) {
	h := newKeyHarness(t, func(w http.ResponseWriter, _ *http.Request) { jsonOK(w, `{}`) })
	web := &fakeWeb{}
	r := &devPortalRouter{key: h.c, web: web}
	r.SetAllowWrites(true)
	ctx := devWriteCtx(t)
	read := map[string]string{"teamId": "TEAM123456"}
	calls := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "v1/appGroups", read},
		{http.MethodPost, "v1/appGroups", map[string]any{"data": map[string]any{}}},
		{http.MethodPost, devPortalLegacyPrefix + "listApplicationGroups.action", url.Values{"teamId": {"TEAM123456"}}},
		{http.MethodPatch, "v1/bundleIdCapabilities/C1", patchBody()},
	}
	for _, c := range calls {
		if err := r.Do(ctx, c.method, c.path, c.body, nil); err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("key server got %d requests: %+v", n, h.requests())
	}
	if web.count() != len(calls) {
		t.Fatalf("web calls = %v", web.calls)
	}
}

func TestKeyPortalUnservedWithoutWeb(t *testing.T) {
	h := newKeyHarness(t, func(w http.ResponseWriter, _ *http.Request) { jsonOK(w, `{}`) })
	r := &devPortalRouter{key: h.c}
	err := r.Do(context.Background(), http.MethodGet, "v1/appGroups", map[string]string{"teamId": "T"}, nil)
	if !errors.Is(err, tool.ErrWebSessionRequired) {
		t.Fatalf("err = %v", err)
	}
	// A path the web client would refuse too stays a plain refusal.
	err = r.Do(context.Background(), http.MethodGet, "v1/../x", map[string]string{"teamId": "T"}, nil)
	if err == nil || errors.Is(err, tool.ErrWebSessionRequired) {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("key server got %d requests", n)
	}
}

func TestKeyPortalWriteGates(t *testing.T) {
	h := newKeyHarness(t, func(w http.ResponseWriter, _ *http.Request) { jsonOK(w, `{}`) })
	web := &fakeWeb{}
	r := &devPortalRouter{key: h.c, web: web}
	// Allowed but not a write context.
	r.SetAllowWrites(true)
	if err := r.Do(context.Background(), http.MethodPatch, "v1/bundleIds/B1", patchBody(), nil); !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("no write ctx: err = %v", err)
	}
	// A write context but writes off.
	r.SetAllowWrites(false)
	if web.allow {
		t.Fatal("SetAllowWrites(false) did not reach the web client")
	}
	if err := r.Do(devWriteCtx(t), http.MethodPatch, "v1/bundleIds/B1", patchBody(), nil); !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("writes off: err = %v", err)
	}
	if n := len(h.requests()); n != 0 || web.count() != 0 {
		t.Fatalf("sent: key %d, web %v", n, web.calls)
	}
}

func TestKeyPortalRefusedFallsBackOnce(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		h := newKeyHarness(t, func(w http.ResponseWriter, _ *http.Request) { appleError(w, status, "Authentication failed") })
		web := &fakeWeb{}
		r := &devPortalRouter{key: h.c, web: web}
		var out map[string]any
		if err := tool.DevPortalGet(context.Background(), r, "v1/bundleIds/B1", nil, "TEAM123456", &out); err != nil {
			t.Fatalf("%d: %v", status, err)
		}
		if out["from"] != "web" || web.count() != 1 || len(h.requests()) != 1 {
			t.Fatalf("%d: out %v, web %v, key %d", status, out, web.calls, len(h.requests()))
		}
		// A refused PATCH goes to the web session once too.
		r.SetAllowWrites(true)
		if err := r.Do(devWriteCtx(t), http.MethodPatch, "v1/bundleIds/B1", patchBody(), nil); err != nil {
			t.Fatalf("%d patch: %v", status, err)
		}
		if web.count() != 2 || len(h.requests()) != 2 {
			t.Fatalf("%d patch: web %v, key %d", status, web.calls, len(h.requests()))
		}
	}
}

func TestKeyPortalRefusedWithoutWebSession(t *testing.T) {
	const msg = "Authentication failed. Make sure a bearer token was provided"
	h := newKeyHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		appleError(w, http.StatusUnauthorized, msg+" (got "+keyTestToken+")")
	})
	for name, r := range map[string]*devPortalRouter{
		"no web client":     {key: h.c},
		"web needs sign-in": {key: h.c, web: &fakeWeb{err: tool.ErrWebSessionRequired}},
	} {
		err := tool.DevPortalGet(context.Background(), r, "v1/bundleIds/B1", nil, "TEAM123456", nil)
		if !errors.Is(err, tool.ErrWebSessionRequired) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if s := err.Error(); !strings.Contains(s, "401") || !strings.Contains(s, msg) || strings.Contains(s, keyTestToken) {
			t.Fatalf("%s: err text = %q", name, s)
		}
	}
}

func TestKeyPortalFinalAnswersDoNotFallBack(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusInternalServerError} {
		h := newKeyHarness(t, func(w http.ResponseWriter, _ *http.Request) { appleError(w, status, "nope") })
		web := &fakeWeb{}
		r := &devPortalRouter{key: h.c, web: web}
		r.SetAllowWrites(true)
		err := r.Do(devWriteCtx(t), http.MethodPatch, "v1/bundleIds/B1", patchBody(), nil)
		var pe *tool.PortalError
		if !errors.As(err, &pe) || pe.Status != status {
			t.Fatalf("%d: err = %v", status, err)
		}
		if errors.Is(err, errKeyRefused) || errors.Is(err, tool.ErrWebSessionRequired) {
			t.Fatalf("%d: err = %v", status, err)
		}
		if n := len(h.requests()); n != 1 || web.count() != 0 {
			t.Fatalf("%d: key %d, web %v", status, n, web.calls)
		}
	}
}

func TestKeyPortalReadRetried(t *testing.T) {
	var mu sync.Mutex
	n := 0
	h := newKeyHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if first {
			appleError(w, http.StatusServiceUnavailable, "busy")
			return
		}
		jsonOK(w, `{"data":{"id":"B1"}}`)
	})
	web := &fakeWeb{}
	r := &devPortalRouter{key: h.c, web: web}
	if err := tool.DevPortalGet(context.Background(), r, "v1/bundleIds/B1", nil, "TEAM123456", nil); err != nil {
		t.Fatal(err)
	}
	if got := len(h.requests()); got != 2 || web.count() != 0 {
		t.Fatalf("key %d, web %v", got, web.calls)
	}
}

func TestKeyPortalTokenNeverInErrors(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusConflict, http.StatusInternalServerError} {
		h := newKeyHarness(t, func(w http.ResponseWriter, r *http.Request) {
			appleError(w, status, "bad "+r.Header.Get("Authorization"))
		})
		h.c.SetAllowWrites(true)
		errs := []error{
			tool.DevPortalGet(context.Background(), h.c, "v1/capabilities", nil, "TEAM123456", nil),
			h.c.Do(devWriteCtx(t), http.MethodPatch, "v1/bundleIds/B1", patchBody(), nil),
			tool.DevPortalGet(context.Background(), &devPortalRouter{key: h.c}, "v1/bundleIds/B1", nil, "TEAM123456", nil),
		}
		for i, err := range errs {
			if err == nil {
				t.Fatalf("%d/%d: no error", status, i)
			}
			if strings.Contains(err.Error(), keyTestToken) {
				t.Fatalf("%d/%d: token in %q", status, i, err.Error())
			}
		}
	}
}
