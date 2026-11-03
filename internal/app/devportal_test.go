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

// devWriteCtx obtains a write context the only way tools can: Tool.Call of
// a Write tool in a write-enabled profile.
func devWriteCtx(t *testing.T) context.Context {
	t.Helper()
	var got context.Context
	tl := tool.New(tool.Meta{
		Name: "devportal_test_write", Toolset: "core", Title: "Test",
		Description: "Test tool.", Class: tool.Write,
	}, func(ctx context.Context, _ *tool.Env, _ struct{}) (*tool.Result, error) {
		got = ctx
		return nil, nil
	})
	tl.Call(context.Background(), &tool.Env{Profile: tool.ProfileInfo{AllowWrites: true}}, json.RawMessage(`{}`))
	if got == nil || !tool.IsWrite(got) {
		t.Fatal("no write context")
	}
	return got
}

// seenReq is one request the fake developer site received.
type seenReq struct {
	method, path, override, ctype, csrf, csrfTS, body string
}

type devHarness struct {
	c    *devPortalClient
	src  *fakeSession
	mu   sync.Mutex
	seen []seenReq
}

func (h *devHarness) requests() []seenReq {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]seenReq(nil), h.seen...)
}

// newDevHarness serves fn under /services-account/ and records requests;
// sleeps are skipped.
func newDevHarness(t *testing.T, fn http.HandlerFunc) *devHarness {
	t.Helper()
	h := &devHarness{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.seen = append(h.seen, seenReq{
			method: r.Method, path: r.URL.Path, override: r.Header.Get("X-HTTP-Method-Override"),
			ctype: r.Header.Get("Content-Type"), csrf: r.Header.Get("csrf"), csrfTS: r.Header.Get("csrf_ts"), body: string(b),
		})
		h.mu.Unlock()
		fn(w, r)
	}))
	t.Cleanup(srv.Close)
	h.src = &fakeSession{cookie: "SECRETCOOKIE"}
	h.c = newDevPortalClient(h.src, srv.URL+"/services-account/", "9.9")
	h.c.p.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return h
}

func jsonOK(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

func TestDevPortalReadShaping(t *testing.T) {
	h := newDevHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		jsonOK(w, `{"data":[{"id":"B1"}]}`)
	})
	var out struct {
		Data []struct{ ID string } `json:"data"`
	}
	q := url.Values{"filter[identifier]": {"com.example.app"}}
	if err := tool.DevPortalGet(context.Background(), h.c, "v1/bundleIds", q, "TEAM1", &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 || out.Data[0].ID != "B1" {
		t.Fatalf("out = %+v", out)
	}
	r := h.requests()[0]
	if r.method != http.MethodPost || r.path != "/services-account/v1/bundleIds" || r.override != "GET" || r.ctype != "application/vnd.api+json" {
		t.Fatalf("request = %+v", r)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(r.body), &body); err != nil || body["teamId"] != "TEAM1" || body["urlEncodedQueryParams"] != q.Encode() {
		t.Fatalf("body = %s (%v)", r.body, err)
	}
}

func TestDevPortalWriteShaping(t *testing.T) {
	h := newDevHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		jsonOK(w, `{"resultCode":0,"applicationGroup":{"applicationGroup":"G1"}}`)
	})
	h.c.SetAllowWrites(true)
	ctx := devWriteCtx(t)
	if err := h.c.Do(ctx, http.MethodPatch, "v1/bundleIds/B1", map[string]any{"data": map[string]any{"id": "B1"}}, nil); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"teamId": {"TEAM1"}, "appIdId": {"B1"}, "applicationGroups": {"G1", "G2"}}
	if err := h.c.Do(ctx, http.MethodPost, devPortalLegacyPrefix+"assignApplicationGroupToAppId.action", form, nil); err != nil {
		t.Fatal(err)
	}
	got := h.requests()
	if got[0].method != http.MethodPatch || got[0].override != "" || got[0].ctype != "application/vnd.api+json" || got[0].path != "/services-account/v1/bundleIds/B1" {
		t.Fatalf("patch = %+v", got[0])
	}
	if got[1].ctype != "application/x-www-form-urlencoded" || got[1].body != form.Encode() {
		t.Fatalf("form post = %+v", got[1])
	}
}

