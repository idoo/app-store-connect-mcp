package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/websession"
)

// webLoginHooks stubs the browser, the display probe and Apple's session
// check (sessions with myacinfo=GOOD are valid). It returns the browser calls.
func webLoginHooks(t *testing.T, display bool) *[]websession.BrowserLoginOptions {
	t.Helper()
	oldLogin, oldDisplay, oldValidate := webBrowserLogin, webHasDisplay, webValidate
	t.Cleanup(func() { webBrowserLogin, webHasDisplay, webValidate = oldLogin, oldDisplay, oldValidate })
	t.Setenv(websession.EnvStore, "file")
	var calls []websession.BrowserLoginOptions
	webHasDisplay = func() bool { return display }
	webValidate = func(_ context.Context, _ *http.Client, s *websession.Session, _ time.Time) (*websession.Info, error) {
		if s.CookieHeader("appstoreconnect.apple.com", "/", time.Now()) != "myacinfo=GOOD" {
			return nil, websession.ErrExpired
		}
		return &websession.Info{AppleID: "dev@example.com", Provider: websession.Provider{ID: 42, Name: "Jvcr"}}, nil
	}
	webBrowserLogin = func(_ context.Context, o websession.BrowserLoginOptions) (*websession.Session, error) {
		calls = append(calls, o)
		_, _ = io.WriteString(o.Log, "opening Brave Browser\nwaiting for sign-in\n")
		return &websession.Session{
			AppleID: "dev@example.com", ProviderID: 42, ProviderName: "Jvcr", Source: "browser",
			Cookies: []websession.Cookie{{Name: "myacinfo", Value: "GOOD", Domain: ".apple.com", Path: "/"}},
		}, nil
	}
	return &calls
}

func TestWebLoginReusesValidSession(t *testing.T) {
	calls := webLoginHooks(t, true)
	dir := t.TempDir()
	store := websession.DefaultStore(dir)
	if err := store.Save("p", &websession.Session{Cookies: []websession.Cookie{{Name: "myacinfo", Value: "GOOD", Domain: ".apple.com"}}}); err != nil {
		t.Fatal(err)
	}
	acct, err := newWebLogin(dir, "p")(context.Background(), func(string) { t.Error("logged on reuse") })
	if err != nil {
		t.Fatal(err)
	}
	if acct != (tool.WebAccount{AppleID: "dev@example.com", TeamID: 42, TeamName: "Jvcr", Reused: true}) || len(*calls) != 0 {
		t.Fatalf("acct %+v, browser calls %d", acct, len(*calls))
	}
}

func TestWebLoginOpensBrowserAndSaves(t *testing.T) {
	calls := webLoginHooks(t, true)
	dir := t.TempDir()
	store := websession.DefaultStore(dir)
	// An expired session is replaced.
	if err := store.Save("p", &websession.Session{Cookies: []websession.Cookie{{Name: "myacinfo", Value: "OLD", Domain: ".apple.com"}}}); err != nil {
		t.Fatal(err)
	}
	var lines []string
	acct, err := newWebLogin(dir, "p")(context.Background(), func(s string) { lines = append(lines, s) })
	if err != nil {
		t.Fatal(err)
	}
	if acct != (tool.WebAccount{AppleID: "dev@example.com", TeamID: 42, TeamName: "Jvcr"}) {
		t.Fatalf("acct %+v", acct)
	}
	if len(*calls) != 1 || (*calls)[0].Headless || (*calls)[0].ProfileDir != websession.BrowserProfileDir(dir, "p") {
		t.Fatalf("calls %+v", *calls)
	}
	if strings.Join(lines, "|") != "opening Brave Browser|waiting for sign-in" {
		t.Errorf("log lines %q", lines)
	}
	got, err := store.Load("p")
	if err != nil || got.Source != "browser" || got.CreatedAt.IsZero() {
		t.Fatalf("stored %v %v", got, err)
	}
}

func TestWebLoginNoDisplay(t *testing.T) {
	calls := webLoginHooks(t, false)
	_, err := newWebLogin(t.TempDir(), "p")(context.Background(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "app-store-connect-mcp web login") || len(*calls) != 0 {
		t.Fatalf("err %v, calls %d", err, len(*calls))
	}
}

func TestWebSourceRenewsOnlyWithBrowserProfile(t *testing.T) {
	calls := webLoginHooks(t, true)
	dir := t.TempDir()
	// No browser profile yet: renewal is refused without launching anything.
	err := newWebSource("p", dir).Authorize(context.Background(), mustReq(t))
	if !errors.Is(err, tool.ErrWebSessionRequired) || len(*calls) != 0 {
		t.Fatalf("no profile: err %v, calls %d", err, len(*calls))
	}
	// With a profile from an earlier sign-in, it renews headless.
	cookies := filepath.Join(websession.BrowserProfileDir(dir, "p"), "Default", "Network", "Cookies")
	if err := os.MkdirAll(filepath.Dir(cookies), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookies, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := mustReq(t)
	if err := newWebSource("p", dir).Authorize(context.Background(), req); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if len(*calls) != 1 || !(*calls)[0].Headless || (*calls)[0].Timeout != webRenewTimeout || !strings.Contains(req.Header.Get("Cookie"), "myacinfo=GOOD") {
		t.Fatalf("calls %+v, cookie set %v", *calls, req.Header.Get("Cookie") != "")
	}
}

func mustReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, websession.IrisBaseURL+"/v1/apps", http.NoBody)
	if err != nil {
		t.Fatal(fmt.Errorf("request: %w", err))
	}
	return req
}

func TestWebLoginOnlyOnStdio(t *testing.T) {
	webLoginHooks(t, true)
	r := testResolved(t, nil, tool.KeyTeam)
	for transport, want := range map[string]bool{tool.TransportStdio: true, tool.TransportHTTP: false, "": false} {
		env, err := newEnv(context.Background(), r, "test", "", transport)
		if err != nil {
			t.Fatal(err)
		}
		_ = env.Files.(*ProjectFiles).Close()
		if (env.WebLogin != nil) != want {
			t.Errorf("transport %q: WebLogin set = %v", transport, env.WebLogin != nil)
		}
	}
}
