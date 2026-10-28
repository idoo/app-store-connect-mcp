package asc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Secret cookie values used by the fake session; they must never surface.
const (
	testMyacinfo = "DAWTKNV323952cf8084a204fb20ab2508441a07d02d3"
	testDqsid    = "dqsidSECRETvalue123"
)

// fakeSession is a SessionAuth counting its calls.
type fakeSession struct {
	authorized atomic.Int32
	observed   atomic.Int32
	expired    atomic.Int32
	fail       error
	// failAfterExpired makes Authorize fail once Expired was called.
	failAfterExpired error
}

func (f *fakeSession) Authorize(_ context.Context, req *http.Request) error {
	if f.fail != nil {
		return f.fail
	}
	if f.failAfterExpired != nil && f.expired.Load() > 0 {
		return f.failAfterExpired
	}
	f.authorized.Add(1)
	req.Header.Set("Cookie", "myacinfo="+testMyacinfo+"; dqsid="+testDqsid)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	return nil
}

func (f *fakeSession) Observe(*http.Response) { f.observed.Add(1) }

func (f *fakeSession) Expired() { f.expired.Add(1) }

// newSessionHarness is a harness whose client is in web-session mode with
// base <server>/iris. The server fails the test on any Authorization header
// or a missing Cookie header.
func newSessionHarness(t *testing.T, h http.HandlerFunc, mutate func(*Options)) (*harness, *fakeSession) {
	t.Helper()
	sess := &fakeSession{}
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization header sent in session mode")
		}
		if r.Header.Get("Cookie") == "" {
			t.Errorf("no Cookie header")
		}
		h(w, r)
	}, func(o *Options) {
		o.BaseURL = o.BaseURL.JoinPath("iris")
		o.Tokens = nil
		o.Session = sess
		o.Spec = nil
		if mutate != nil {
			mutate(o)
		}
	})
	return hs, sess
}

func TestNewRequiresExactlyOneAuth(t *testing.T) {
	if _, err := New(Options{Tokens: &fakeTokens{}, Session: &fakeSession{}}); err == nil {
		t.Error("both Tokens and Session accepted")
	}
	if _, err := New(Options{}); err == nil {
		t.Error("neither Tokens nor Session accepted")
	}
	u, _ := url.Parse("https://appstoreconnect.apple.com/iris")
	if _, err := New(Options{BaseURL: u, Session: &fakeSession{}}); err != nil {
		t.Errorf("session client: %v", err)
	}
	for _, raw := range []string{"https://appstoreconnect.apple.com/iris?x=1", "https://appstoreconnect.apple.com/iris#f"} {
		u, _ := url.Parse(raw)
		if _, err := New(Options{BaseURL: u, Session: &fakeSession{}}); err == nil {
			t.Errorf("New(%q) accepted", raw)
		}
	}
}

