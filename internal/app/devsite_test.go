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
	"sync/atomic"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// siteReq is one request the fake developer site received.
type siteReq struct {
	method, path, query, override, ctype, accept, referer, origin, csrf, token, body string
}

type siteHarness struct {
	c    *devSiteClient
	src  *fakeSession
	srv  *httptest.Server
	mu   sync.Mutex
	seen []siteReq
}

func (h *siteHarness) requests() []siteReq {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]siteReq(nil), h.seen...)
}

// newSiteHarness serves fn as developer.apple.com and records requests.
// Requests to the sign-in host are answered by an HTML sign-in page and
// recorded with path "idmsa:<path>". Sleeps are skipped.
func newSiteHarness(t *testing.T, fn http.HandlerFunc) *siteHarness {
	t.Helper()
	h := &siteHarness{}
	record := func(r *http.Request, path string) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.seen = append(h.seen, siteReq{
			method: r.Method, path: path, query: r.URL.RawQuery, override: r.Header.Get("X-HTTP-Method-Override"),
			ctype: r.Header.Get("Content-Type"), accept: r.Header.Get("Accept"), referer: r.Header.Get("Referer"),
			origin: r.Header.Get("Origin"), csrf: r.Header.Get("csrf"), token: r.Header.Get("X-CSRF-Token"), body: string(b),
		})
		h.mu.Unlock()
	}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(r, r.URL.Path)
		fn(w, r)
	}))
	t.Cleanup(h.srv.Close)
	h.src = &fakeSession{cookie: "SECRETCOOKIE"}
	d := newDevPortalClient(h.src, h.srv.URL+"/services-account/", "9.9")
	d.p.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	d.p.hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == devSiteSignInHost {
			record(r, "idmsa:"+r.URL.Path)
			rec := httptest.NewRecorder()
			rec.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(rec, "<html>Sign in</html>")
			resp := rec.Result()
			resp.Request = r
			return resp, nil
		}
		if r.URL.Host != strings.TrimPrefix(h.srv.URL, "http://") {
			t.Errorf("request left for %s", r.URL.Host)
			return nil, errors.New("unexpected host")
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	h.c = newDevSiteClient(d)
	return h
}

// holdCSRF gives the client a csrf pair so writes do not prime.
func (h *siteHarness) holdCSRF() {
	h.c.d.mu.Lock()
	h.c.d.csrf, h.c.d.csrfTS = "CSRFVALUE", "1"
	h.c.d.mu.Unlock()
}

