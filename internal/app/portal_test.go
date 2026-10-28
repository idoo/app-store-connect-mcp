package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// fakeSession adds a secret cookie and counts renewals.
type fakeSession struct {
	mu       sync.Mutex
	expired  int
	observed int
	cookie   string
}

func (f *fakeSession) Authorize(_ context.Context, req *http.Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	req.Header.Set("Cookie", "myacinfo="+f.cookie)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	return nil
}

func (f *fakeSession) Observe(*http.Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed++
}

func (f *fakeSession) Expired() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expired++
	f.cookie = "RENEWED"
}

// portalHarness serves h and returns a portal client over a fake session
// whose sleeps are recorded, not waited.
func portalHarness(t *testing.T, h http.HandlerFunc) (*portalClient, *fakeSession, *[]time.Duration, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	src := &fakeSession{cookie: "SECRETCOOKIE"}
	c := newPortalClient(src, srv.URL, "9.9")
	var sleeps []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return ctx.Err()
	}
	return c, src, &sleeps, &hits
}

func TestPortalRefusesOtherPathsAndMethods(t *testing.T) {
	c, _, _, hits := portalHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/iris/v1/apps"},
		{http.MethodGet, "/olympus/v1/session/extra"},
		{http.MethodGet, "/olympus/v1/providerSwitchRequests"},
		{http.MethodGet, "/analytics/api/v1/../../iris/v1/apps"},
		{http.MethodGet, "/analytics/api/v1/%2e%2e/x"},
		{http.MethodGet, "//evil.example/analytics/api/v1/x"},
		{http.MethodGet, "/analytics/api/v1/"},
		{http.MethodDelete, "/analytics/api/v1/settings/all"},
		{http.MethodPatch, "/olympus/v1/session"},
	} {
		if err := c.JSON(context.Background(), tc.method, tc.path, nil, nil); err == nil {
			t.Errorf("%s %s: no error", tc.method, tc.path)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("refused requests reached the server %d times", hits.Load())
	}
	for _, p := range []string{"/olympus/v1/session", "/olympus/v1/session?x=1", "/analytics/api/v1/settings/all", "/WebObjects/iTunesConnect.woa/ra/paymentConsolidation/providers/1/sapVendorNumbers"} {
		if err := c.JSON(context.Background(), http.MethodGet, p, nil, nil); err != nil {
			t.Errorf("GET %s: %v", p, err)
		}
	}
}

func TestPortalSendsHeadersAndDecodes(t *testing.T) {
	var got http.Header
	var gotBody, gotQuery string
	c, src, _, _ := portalHarness(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		gotBody, gotQuery = string(b), r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"size":3}`)
	})
	var out struct{ Size int }
	if err := c.JSON(context.Background(), http.MethodPost, "/analytics/api/v1/data/app/detail/measures?a=b", map[string]any{"adamId": []string{"1"}}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Size != 3 || gotBody != `{"adamId":["1"]}` || gotQuery != "a=b" {
		t.Errorf("out %+v body %s query %s", out, gotBody, gotQuery)
	}
	for k, v := range map[string]string{
		"Cookie":         "myacinfo=SECRETCOOKIE",
		"X-Requested-By": "appstoreconnect.apple.com",
		"Accept":         "application/json",
		"Content-Type":   "application/json",
		"User-Agent":     "app-store-connect-mcp/9.9",
	} {
		if got.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, got.Get(k), v)
		}
	}
	if src.observed != 1 {
		t.Errorf("observed %d responses, want 1", src.observed)
	}
	// GET sends no Content-Type.
	if err := c.JSON(context.Background(), http.MethodGet, "/olympus/v1/session", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got.Get("Content-Type") != "" {
		t.Errorf("GET Content-Type = %q", got.Get("Content-Type"))
	}
}

func TestPortalRenewsOnceOn401(t *testing.T) {
	c, src, _, hits := portalHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "myacinfo=RENEWED" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	var out struct{ OK bool }
	if err := c.JSON(context.Background(), http.MethodGet, "/olympus/v1/session", nil, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || src.expired != 1 || hits.Load() != 2 {
		t.Fatalf("ok %v expired %d hits %d", out.OK, src.expired, hits.Load())
	}
}

func TestPortalSecond401NeedsSignIn(t *testing.T) {
	c, src, _, hits := portalHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	err := c.JSON(context.Background(), http.MethodGet, "/olympus/v1/session", nil, nil)
	if !errors.Is(err, tool.ErrWebSessionRequired) || src.expired != 1 || hits.Load() != 2 {
		t.Fatalf("err %v expired %d hits %d", err, src.expired, hits.Load())
	}
}

func TestPortalRedirectNeedsSignIn(t *testing.T) {
	c, _, _, hits := portalHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		http.Redirect(w, r, "/login?cookie=SECRETCOOKIE", http.StatusFound)
	})
	err := c.JSON(context.Background(), http.MethodGet, "/analytics/api/v1/settings/all", nil, nil)
	if !errors.Is(err, tool.ErrWebSessionRequired) || hits.Load() != 1 {
		t.Fatalf("err %v hits %d", err, hits.Load())
	}
	if strings.Contains(err.Error(), "SECRETCOOKIE") {
		t.Fatalf("error leaks the cookie: %v", err)
	}
}

func TestPortalHTMLAnswerNeedsSignIn(t *testing.T) {
	c, _, _, _ := portalHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><body>Sign In</body></html>`)
	})
	var out map[string]any
	err := c.JSON(context.Background(), http.MethodGet, "/analytics/api/v1/settings/all", nil, &out)
	if !errors.Is(err, tool.ErrWebSessionRequired) {
		t.Fatalf("err %v", err)
	}
}