func TestDevPortalRefusesOutsideAllowlist(t *testing.T) {
	h := newDevHarness(t, func(w http.ResponseWriter, _ *http.Request) { jsonOK(w, `{}`) })
	h.c.SetAllowWrites(true)
	ctx := devWriteCtx(t)
	read := map[string]string{"teamId": "T"}
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/bundleIds", read},
		{http.MethodGet, "v1/bundleIds?x=1", read},
		{http.MethodGet, "v1/../v1/bundleIds", read},
		{http.MethodGet, "v1/%2e%2e/x", read},
		{http.MethodGet, "v1//bundleIds", read},
		{http.MethodGet, "https://evil.example/v1/bundleIds", read},
		{http.MethodGet, "v1/bundleIds", nil},
		{http.MethodGet, devPortalLegacyPrefix + "listApplicationGroups.action", read},
		{http.MethodPatch, "v1/certificates/C1", map[string]any{}},
		{http.MethodPatch, "v1/bundleIds", map[string]any{}},
		{http.MethodPatch, "v1/bundleIds/B1/relationships/x", map[string]any{}},
		{http.MethodPost, "v1/certificates", map[string]any{}},
		{http.MethodPost, "v1/bundleIds", map[string]any{}},
		{http.MethodPost, "v1/appGroups/G1", map[string]any{}},
		{http.MethodPost, devPortalLegacyPrefix + "deleteAppId.action", url.Values{}},
		{http.MethodPost, devPortalLegacyPrefix + "addApplicationGroup.action", map[string]any{}},
		{http.MethodDelete, "v1/bundleIds/B1", nil},
		{http.MethodPut, "v1/bundleIds/B1", map[string]any{}},
	} {
		if err := h.c.Do(ctx, tc.method, tc.path, tc.body, nil); err == nil || errors.Is(err, tool.ErrWebSessionRequired) {
			t.Errorf("%s %s: err = %v, want refusal", tc.method, tc.path, err)
		}
	}
	if n := len(h.requests()); n != 0 {
		t.Fatalf("requests sent = %d, want 0", n)
	}
}

func TestDevPortalWritesNeedGuardAndWriteContext(t *testing.T) {
	h := newDevHarness(t, func(w http.ResponseWriter, _ *http.Request) { jsonOK(w, `{}`) })
	body := map[string]any{"data": map[string]any{}}
	if err := h.c.Do(devWriteCtx(t), http.MethodPost, devPortalAppGroups, body, nil); !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("guard off: err = %v", err)
	}
	h.c.SetAllowWrites(true)
	if err := h.c.Do(context.Background(), http.MethodPost, devPortalAppGroups, body, nil); !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("no write ctx: err = %v", err)
	}
	// The legacy list is a read: no write context needed.
	if err := h.c.Do(context.Background(), http.MethodPost, devPortalLegacyPrefix+"listApplicationGroups.action", url.Values{"teamId": {"T"}}, nil); err != nil {
		t.Fatalf("legacy list: %v", err)
	}
	if err := h.c.Do(devWriteCtx(t), http.MethodPost, devPortalAppGroups, body, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	if n := len(h.requests()); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
}