func TestDevSiteAPIWireShapes(t *testing.T) {
	q := url.Values{"filter[identifier]": {"com.example.app"}}
	for _, tc := range []struct {
		name                         string
		call                         tool.DevSiteCall
		method, override, ctype, raw string
		check                        func(t *testing.T, r siteReq)
	}{
		{
			name: "v1 GET", call: tool.DevSiteCall{Method: http.MethodGet, Path: "v1/bundleIds", Query: q, TeamID: "TEAM1"},
			method: http.MethodPost, override: "GET", ctype: "application/vnd.api+json",
			check: func(t *testing.T, r siteReq) {
				var b map[string]string
				if err := json.Unmarshal([]byte(r.body), &b); err != nil || b["teamId"] != "TEAM1" || b["urlEncodedQueryParams"] != q.Encode() {
					t.Fatalf("body = %s", r.body)
				}
			},
		},
		{
			name: "v1 DELETE", call: tool.DevSiteCall{Method: http.MethodDelete, Path: "v1/keys/K1", TeamID: "TEAM1"},
			method: http.MethodPost, override: "DELETE", ctype: "application/vnd.api+json",
			check: func(t *testing.T, r siteReq) {
				var b map[string]string
				if err := json.Unmarshal([]byte(r.body), &b); err != nil || b["teamId"] != "TEAM1" {
					t.Fatalf("body = %s", r.body)
				}
			},
		},
		{
			name: "v1 PATCH", call: tool.DevSiteCall{Method: http.MethodPatch, Path: "v1/bundleIds/B1", TeamID: "TEAM1", JSON: map[string]any{"data": map[string]any{"id": "B1"}}},
			method: http.MethodPatch, ctype: "application/vnd.api+json",
			check: func(t *testing.T, r siteReq) {
				if r.body != `{"data":{"id":"B1"}}` {
					t.Fatalf("body = %s (team id must not be added)", r.body)
				}
			},
		},
		{
			name: "legacy form", call: tool.DevSiteCall{Method: http.MethodPost, Path: "QH65B2/account/auth/key/create", TeamID: "TEAM1", Form: url.Values{"name": {"K"}}},
			method: http.MethodPost, ctype: "application/x-www-form-urlencoded",
			check: func(t *testing.T, r siteReq) {
				if f, _ := url.ParseQuery(r.body); f.Get("teamId") != "TEAM1" || f.Get("name") != "K" {
					t.Fatalf("body = %s", r.body)
				}
			},
		},
		{
			name: "legacy JSON", call: tool.DevSiteCall{Method: http.MethodPost, Path: "QH65B2/account/auth/key/edit", TeamID: "TEAM1", JSON: map[string]string{"a": "b"}},
			method: http.MethodPost, ctype: "application/json",
			check: func(t *testing.T, r siteReq) {
				if r.body != `{"a":"b"}` {
					t.Fatalf("body = %s", r.body)
				}
			},
		},
		{
			name: "legacy GET", call: tool.DevSiteCall{Method: http.MethodGet, Path: "QH65B2/account/auth/key/list", TeamID: "TEAM1", Query: url.Values{"x": {"1"}}},
			method: http.MethodGet,
			check: func(t *testing.T, r siteReq) {
				v, _ := url.ParseQuery(r.query)
				if v.Get("teamId") != "TEAM1" || v.Get("x") != "1" || r.body != "" || r.accept != "application/json, text/plain, */*" {
					t.Fatalf("request = %+v", r)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSiteHarness(t, func(w http.ResponseWriter, _ *http.Request) { jsonOK(w, `{"resultCode":0}`) })
			h.c.SetAllowWrites(true)
			h.holdCSRF()
			ans, err := h.c.API(devWriteCtx(t), tc.call)
			if err != nil {
				t.Fatal(err)
			}
			if ans.Status != http.StatusOK || string(ans.Body) != `{"resultCode":0}` {
				t.Fatalf("answer = %+v", ans)
			}
			got := h.requests()
			if len(got) != 1 {
				t.Fatalf("requests = %+v", got)
			}
			r := got[0]
			if r.method != tc.method || r.override != tc.override || r.ctype != tc.ctype || r.path != "/services-account/"+tc.call.Path {
				t.Fatalf("request = %+v", r)
			}
			if !strings.HasSuffix(r.referer, "/account/resources/") {
				t.Fatalf("referer = %q", r.referer)
			}
			tc.check(t, r)
		})
	}
}

func TestDevSiteAPIRefusesUnknownShapes(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, _ *http.Request) { jsonOK(w, `{}`) })
	h.c.SetAllowWrites(true)
	ctx := devWriteCtx(t)
	for name, call := range map[string]tool.DevSiteCall{
		"JSON and Form": {Method: http.MethodPost, Path: "QH65B2/account/x/create.action", JSON: map[string]string{}, Form: url.Values{}},
		"v1 with Form":  {Method: http.MethodPost, Path: "v1/bundleIds", Form: url.Values{"a": {"b"}}},
		"v1 GET JSON":   {Method: http.MethodGet, Path: "v1/bundleIds", JSON: map[string]string{}},
		"unknown":       {Method: "TRACE", Path: "v1/bundleIds"},
		"legacy PUT":    {Method: http.MethodPut, Path: "QH65B2/account/x.action", JSON: map[string]string{}},
		"bad path":      {Method: http.MethodGet, Path: "v1/../v1/bundleIds"},
	} {
		if _, err := h.c.API(ctx, call); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("%d requests left", n)
	}
}