func TestPortalErrorCarriesAppleMessages(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want []string
	}{
		"errors": {`{"message":"Make sure the request is permitted.","errors":[{"message":"Invalid adam id: 1000000099"}]}`, []string{"Invalid adam id: 1000000099"}},
		"top":    {`{"message":"Make sure the request is permitted."}`, []string{"Make sure the request is permitted."}},
		"html":   {`<html>denied</html>`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			c, _, _, hits := portalHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, tc.body)
			})
			err := c.JSON(context.Background(), http.MethodPost, "/analytics/api/v1/data/timeseries", map[string]int{"a": 1}, nil)
			var pe *tool.PortalError
			if !errors.As(err, &pe) || pe.Status != http.StatusForbidden || strings.Join(pe.Messages, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("err %v (%#v)", err, pe)
			}
			if errors.Is(err, tool.ErrWebSessionRequired) || hits.Load() != 1 {
				t.Fatalf("403 treated as session failure or retried: %v hits %d", err, hits.Load())
			}
		})
	}
}

func TestPortalRetriesUnavailable(t *testing.T) {
	var n atomic.Int32
	c, _, sleeps, hits := portalHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		switch n.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"v":1}`)
		}
	})
	var out struct{ V int }
	if err := c.JSON(context.Background(), http.MethodPost, "/analytics/api/v1/data/timeseries", map[string]int{}, &out); err != nil {
		t.Fatal(err)
	}
	if out.V != 1 || hits.Load() != 3 {
		t.Fatalf("out %+v hits %d", out, hits.Load())
	}
	if len(*sleeps) != 2 || (*sleeps)[0] != portalBackoff || (*sleeps)[1] != 3*time.Second {
		t.Fatalf("sleeps %v", *sleeps)
	}
}

func TestPortalRetryBudgetEnds(t *testing.T) {
	c, _, _, hits := portalHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	err := c.JSON(context.Background(), http.MethodGet, "/analytics/api/v1/settings/all", nil, nil)
	var pe *tool.PortalError
	if !errors.As(err, &pe) || pe.Status != http.StatusBadGateway || hits.Load() != 1+portalMaxRetries {
		t.Fatalf("err %v hits %d", err, hits.Load())
	}
}

func TestPortalHonoursCancel(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	c, _, _, _ := portalHarness(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := c.JSON(ctx, http.MethodGet, "/analytics/api/v1/settings/all", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	// A cancelled ctx also ends a retry wait.
	c2, _, _, _ := portalHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c2.sleep = sleepCtx
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if err := c2.JSON(ctx2, http.MethodGet, "/analytics/api/v1/settings/all", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retry wait err %v", err)
	}
}

func TestPortalBoundsConcurrency(t *testing.T) {
	var cur, peak atomic.Int32
	c, _, _, _ := portalHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		_, _ = io.WriteString(w, `{}`)
	})
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.JSON(context.Background(), http.MethodGet, "/analytics/api/v1/settings/all", nil, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak.Load() > portalMaxConcurrent {
		t.Fatalf("peak in-flight %d > %d", peak.Load(), portalMaxConcurrent)
	}
}

func TestEnvHasPortalWithStateDir(t *testing.T) {
	r := testResolved(t, nil, tool.KeyTeam)
	env, err := newEnv(context.Background(), r, "test", "", tool.TransportStdio)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Files.(*ProjectFiles).Close() })
	if env.Portal == nil || env.Web == nil {
		t.Fatalf("Portal %v Web %v", env.Portal, env.Web)
	}
}
