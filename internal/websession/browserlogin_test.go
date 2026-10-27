package websession

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/browser"
)

// fakeOlympus accepts GET /olympus/v1/session only with myacinfo=GOOD.
type fakeOlympus struct {
	mu     sync.Mutex
	hits   int
	status int // forced status when non-zero
}

func (f *fakeOlympus) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits++
	status := f.status
	f.mu.Unlock()
	if r.Method != http.MethodGet || r.URL.Path != "/olympus/v1/session" {
		http.Error(w, "bad", http.StatusNotFound)
		return
	}
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if !strings.Contains(r.Header.Get("Cookie"), "myacinfo=GOOD") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_, _ = fmt.Fprint(w, `{"user":{"emailAddress":"dev@example.com"},"provider":{"providerId":123,"name":"Acme AB"}}`)
}

func (f *fakeOlympus) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.hits }

func newOlympus(t *testing.T) (*fakeOlympus, *http.Client) {
	t.Helper()
	f := &fakeOlympus{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, serverClient(srv)
}

// stubBrowser replaces browserLogin with fn for the test.
func stubBrowser(t *testing.T, fn func(ctx context.Context, o browser.Options, r browser.Request) (browser.Result, error)) {
	t.Helper()
	old := browserLogin
	browserLogin = fn
	t.Cleanup(func() { browserLogin = old })
}

type pollStep struct {
	advance time.Duration
	page    string
	cookies []browser.Cookie
}

func TestBrowserLogin(t *testing.T) {
	ol, hc := newOlympus(t)
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	future := clk.now().Add(time.Hour)
	other := browser.Cookie{Name: "tracker", Value: "X", Domain: ".example.com", Path: "/"}
	expired := browser.Cookie{Name: "old", Value: "O", Domain: ".apple.com", Path: "/", Expires: clk.now().Add(-time.Hour)}
	bad := browser.Cookie{Name: "myacinfo", Value: "BAD", Domain: ".apple.com", Path: "/", Expires: future}
	good := browser.Cookie{Name: "myacinfo", Value: "GOOD", Domain: ".apple.com", Path: "/", Expires: future, HTTPOnly: true, Secure: true}
	dq := browser.Cookie{Name: "dqsid", Value: "D", Domain: "appstoreconnect.apple.com", Path: "/"}
	steps := []pollStep{
		{page: "https://appstoreconnect.apple.com/login", cookies: []browser.Cookie{other}},          // no myacinfo: no verify
		{advance: time.Second, page: "https://idmsa.apple.com/", cookies: []browser.Cookie{bad, dq}}, // verify #1 → 401
		{advance: time.Second, cookies: []browser.Cookie{bad, dq}},                                   // same print, <10 s: skip
		{advance: 5 * time.Second, cookies: []browser.Cookie{bad, dq}},                               // still <10 s: skip
		{advance: 4 * time.Second, cookies: []browser.Cookie{bad, dq}},                               // 10 s: verify #2 → 401
		{advance: time.Second, cookies: []browser.Cookie{good, dq, other, expired}},                  // changed: verify #3 → ok
	}
	var gotOpts browser.Options
	var gotReq browser.Request
	var hitsAt []int
	stubBrowser(t, func(ctx context.Context, o browser.Options, r browser.Request) (browser.Result, error) {
		gotOpts, gotReq = o, r
		for _, s := range steps {
			clk.add(s.advance)
			done, err := r.Check(ctx, s.page, s.cookies)
			hitsAt = append(hitsAt, ol.count())
			if err != nil {
				return browser.Result{}, err
			}
			if done {
				return browser.Result{Cookies: s.cookies, Browser: "Fake"}, nil
			}
		}
		return browser.Result{}, browser.ErrClosed
	})
	var log bytes.Buffer
	sess, err := BrowserLogin(context.Background(), BrowserLoginOptions{ProfileDir: t.TempDir(), HTTP: hc, Now: clk.now, Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{0, 1, 1, 1, 2, 3}; fmt.Sprint(hitsAt) != fmt.Sprint(want) {
		t.Fatalf("verify cadence %v, want %v", hitsAt, want)
	}
	if sess.Source != "browser" || sess.AppleID != "dev@example.com" || sess.ProviderID != 123 ||
		sess.ProviderName != "Acme AB" || !sess.CreatedAt.Equal(clk.now()) {
		t.Fatalf("session %+v", sess)
	}
	names := map[string]bool{}
	for _, c := range sess.Cookies {
		names[c.Name] = true
	}
	if len(sess.Cookies) != 2 || !names["myacinfo"] || !names["dqsid"] {
		t.Fatalf("cookies %v", names)
	}
	if c := cookieByName(sess, "myacinfo"); !c.HTTPOnly || !c.Secure || !c.Expires.Equal(future) {
		t.Fatalf("myacinfo attrs %+v", *c)
	}
	if gotReq.StartURL != "https://appstoreconnect.apple.com/login" || gotReq.FrameScript == "" || gotReq.NeedsHuman == nil {
		t.Fatalf("request %+v", gotReq)
	}
	if gotOpts.Headless || gotOpts.ProfileDir == "" {
		t.Fatalf("options %+v", gotOpts)
	}
	if out := log.String(); strings.Contains(out, "GOOD") || strings.Contains(out, "BAD") || !strings.Contains(out, "Signed in as dev@example.com") {
		t.Fatalf("log %q", out)
	}
}

func TestBrowserLoginLogsVerifyErrorOnce(t *testing.T) {
	ol, hc := newOlympus(t)
	ol.status = http.StatusInternalServerError
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	ck := []browser.Cookie{{Name: "myacinfo", Value: "SECRET", Domain: ".apple.com", Path: "/"}}
	stubBrowser(t, func(ctx context.Context, _ browser.Options, r browser.Request) (browser.Result, error) {
		for range 3 {
			clk.add(verifyEvery)
			if done, err := r.Check(ctx, "", ck); done || err != nil {
				t.Fatalf("done=%v err=%v", done, err)
			}
		}
		return browser.Result{}, browser.ErrInteractionRequired
	})
	var log bytes.Buffer
	_, err := BrowserLogin(context.Background(), BrowserLoginOptions{ProfileDir: t.TempDir(), HTTP: hc, Now: clk.now, Log: &log, Headless: true})
	if !errors.Is(err, browser.ErrInteractionRequired) {
		t.Fatalf("err %v", err)
	}
	if n := strings.Count(log.String(), "Could not verify"); n != 1 || strings.Contains(log.String(), "SECRET") {
		t.Fatalf("log %q", log.String())
	}
	if ol.count() != 3 {
		t.Fatalf("verifies %d", ol.count())
	}
}

func TestBrowserLoginErrorsPassThrough(t *testing.T) {
	for _, want := range []error{browser.ErrClosed, browser.ErrNoBrowser, browser.ErrInteractionRequired} {
		stubBrowser(t, func(context.Context, browser.Options, browser.Request) (browser.Result, error) {
			return browser.Result{}, fmt.Errorf("browser: %w", want)
		})
		if _, err := BrowserLogin(context.Background(), BrowserLoginOptions{ProfileDir: t.TempDir()}); !errors.Is(err, want) {
			t.Fatalf("got %v, want %v", err, want)
		}
	}
	if _, err := BrowserLogin(context.Background(), BrowserLoginOptions{}); err == nil {
		t.Fatal("empty ProfileDir accepted")
	}
}

func TestNeedsHuman(t *testing.T) {
	cases := map[string]bool{
		"https://idmsa.apple.com/appleauth/auth/signin":          true,
		"https://appstoreconnect.apple.com/login":                true,
		"https://appstoreconnect.apple.com/login?targetUrl=%2Fa": true,
		"https://appstoreconnect.apple.com/apps":                 false,
		"https://appstoreconnect.apple.com/loginx":               false,
		"https://example.com/login":                              false,
		"about:blank":                                            false,
	}
	for u, want := range cases {
		if got := needsHuman(u); got != want {
			t.Errorf("needsHuman(%q) = %v", u, got)
		}
	}
}

func TestBrowserProfileDir(t *testing.T) {
	if got := BrowserProfileDir("/s", "work"); got != filepath.Join("/s", "web", "browser", "work") {
		t.Fatalf("got %q", got)
	}
	got := BrowserProfileDir("/s", "../evil")
	if filepath.Dir(got) != filepath.Join("/s", "web", "browser") || strings.Contains(got, "..") {
		t.Fatalf("unsafe name not hashed: %q", got)
	}
}

func TestRememberMeScriptMarksOnce(t *testing.T) {
	for _, s := range []string{"remember-me", rememberAttr, "el.click()", "hasAttribute"} {
		if !strings.Contains(rememberMeScript, s) {
			t.Errorf("script lacks %q", s)
		}
	}
}