func TestDevSiteWriteGating(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, _ *http.Request) { jsonOK(w, `{"resultCode":0}`) })
	write := tool.DevSiteCall{Method: http.MethodPost, Path: "QH65B2/account/auth/key/create", Form: url.Values{}}
	h.c.SetAllowWrites(true)
	if _, err := h.c.API(context.Background(), write); !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("no write context: %v", err)
	}
	h.c.SetAllowWrites(false)
	if _, err := h.c.API(devWriteCtx(t), write); !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("writes off: %v", err)
	}
	if _, err := h.c.Submit(devWriteCtx(t), tool.DevSitePost{Path: "contact/form", ContentType: "application/json"}); !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("submit, writes off: %v", err)
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("%d requests left", n)
	}
	if _, err := h.c.API(context.Background(), tool.DevSiteCall{Method: http.MethodPost, Path: "QH65B2/account/auth/key/list"}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestDevSiteWritePrimesCSRF(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/listTeams.action") {
			w.Header().Set("csrf", "MINTED")
			w.Header().Set("csrf_ts", "2")
		}
		jsonOK(w, `{"resultCode":0}`)
	})
	h.c.SetAllowWrites(true)
	ctx := devWriteCtx(t)
	write := tool.DevSiteCall{Method: http.MethodPost, Path: "QH65B2/account/auth/key/create", Form: url.Values{}}
	if _, err := h.c.API(ctx, write); err != nil {
		t.Fatal(err)
	}
	got := h.requests()
	if len(got) != 2 || got[0].path != "/services-account/"+devSitePrimePath || got[0].method != http.MethodPost || got[0].body != "" {
		t.Fatalf("requests = %+v", got)
	}
	if got[1].csrf != "MINTED" {
		t.Fatalf("write csrf = %q", got[1].csrf)
	}
	if _, err := h.c.API(ctx, write); err != nil {
		t.Fatal(err)
	}
	if n := len(h.requests()); n != 3 {
		t.Fatalf("held pair still primed: %d requests", n)
	}
}

// The request history is a v1 POST that only reads: it needs no write
// context, and it carries the csrf pair like a write.
func TestDevSiteV1PostReadPrimesCSRF(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/listTeams.action") {
			w.Header().Set("csrf", "MINTED")
			w.Header().Set("csrf_ts", "2")
		}
		jsonOK(w, `{"data":{"type":"featureRequest"}}`)
	})
	read := tool.DevSiteCall{Method: http.MethodPost, Path: "v1/featureRequest", JSON: map[string]any{"data": map[string]any{"type": "featureRequest"}}}
	if _, err := h.c.API(context.Background(), read); err != nil {
		t.Fatal(err)
	}
	got := h.requests()
	if len(got) != 2 || got[0].path != "/services-account/"+devSitePrimePath || got[1].path != "/services-account/v1/featureRequest" ||
		got[1].method != http.MethodPost || got[1].csrf != "MINTED" {
		t.Fatalf("requests = %+v", got)
	}
	// Any other v1 POST is a write and stops at the gate without a request.
	other := tool.DevSiteCall{Method: http.MethodPost, Path: "v1/bundleIds", JSON: map[string]any{"data": map[string]any{}}}
	if _, err := h.c.API(context.Background(), other); !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("v1 POST write without a write context: %v", err)
	}
	if n := len(h.requests()); n != 2 {
		t.Fatalf("a refused write reached the site: %d requests", n)
	}
}

func TestDevSiteWrite5xxIsOutcomeUnknown(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		jsonOK(w, `{}`)
	})
	h.c.SetAllowWrites(true)
	h.holdCSRF()
	_, err := h.c.API(devWriteCtx(t), tool.DevSiteCall{Method: http.MethodPost, Path: "QH65B2/account/auth/key/create", Form: url.Values{"secret": {"BODYSECRET"}}})
	if !errors.Is(err, tool.ErrOutcomeUnknown) {
		t.Fatalf("err = %v", err)
	}
	for _, s := range []string{"BODYSECRET", "SECRETCOOKIE", "CSRFVALUE"} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks %s: %v", s, err)
		}
	}
	if n := len(h.requests()); n != 1 {
		t.Fatalf("write sent %d times", n)
	}
}

func TestDevSiteRead503IsRetried(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	h := newSiteHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		jsonOK(w, `{"data":[]}`)
	})
	if _, err := h.c.API(context.Background(), tool.DevSiteCall{Method: http.MethodGet, Path: "v1/bundleIds"}); err != nil {
		t.Fatal(err)
	}
	if n := len(h.requests()); n != 2 {
		t.Fatalf("%d requests", n)
	}
}

func TestDevSiteExpiryBootstrapsThenSucceeds(t *testing.T) {
	var mu sync.Mutex
	booted := false
	h := newSiteHarness(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/account/" {
			booted = true
			return
		}
		if !booted {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		jsonOK(w, `{"data":[]}`)
	})
	ans, err := h.c.API(context.Background(), tool.DevSiteCall{Method: http.MethodGet, Path: "v1/bundleIds"})
	if err != nil || ans.Status != http.StatusOK {
		t.Fatalf("answer = %+v, err = %v", ans, err)
	}
	if h.src.expired != 0 {
		t.Fatalf("expired = %d", h.src.expired)
	}
}

