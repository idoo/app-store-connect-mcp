package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/browser"
	"github.com/idoo/app-store-connect-mcp/internal/websession"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeOlympus answers /olympus/v1/session: live (200, rolling myacinfo) for
// the cookie myacinfo=GOOD, 401 otherwise.
func fakeOlympus(t *testing.T) {
	t.Helper()
	old := keepaliveHTTP
	t.Cleanup(func() { keepaliveHTTP = old })
	keepaliveHTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}
		if r.URL.String() == websession.OlympusSessionURL && strings.Contains(r.Header.Get("Cookie"), "myacinfo=GOOD") {
			resp.StatusCode = http.StatusOK
			resp.Header.Set("Content-Type", "application/json")
			resp.Header.Add("Set-Cookie", "myacinfo=ROLLED; Domain=.apple.com; Path=/; Secure")
			resp.Body = io.NopCloser(strings.NewReader(`{"user":{"emailAddress":"dev@example.com"},"provider":{"providerId":42}}`))
		}
		return resp, nil
	})}
}

// withBrowserProfile marks profile p as signed in through a browser once,
// so renewal is attempted.
func withBrowserProfile(t *testing.T, dir string) {
	t.Helper()
	cookies := filepath.Join(websession.BrowserProfileDir(dir, "p"), "Default", "Network", "Cookies")
	if err := os.MkdirAll(filepath.Dir(cookies), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookies, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestKeepaliveSavesRolledCookies(t *testing.T) {
	webLoginHooks(t, true)
	fakeOlympus(t)
	dir := t.TempDir()
	store := websession.DefaultStore(dir)
	if err := store.Save("p", &websession.Session{Cookies: []websession.Cookie{{Name: "myacinfo", Value: "GOOD", Domain: ".apple.com", Path: "/"}}}); err != nil {
		t.Fatal(err)
	}
	apple, err := WebKeepalive(context.Background(), dir, "p")
	if err != nil || apple != "dev@example.com" {
		t.Fatalf("keepalive = %q, %v", apple, err)
	}
	got, err := store.Load("p")
	if err != nil {
		t.Fatal(err)
	}
	if h := got.CookieHeader("appstoreconnect.apple.com", "/", time.Now()); !strings.Contains(h, "myacinfo=ROLLED") {
		t.Fatalf("rolled cookie not saved: %d cookies", len(got.Cookies))
	}
}

func TestKeepaliveDeadSessionNotifiesOnce(t *testing.T) {
	calls := webLoginHooks(t, true)
	fakeOlympus(t)
	var notes []string
	webNotify = func(_, body string) { notes = append(notes, body) }
	webBrowserLogin = func(_ context.Context, o websession.BrowserLoginOptions) (*websession.Session, error) {
		*calls = append(*calls, o)
		return nil, browser.ErrInteractionRequired
	}
	dir := t.TempDir()
	withBrowserProfile(t, dir)
	store := websession.DefaultStore(dir)
	if err := store.Save("p", &websession.Session{Cookies: []websession.Cookie{{Name: "myacinfo", Value: "OLD", Domain: ".apple.com", Path: "/"}}}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, err := WebKeepalive(context.Background(), dir, "p"); !errors.Is(err, ErrKeepaliveSignIn) {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if len(*calls) != 2 || !(*calls)[0].Headless {
		t.Fatalf("renewals = %+v, want one headless per run", *calls)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "web login") {
		t.Fatalf("notifications = %q, want one", notes)
	}
	// Another dead session is news again.
	if err := store.Save("p", &websession.Session{Cookies: []websession.Cookie{{Name: "myacinfo", Value: "OLDER", Domain: ".apple.com", Path: "/"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := WebKeepalive(context.Background(), dir, "p"); !errors.Is(err, ErrKeepaliveSignIn) || len(notes) != 2 {
		t.Fatalf("new dead session: %v, %d notifications", err, len(notes))
	}
}

func TestKeepaliveSkipsWhileSignInWindowOpen(t *testing.T) {
	webLoginHooks(t, true)
	fakeOlympus(t)
	notified := false
	webNotify = func(string, string) { notified = true }
	webBrowserLogin = func(context.Context, websession.BrowserLoginOptions) (*websession.Session, error) {
		return nil, browser.ErrProfileInUse
	}
	dir := t.TempDir()
	withBrowserProfile(t, dir)
	if err := websession.DefaultStore(dir).Save("p", &websession.Session{Cookies: []websession.Cookie{{Name: "myacinfo", Value: "OLD", Domain: ".apple.com", Path: "/"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := WebKeepalive(context.Background(), dir, "p"); !errors.Is(err, ErrKeepaliveWindowOpen) || notified {
		t.Fatalf("err %v, notified %v", err, notified)
	}
}