func TestResolveJoinsBasePathPrefix(t *testing.T) {
	for _, base := range []string{"https://appstoreconnect.apple.com/iris", "https://appstoreconnect.apple.com/iris/"} {
		u, _ := url.Parse(base)
		c, err := New(Options{BaseURL: u, Session: &fakeSession{}})
		if err != nil {
			t.Fatal(err)
		}
		tests := map[string]string{
			"/v1/apps":                 "https://appstoreconnect.apple.com/iris/v1/apps",
			"/v2/apps/1?include=x":     "https://appstoreconnect.apple.com/iris/v2/apps/1?include=x",
			"/v1/../../olympus/v1/x":   "https://appstoreconnect.apple.com/iris/olympus/v1/x",
			"/v1/apps/a%2Fb/dataUsage": "https://appstoreconnect.apple.com/iris/v1/apps/a%2Fb/dataUsage",
		}
		for in, want := range tests {
			got, err := c.resolve(in, nil)
			if err != nil || got.String() != want {
				t.Errorf("base %s: resolve(%q) = %v, %v; want %s", base, in, got, err, want)
			}
		}
	}
	// No prefix: unchanged behaviour.
	c, err := New(Options{Tokens: &fakeTokens{}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.resolve("/v1/apps", url.Values{"limit": {"1"}}); got.String() != DefaultBaseURL+"/v1/apps?limit=1" {
		t.Errorf("resolve = %s", got)
	}
}

func TestSessionRequestHeaders(t *testing.T) {
	var gotPath, accept, ctype, xrw string
	hs, sess := newSessionHarness(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, accept, ctype, xrw = r.URL.Path, r.Header.Get("Accept"), r.Header.Get("Content-Type"), r.Header.Get("X-Requested-With")
		w.Header().Set("Set-Cookie", "myacinfo="+testMyacinfo+"; Path=/")
		fmt.Fprint(w, `{"data":{"type":"apps","id":"1"}}`)
	}, nil)
	resp, err := hs.c.Do(writeCtx(t), tool.Request{Method: http.MethodPatch, Path: "/v1/apps/1", Write: true, Body: map[string]any{"data": map[string]any{"type": "apps", "id": "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/iris/v1/apps/1" || accept != "application/json" || ctype != "application/json" || xrw != "XMLHttpRequest" {
		t.Errorf("path %q accept %q ctype %q xrw %q", gotPath, accept, ctype, xrw)
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Error("Set-Cookie leaked into the response")
	}
	if sess.authorized.Load() != 1 || sess.observed.Load() != 1 || sess.expired.Load() != 0 {
		t.Errorf("authorized %d observed %d expired %d", sess.authorized.Load(), sess.observed.Load(), sess.expired.Load())
	}
	if rl := hs.c.RateLimit(); rl != (tool.RateLimit{}) {
		t.Errorf("rate limit = %+v, want zero", rl)
	}
}

func TestSessionAuthorizePerRetry(t *testing.T) {
	var n atomic.Int32
	hs, sess := newSessionHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"data":[]}`)
	}, nil)
	if _, err := hs.c.Get(context.Background(), "/v1/apps", nil); err != nil {
		t.Fatal(err)
	}
	if sess.authorized.Load() != 3 || sess.observed.Load() != 3 {
		t.Errorf("authorized %d observed %d, want 3/3", sess.authorized.Load(), sess.observed.Load())
	}
}

func TestSession401ExpiresAndRetriesOnce(t *testing.T) {
	var n atomic.Int32
	hs, sess := newSessionHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":[]}`)
	}, nil)
	if _, err := hs.c.Get(context.Background(), "/v1/apps", nil); err != nil {
		t.Fatal(err)
	}
	if sess.expired.Load() != 1 || sess.authorized.Load() != 2 || hs.hits.Load() != 2 {
		t.Errorf("expired %d authorized %d hits %d", sess.expired.Load(), sess.authorized.Load(), hs.hits.Load())
	}
}

func TestSessionRejectedTwiceNeedsLogin(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"401": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"errors":[{"status":"401","code":"NOT_AUTHORIZED","title":"x"}]}`)
		},
		"403 html": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `<html>Sign in myacinfo=`+testMyacinfo+`</html>`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			hs, sess := newSessionHarness(t, h, nil)
			_, err := hs.c.Do(writeCtx(t), tool.Request{Method: http.MethodPost, Path: "/v1/apps", Write: true, Body: map[string]any{}})
			if !errors.Is(err, tool.ErrWebSessionRequired) || !strings.Contains(err.Error(), "App Store Connect web session expired") {
				t.Fatalf("err = %v", err)
			}
			if errors.Is(err, tool.ErrOutcomeUnknown) || strings.Contains(err.Error(), testMyacinfo) {
				t.Errorf("err = %v", err)
			}
			if hs.hits.Load() != 2 || sess.expired.Load() < 1 {
				t.Errorf("hits %d expired %d", hs.hits.Load(), sess.expired.Load())
			}
		})
	}
}

func TestSession403JSONIsPermissionError(t *testing.T) {
	hs, sess := newSessionHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"errors":[{"status":"403","code":"FORBIDDEN_ERROR","title":"no"}]}`)
	}, nil)
	_, err := hs.c.Get(context.Background(), "/v1/apps", nil)
	var ae *tool.APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusForbidden || ae.Hint != hint403Web || errors.Is(err, tool.ErrWebSessionRequired) {
		t.Fatalf("err = %v", err)
	}
	if sess.expired.Load() != 0 || hs.hits.Load() != 1 {
		t.Errorf("expired %d hits %d", sess.expired.Load(), hs.hits.Load())
	}
}

func TestSessionAuthorizeErrorSendsNothing(t *testing.T) {
	want := fmt.Errorf("websession: no session: %w", tool.ErrWebSessionRequired)
	hs, _ := newSessionHarness(t, func(http.ResponseWriter, *http.Request) {}, func(o *Options) {
		o.Session.(*fakeSession).fail = want
	})
	_, err := hs.c.Get(context.Background(), "/v1/apps", nil)
	if !errors.Is(err, tool.ErrWebSessionRequired) || !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	if hs.hits.Load() != 0 {
		t.Fatalf("requests = %d", hs.hits.Load())
	}
}

func TestSessionReauthorizeFailureAfter401(t *testing.T) {
	want := fmt.Errorf("websession: expired: %w", tool.ErrWebSessionRequired)
	hs, _ := newSessionHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}, func(o *Options) {
		o.Session.(*fakeSession).failAfterExpired = want
	})
	_, err := hs.c.Get(context.Background(), "/v1/apps", nil)
	if !errors.Is(err, want) || hs.hits.Load() != 1 {
		t.Fatalf("err = %v, hits = %d", err, hs.hits.Load())
	}
}

func TestSessionReadOnlyGuard(t *testing.T) {
	hs, sess := newSessionHarness(t, func(http.ResponseWriter, *http.Request) {}, func(o *Options) {
		o.AllowWrites = false
	})
	_, err := hs.c.Do(writeCtx(t), tool.Request{Method: http.MethodPatch, Path: "/v1/apps/1", Write: true, Body: map[string]any{}})
	if !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("err = %v", err)
	}
	hs.c.SetAllowWrites(true)
	if _, err := hs.c.Do(context.Background(), tool.Request{Method: http.MethodPatch, Path: "/v1/apps/1", Write: true}); !errors.Is(err, tool.ErrWritesDisabled) {
		t.Fatalf("no write context: err = %v", err)
	}
	if hs.hits.Load() != 0 || sess.authorized.Load() != 0 {
		t.Fatalf("hits %d authorized %d", hs.hits.Load(), sess.authorized.Load())
	}
}