func TestDevSiteDownloadIsNotSignIn(t *testing.T) {
	pem := "-----BEGIN " + "PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49\n-----END " + "PRIVATE KEY-----\n"
	h := newSiteHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, pem)
	})
	h.c.SetAllowWrites(true)
	h.holdCSRF()
	ans, err := h.c.API(devWriteCtx(t), tool.DevSiteCall{Method: http.MethodGet, Path: "QH65B2/account/auth/key/download", Query: url.Values{"keyId": {"K1"}}})
	if err != nil || string(ans.Body) != pem {
		t.Fatalf("answer = %+v, err = %v", ans, err)
	}
	if n := len(h.requests()); n != 1 {
		t.Fatalf("%d requests (bootstrapped?)", n)
	}
}

func TestDevSiteHTMLAnswerIsSignIn(t *testing.T) {
	for name, call := range map[string]tool.DevSiteCall{
		"JSON call":       {Method: http.MethodGet, Path: "v1/bundleIds"},
		"legacy download": {Method: http.MethodGet, Path: "QH65B2/account/auth/key/list"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSiteHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(w, "\n<!DOCTYPE html><html>Sign in</html>")
			})
			if _, err := h.c.API(context.Background(), call); !errors.Is(err, tool.ErrWebSessionRequired) {
				t.Fatalf("err = %v", err)
			}
			if h.src.expired != 1 {
				t.Fatalf("expired = %d", h.src.expired)
			}
		})
	}
}

func TestDevSiteLegacyResultCodeIsPortalError(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		jsonOK(w, `{"resultCode":35,"userString":"No team"}`)
	})
	_, err := h.c.API(context.Background(), tool.DevSiteCall{Method: http.MethodPost, Path: "QH65B2/account/auth/key/list"})
	var pe *tool.PortalError
	if !errors.As(err, &pe) || len(pe.Messages) != 1 || pe.Messages[0] != "No team" {
		t.Fatalf("err = %v", err)
	}
}

func TestDevSitePageFollowsChainInsideSite(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/account/a":
			http.SetCookie(w, &http.Cookie{Name: "A", Value: "1"})
			http.Redirect(w, r, "/account/b", http.StatusFound)
		case "/account/b":
			http.SetCookie(w, &http.Cookie{Name: "B", Value: "2"})
			http.Redirect(w, r, "/account/c?x=1", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>ok</html>")
		}
	})
	ans, err := h.c.Page(context.Background(), "account/a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ans.Status != http.StatusOK || ans.URL != h.srv.URL+"/account/c?x=1" || string(ans.Body) != "<html>ok</html>" || ans.ContentType != "text/html" {
		t.Fatalf("answer = %+v", ans)
	}
	if h.src.observed != 3 {
		t.Fatalf("observed %d answers", h.src.observed)
	}
	if got := h.requests(); len(got) != 3 || got[0].accept != "text/html,application/xhtml+xml" {
		t.Fatalf("requests = %+v", got)
	}
}

func TestDevSitePageRefusesOffSiteHop(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.com/x", http.StatusFound)
	})
	_, err := h.c.Page(context.Background(), "account/a", nil)
	if err == nil || errors.Is(err, tool.ErrWebSessionRequired) {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.requests()); n != 1 {
		t.Fatalf("%d requests", n)
	}
}

func TestDevSitePageEndingOnSignInIsNoSession(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://idmsa.apple.com/IDMSWebAuth/signin", http.StatusFound)
	})
	if _, err := h.c.Page(context.Background(), "account/", nil); err == nil {
		t.Fatal("trailing slash path accepted")
	}
	_, err := h.c.Page(context.Background(), "account", nil)
	if !errors.Is(err, tool.ErrWebSessionRequired) {
		t.Fatalf("err = %v", err)
	}
	if h.src.expired != 1 {
		t.Fatalf("expired = %d", h.src.expired)
	}
	if n := len(h.requests()); n != 4 {
		t.Fatalf("%d requests, want two tries of two hops", n)
	}
}

func TestDevSitePageRefusesOtherRoots(t *testing.T) {
	h := newSiteHarness(t, func(http.ResponseWriter, *http.Request) {})
	for _, p := range []string{"help/x", "services-account/v1/bundleIds", "/account/x", "account/../help"} {
		if _, err := h.c.Page(context.Background(), p, nil); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestDevSitePageTooLarge(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(make([]byte, devSitePageMax+1))
	})
	if _, err := h.c.Page(context.Background(), "account", nil); err == nil {
		t.Fatal("no error")
	}
}

