package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/websession"
)

// fakeBrowser replaces the browser sign-in with one that returns a session
// the fake olympus accepts; it records the options of every call.
func fakeBrowser(t *testing.T, display bool) *[]websession.BrowserLoginOptions {
	t.Helper()
	oldLogin, oldDisplay := webBrowserLogin, webHasDisplay
	t.Cleanup(func() { webBrowserLogin, webHasDisplay = oldLogin, oldDisplay })
	var calls []websession.BrowserLoginOptions
	webHasDisplay = func() bool { return display }
	webBrowserLogin = func(_ context.Context, o websession.BrowserLoginOptions) (*websession.Session, error) {
		calls = append(calls, o)
		return &websession.Session{
			Source:  "browser",
			Cookies: []websession.Cookie{{Name: "myacinfo", Value: "GOODSECRET", Domain: ".apple.com", Path: "/"}},
		}, nil
	}
	return &calls
}

// noSRP fails the test if the password sign-in runs.
func noSRP(t *testing.T) {
	t.Helper()
	old := webLogin
	t.Cleanup(func() { webLogin = old })
	webLogin = func(context.Context, websession.LoginOptions) (*websession.Session, error) {
		t.Error("password sign-in ran")
		return nil, errors.New("unexpected")
	}
}

func TestWebLoginDefaultsToBrowser(t *testing.T) {
	webSandbox(t)
	noSRP(t)
	calls := fakeBrowser(t, true)
	out, errOut, err := runCLI(t, "", "web", "login", "--timeout", "3m", "--browser", "/opt/brave", "--team", "200")
	assertNoSecrets(t, out, errOut)
	if err != nil {
		t.Fatalf("login: %v\n%s", err, errOut)
	}
	if len(*calls) != 1 {
		t.Fatalf("browser login ran %d times", len(*calls))
	}
	dir, err := webStateDir()
	if err != nil {
		t.Fatal(err)
	}
	o := (*calls)[0]
	if o.ProfileDir != websession.BrowserProfileDir(dir, "default") || o.BrowserPath != "/opt/brave" || o.Timeout != 3*time.Minute || o.Headless || o.Log == nil {
		t.Errorf("options = %+v", o)
	}
	if !strings.Contains(out, "Signed in as dev@example.com (team Side Team, id 200)") {
		t.Errorf("output: %q", out)
	}
	out, _, err = runCLI(t, "", "web", "status")
	if err != nil || !strings.Contains(out, "source browser") || !strings.Contains(out, "Side Team") {
		t.Fatalf("status: %v %q", err, out)
	}
}

func TestWebLoginDefaultTimeout(t *testing.T) {
	webSandbox(t)
	calls := fakeBrowser(t, true)
	if _, errOut, err := runCLI(t, "", "web", "login"); err != nil {
		t.Fatalf("login: %v\n%s", err, errOut)
	}
	if len(*calls) != 1 || (*calls)[0].Timeout != 10*time.Minute || (*calls)[0].BrowserPath != "" {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestWebLoginNoDisplay(t *testing.T) {
	webSandbox(t)
	calls := fakeBrowser(t, false)
	_, _, err := runCLI(t, "", "web", "login")
	if err == nil || !strings.Contains(err.Error(), "--password") || !strings.Contains(err.Error(), "web import") {
		t.Fatalf("got %v", err)
	}
	if len(*calls) != 0 {
		t.Fatal("browser launched without a display")
	}
}

func TestWebLoginPasswordFlagsNeedPassword(t *testing.T) {
	webSandbox(t)
	noSRP(t)
	calls := fakeBrowser(t, true)
	for _, args := range [][]string{
		{"web", "login", "--password-stdin"},
		{"web", "login", "--apple-id", "a@b.c"},
	} {
		_, _, err := runCLI(t, "PW\n", args...)
		var ue *usageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), "--password") {
			t.Errorf("%v: got %v", args, err)
		}
	}
	if len(*calls) != 0 {
		t.Fatal("browser launched on a usage error")
	}
}

func TestWebLoginPasswordSkipsBrowser(t *testing.T) {
	webSandbox(t)
	calls := fakeBrowser(t, true)
	old := webLogin
	t.Cleanup(func() { webLogin = old })
	srp := 0
	webLogin = func(_ context.Context, o websession.LoginOptions) (*websession.Session, error) {
		srp++
		if o.AppleID != "dev@example.com" || o.Password != "PWSECRET" {
			return nil, errors.New("wrong credentials passed")
		}
		return &websession.Session{Cookies: []websession.Cookie{{Name: "myacinfo", Value: "GOODSECRET", Domain: ".apple.com", Path: "/"}}}, nil
	}
	out, errOut, err := runCLI(t, "PWSECRET\n", "web", "login", "--password", "--apple-id", "dev@example.com", "--password-stdin")
	assertNoSecrets(t, out, errOut)
	if err != nil || srp != 1 || len(*calls) != 0 || !strings.Contains(out, "Signed in as dev@example.com") {
		t.Fatalf("err %v, srp %d, browser %d, out %q", err, srp, len(*calls), out)
	}
}

func TestWebLoginAlreadySignedInAndForce(t *testing.T) {
	posts := webSandbox(t)
	calls := fakeBrowser(t, true)
	if _, errOut, err := runCLI(t, "", "web", "login"); err != nil {
		t.Fatalf("first login: %v\n%s", err, errOut)
	}
	out, errOut, err := runCLI(t, "", "web", "login")
	assertNoSecrets(t, out, errOut)
	if err != nil || len(*calls) != 1 || !strings.Contains(out, "Already signed in as dev@example.com (team Main Team, id 100)") {
		t.Fatalf("second login: %v, browser %d, %q", err, len(*calls), out)
	}
	// A --team on a valid session switches in place without a window.
	out, _, err = runCLI(t, "", "web", "login", "--team", "200")
	if err != nil || len(*calls) != 1 || !strings.Contains(out, "Already signed in as dev@example.com (team Side Team, id 200)") || len(posts()) != 1 {
		t.Fatalf("team on valid session: %v, browser %d, posts %v, %q", err, len(*calls), posts(), out)
	}
	out, _, err = runCLI(t, "", "web", "login", "--force")
	if err != nil || len(*calls) != 2 || !strings.Contains(out, "Signed in as") {
		t.Fatalf("--force: %v, browser %d, %q", err, len(*calls), out)
	}
}

func TestWebLogoutRemovesBrowserProfile(t *testing.T) {
	webSandbox(t)
	fakeBrowser(t, true)
	if _, errOut, err := runCLI(t, "", "web", "login"); err != nil {
		t.Fatalf("login: %v\n%s", err, errOut)
	}
	dir, err := webStateDir()
	if err != nil {
		t.Fatal(err)
	}
	profileDir := websession.BrowserProfileDir(dir, "default")
	cookies := filepath.Join(profileDir, "Default", "Network", "Cookies")
	if err := os.MkdirAll(filepath.Dir(cookies), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookies, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "web", "logout")
	if err != nil || !strings.Contains(out, "browser profile") {
		t.Fatalf("logout: %v %q", err, out)
	}
	if _, err := os.Stat(profileDir); !os.IsNotExist(err) {
		t.Fatalf("browser profile still there: %v", err)
	}
	// Logging out again (nothing left) is fine.
	if _, _, err := runCLI(t, "", "web", "logout"); err != nil {
		t.Fatalf("second logout: %v", err)
	}
}