func TestSessionMutationNotReplayedOn5xx(t *testing.T) {
	hs, _ := newSessionHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}, nil)
	_, err := hs.c.Do(writeCtx(t), tool.Request{Method: http.MethodPost, Path: "/v1/apps", Write: true, Body: map[string]any{}})
	if !errors.Is(err, tool.ErrOutcomeUnknown) || hs.hits.Load() != 1 {
		t.Fatalf("err = %v, hits = %d", err, hs.hits.Load())
	}
}

func TestSessionListFollowsAbsoluteIrisLinks(t *testing.T) {
	var srv string
	hs, _ := newSessionHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/iris/v1/apps" {
			t.Errorf("path %q", r.URL.Path)
		}
		if r.URL.Query().Get("cursor") == "" {
			fmt.Fprintf(w, `{"data":[{"type":"apps","id":"1"}],"links":{"next":%q}}`, srv+"/iris/v1/apps?cursor=Mg&limit=1")
			return
		}
		fmt.Fprint(w, `{"data":[{"type":"apps","id":"2"}]}`)
	}, nil)
	srv = hs.srv.URL
	res, err := hs.c.List(context.Background(), "/v1/apps", tool.ListOptions{Limit: 1})
	if err != nil || res.Returned != 2 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestSessionListRejectsNextOutsidePrefix(t *testing.T) {
	for name, next := range map[string]func(srv string) string{
		"no prefix":    func(srv string) string { return srv + "/v1/apps?cursor=Mg" },
		"other prefix": func(srv string) string { return srv + "/olympus/v1/apps?cursor=Mg" },
		"relative":     func(string) string { return "/v1/apps?cursor=Mg" },
		"other host":   func(string) string { return "https://evil.example/iris/v1/apps?cursor=Mg" },
	} {
		t.Run(name, func(t *testing.T) {
			var srv string
			hs, _ := newSessionHarness(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"data":[{"type":"apps","id":"1"}],"links":{"next":%q}}`, next(srv))
			}, nil)
			srv = hs.srv.URL
			if _, err := hs.c.List(context.Background(), "/v1/apps", tool.ListOptions{}); err == nil {
				t.Fatal("next link outside the prefix followed")
			}
			if hs.hits.Load() != 1 {
				t.Fatalf("hits = %d", hs.hits.Load())
			}
		})
	}
}

func TestSessionCursorKeepsPrefix(t *testing.T) {
	hs, _ := newSessionHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":[{"type":"apps","id":"2"}]}`)
	}, nil)
	ctx := context.Background()
	good := EncodeCursor(hs.srv.URL + "/iris/v1/apps?cursor=Mg&limit=1")
	if res, err := hs.c.List(ctx, "/v1/apps", tool.ListOptions{Cursor: good}); err != nil || res.Returned != 1 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	for _, raw := range []string{hs.srv.URL + "/v1/apps?cursor=Mg", hs.srv.URL + "/iris/v1/builds?cursor=Mg"} {
		if _, err := hs.c.List(ctx, "/v1/apps", tool.ListOptions{Cursor: EncodeCursor(raw)}); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("cursor %s: err = %v", raw, err)
		}
	}
	base, _ := url.Parse("https://appstoreconnect.apple.com/iris")
	if _, err := DecodeCursor(EncodeCursor("https://appstoreconnect.apple.com/v1/apps"), base, "/v1/apps"); !errors.Is(err, ErrInvalidCursor) ||
		!strings.Contains(err.Error(), "outside the base path") {
		t.Errorf("err = %v", err)
	}
	if hs.hits.Load() != 1 {
		t.Errorf("hits = %d", hs.hits.Load())
	}
}

func TestSessionDownloadUsesPrefix(t *testing.T) {
	hs, sess := newSessionHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/iris/v1/x" {
			t.Errorf("path %q", r.URL.Path)
		}
		fmt.Fprint(w, "raw")
	}, nil)
	b, _, err := hs.c.Download(context.Background(), tool.Request{Path: "/v1/x"})
	if err != nil || string(b) != "raw" || sess.observed.Load() != 1 {
		t.Fatalf("b = %q, err = %v", b, err)
	}
}

func TestSessionErrorBodyRedactsCookies(t *testing.T) {
	hs, _ := newSessionHarness(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "bad request for myacinfo=%s; dqsid=%s\nSet-Cookie: DES58b0eba556d80ed2b98707e15ffafd344=abcSECRET; Path=/", testMyacinfo, testDqsid)
	}, nil)
	_, err := hs.c.Get(context.Background(), "/v1/apps", nil)
	if err == nil {
		t.Fatal("no error")
	}
	for _, s := range []string{testMyacinfo, testDqsid, "abcSECRET"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error leaks %q: %v", s, err)
		}
	}
}