func TestDevSiteSubmitRefusesHeaderOutsideAllowList(t *testing.T) {
	h := newSiteHarness(t, func(http.ResponseWriter, *http.Request) {})
	h.c.SetAllowWrites(true)
	ctx := devWriteCtx(t)
	for name, post := range map[string]tool.DevSitePost{
		"cookie header":      {Path: "contact/form", ContentType: "application/json", Headers: map[string]string{"Cookie": "x=1"}},
		"multipart no bound": {Path: "contact/form", ContentType: "multipart/form-data"},
		"text body":          {Path: "contact/form", ContentType: "text/plain"},
		"referer off root":   {Path: "contact/form", Referer: "help/x", ContentType: "application/json"},
	} {
		if _, err := h.c.Submit(ctx, post); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestDevSiteSubmitSendsAsGivenAndReturnsRedirect(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/contact/thanks", http.StatusFound)
	})
	h.c.SetAllowWrites(true)
	body := "--B\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\n1\r\n--B--\r\n"
	ans, err := h.c.Submit(devWriteCtx(t), tool.DevSitePost{
		Path: "contact/request/submit", Referer: "contact/request/form", ContentType: "multipart/form-data; boundary=B",
		Body: []byte(body), Headers: map[string]string{"x-csrf-token": "TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Status != http.StatusFound || ans.Location != h.srv.URL+"/contact/thanks" {
		t.Fatalf("answer = %+v", ans)
	}
	got := h.requests()
	if len(got) != 1 {
		t.Fatalf("redirect followed: %+v", got)
	}
	r := got[0]
	if r.method != http.MethodPost || r.body != body || r.ctype != "multipart/form-data; boundary=B" || r.token != "TOKEN" ||
		r.origin != h.srv.URL || r.referer != h.srv.URL+"/contact/request/form" {
		t.Fatalf("request = %+v", r)
	}
}

func TestDevSiteSubmitRedirectToSignInIsNoSession(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://idmsa.apple.com/signin", http.StatusFound)
	})
	h.c.SetAllowWrites(true)
	_, err := h.c.Submit(devWriteCtx(t), tool.DevSitePost{Path: "contact/form", ContentType: "application/x-www-form-urlencoded", Body: []byte("a=BODYSECRET")})
	if !errors.Is(err, tool.ErrWebSessionRequired) || strings.Contains(err.Error(), "BODYSECRET") {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.requests()); n != 1 {
		t.Fatalf("%d requests", n)
	}
}

func TestDevSitePathSegments(t *testing.T) {
	for _, tc := range []struct {
		path      string
		api, page bool
	}{
		{"QH65B2/account/auth/key/list", true, false},
		{"v1/bundleIds/ABC123XYZ9", true, false},
		{"account/resources/identifiers/list", true, true},
		{"contact/request/notification-service", true, true},
		{"account/..;/services-account/x", false, false},
		{"account/a;b", false, false},
		{"account/a:b", false, false},
		{"account/a@b", false, false},
		{"account//b", false, false},
		{"account/./b", false, false},
		{"v1/a:b", false, false},
		{"v1/a@b", false, false},
		{"v1//b", false, false},
	} {
		if got := devSiteAPIPath(tc.path) == nil; got != tc.api {
			t.Errorf("devSiteAPIPath(%q) ok = %v, want %v", tc.path, got, tc.api)
		}
		if got := devSitePagePath(tc.path) == nil; got != tc.page {
			t.Errorf("devSitePagePath(%q) ok = %v, want %v", tc.path, got, tc.page)
		}
	}
	h := newSiteHarness(t, func(w http.ResponseWriter, _ *http.Request) { jsonOK(w, `{}`) })
	if _, err := h.c.API(context.Background(), tool.DevSiteCall{Method: http.MethodGet, Path: "v1/bundleIds;x"}); err == nil {
		t.Fatal("API accepted a ; segment")
	}
	if _, err := h.c.Page(context.Background(), "account/..;/services-account/x", nil); err == nil {
		t.Fatal("Page accepted a ; segment")
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("%d requests sent", n)
	}
}