func TestDevPortalExpiryMapping(t *testing.T) {
	for name, answer := range map[string]http.HandlerFunc{
		"401 errorCode 1100": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"errorId":"authentication.failed","errorMessage":"Your session has expired. Please log in.","errorCode":1100}`)
		},
		"200 errorCode 1100": func(w http.ResponseWriter, _ *http.Request) {
			jsonOK(w, `{"errorCode":1100}`)
		},
		"200 text expired": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "Your session has expired. Please log in.")
		},
		"redirect to sign-in": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://idmsa.apple.com/IDMSWebAuth/signin", http.StatusFound)
		},
		"html page": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>sign in</html>")
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newDevHarness(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/account/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				answer(w, r)
			})
			err := tool.DevPortalGet(context.Background(), h.c, "v1/bundleIds", nil, "T", nil)
			if !errors.Is(err, tool.ErrWebSessionRequired) {
				t.Fatalf("err = %v, want web_session_required", err)
			}
			if strings.Contains(err.Error(), "SECRETCOOKIE") {
				t.Fatal("cookie value in error")
			}
			var api, boot int
			for _, r := range h.requests() {
				if r.path == "/account/" {
					boot++
				} else {
					api++
				}
			}
			// First try, retry after the developer-site bootstrap, retry after
			// Expired (renewal), then give up.
			if api != 3 || boot != 1 || h.src.expired != 1 {
				t.Fatalf("api = %d boot = %d expired = %d", api, boot, h.src.expired)
			}
		})
	}
}

func TestDevPortalBootstrapRecovers(t *testing.T) {
	var booted sync.Once
	var ok bool
	var mu sync.Mutex
	h := newDevHarness(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/account/":
			booted.Do(func() { ok = true })
			http.SetCookie(w, &http.Cookie{Name: "DSESSIONID", Value: "x"})
		default:
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"errorCode":1100}`)
				return
			}
			jsonOK(w, `{"data":[]}`)
		}
	})
	if err := tool.DevPortalGet(context.Background(), h.c, "v1/appGroups", nil, "T", &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if h.src.expired != 0 || h.src.observed < 3 {
		t.Fatalf("expired = %d observed = %d", h.src.expired, h.src.observed)
	}
}

func TestDevPortalEchoesCSRF(t *testing.T) {
	h := newDevHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("csrf", "TOKEN")
		w.Header().Set("csrf_ts", "123")
		jsonOK(w, `{"resultCode":0,"applicationGroupList":[]}`)
	})
	list := devPortalLegacyPrefix + "listApplicationGroups.action"
	for range 2 {
		if err := h.c.Do(context.Background(), http.MethodPost, list, url.Values{"teamId": {"T"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	got := h.requests()
	if got[0].csrf != "" || got[1].csrf != "TOKEN" || got[1].csrfTS != "123" {
		t.Fatalf("csrf headers = %+v / %+v", got[0], got[1])
	}
}

func TestDevPortalErrorsAndRetries(t *testing.T) {
	var n int
	var mu sync.Mutex
	h := newDevHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"errors":[{"detail":"busy"}]}`)
	})
	err := tool.DevPortalGet(context.Background(), h.c, "v1/bundleIds", nil, "T", nil)
	var pe *tool.PortalError
	if !errors.As(err, &pe) || pe.Status != 503 || n != 1+portalMaxRetries {
		t.Fatalf("read: err = %v, attempts = %d", err, n)
	}
	h.c.SetAllowWrites(true)
	n = 0
	err = h.c.Do(devWriteCtx(t), http.MethodPatch, "v1/bundleIds/B1", map[string]any{}, nil)
	if !errors.As(err, &pe) || n != 1 {
		t.Fatalf("write must not be replayed: err = %v, attempts = %d", err, n)
	}
}

func TestDevPortalLegacyResultCodeIsError(t *testing.T) {
	h := newDevHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		jsonOK(w, `{"resultCode":35,"userString":"An App Group with Identifier 'group.x' is not available."}`)
	})
	h.c.SetAllowWrites(true)
	err := h.c.Do(devWriteCtx(t), http.MethodPost, devPortalLegacyPrefix+"addApplicationGroup.action", url.Values{"identifier": {"group.x"}}, nil)
	var pe *tool.PortalError
	if !errors.As(err, &pe) || len(pe.Messages) != 1 || !strings.Contains(pe.Messages[0], "not available") {
		t.Fatalf("err = %v", err)
	}
}