func TestDevSiteSubmitRefererCarriesQuery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query url.Values
		want  string
	}{
		{"with query", url.Values{"topic": {"1"}}, "/contact/request?topic=1"},
		{"without query", nil, "/contact/request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSiteHarness(t, func(w http.ResponseWriter, _ *http.Request) { jsonOK(w, `{}`) })
			h.c.SetAllowWrites(true)
			_, err := h.c.Submit(devWriteCtx(t), tool.DevSitePost{
				Path: "contact/request/submit", Referer: "contact/request", RefererQuery: tc.query,
				ContentType: "application/x-www-form-urlencoded", Body: []byte("a=1"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := h.requests(); len(got) != 1 || got[0].referer != h.srv.URL+tc.want {
				t.Fatalf("requests = %+v", got)
			}
		})
	}
}

func TestDevSitePageRedirectStaysInsideRoots(t *testing.T) {
	for _, tc := range []struct {
		target string
		follow bool
	}{
		{"/services-account/x", false},
		{"/forums/x", false},
		{"/account/..;/services-account/x", false},
		{"/account/b", true},
		{"/contact/b/", true},
	} {
		t.Run(tc.target, func(t *testing.T) {
			h := newSiteHarness(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/account/a" {
					http.Redirect(w, r, tc.target, http.StatusFound)
					return
				}
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, "<html>landed</html>")
			})
			ans, err := h.c.Page(context.Background(), "account/a", nil)
			got := h.requests()
			if tc.follow {
				if err != nil || string(ans.Body) != "<html>landed</html>" || len(got) != 2 {
					t.Fatalf("answer = %+v, err = %v, requests = %+v", ans, err, got)
				}
				return
			}
			if err == nil || errors.Is(err, tool.ErrWebSessionRequired) || !strings.Contains(err.Error(), "redirect to "+tc.target+" refused") {
				t.Fatalf("err = %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("redirect followed: %+v", got)
			}
		})
	}
}

func TestDevSitePage403KeepsSession(t *testing.T) {
	h := newSiteHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<html><a href="https://idmsa.apple.com/signin">Sign in</a> session has expired</html>`)
	})
	_, err := h.c.Page(context.Background(), "account/resources", nil)
	var pe *tool.PortalError
	if !errors.As(err, &pe) || pe.Status != http.StatusForbidden || errors.Is(err, tool.ErrWebSessionRequired) {
		t.Fatalf("err = %v", err)
	}
	if h.src.expired != 0 {
		t.Fatalf("expired = %d", h.src.expired)
	}
	if n := len(h.requests()); n != 1 {
		t.Fatalf("%d requests", n)
	}
}

func TestDevSiteWriteNonJSONAnswerIsNotResent(t *testing.T) {
	for _, tc := range []struct {
		name string
		call tool.DevSiteCall
	}{
		{"legacy form", tool.DevSiteCall{Method: http.MethodPost, Path: "QH65B2/account/auth/key/create", Form: url.Values{"a": {"1"}}}},
		{"v1 POST", tool.DevSiteCall{Method: http.MethodPost, Path: "v1/bundleIds", JSON: map[string]any{"data": map[string]any{}}}},
		{"legacy download", tool.DevSiteCall{Method: http.MethodGet, Path: "QH65B2/account/auth/key/download"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sends atomic.Int32
			h := newSiteHarness(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/services-account/"+tc.call.Path {
					sends.Add(1)
				}
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, "<html>Done?</html>")
			})
			h.c.SetAllowWrites(true)
			h.holdCSRF()
			_, err := h.c.API(devWriteCtx(t), tc.call)
			if !errors.Is(err, tool.ErrOutcomeUnknown) || !strings.Contains(err.Error(), "not repeated") {
				t.Fatalf("err = %v", err)
			}
			if sends.Load() != 1 || h.src.expired != 0 {
				t.Fatalf("sent %d times, expired = %d", sends.Load(), h.src.expired)
			}
		})
	}
}

func TestDevSiteWrite401IsRetriedAfterRenewal(t *testing.T) {
	var sends atomic.Int32
	h := newSiteHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/services-account/QH65B2/account/auth/key/create" {
			return
		}
		if sends.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		jsonOK(w, `{"resultCode":0}`)
	})
	h.c.SetAllowWrites(true)
	h.holdCSRF()
	ans, err := h.c.API(devWriteCtx(t), tool.DevSiteCall{Method: http.MethodPost, Path: "QH65B2/account/auth/key/create", Form: url.Values{}})
	if err != nil || ans.Status != http.StatusOK || sends.Load() != 2 {
		t.Fatalf("answer = %+v, err = %v, sends = %d", ans, err, sends.Load())
	}
}
