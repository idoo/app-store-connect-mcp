package browser

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCmdlineHasProfile(t *testing.T) {
	const prof = `C:\Users\John Smith\AppData\Local\app-store-connect-mcp\web\browser\default`
	match := []string{
		// Go's exec quotes the whole argument when the path has a space.
		`"C:\Program Files\Google\Chrome\Application\chrome.exe" "--user-data-dir=` + prof + `" --no-first-run`,
		`"C:\Program Files\Google\Chrome\Application\chrome.exe" --headless "--user-data-dir=` + prof + `"`,
		// Chromium's own children.
		`"C:\Program Files\Google\Chrome\Application\chrome.exe" --type=renderer --user-data-dir="` + prof + `" --lang=en`,
		// No space: unquoted, mid-line or last.
		`chrome.exe --user-data-dir=C:\p\default --no-first-run`,
		`chrome.exe --user-data-dir=C:\p\default`,
	}
	profs := []string{prof, prof, prof, `C:\p\default`, `C:\p\default`}
	for i, c := range match {
		if !cmdlineHasProfile(c, profs[i]) {
			t.Errorf("no match: %s", c)
		}
	}
	miss := map[string]string{
		`chrome.exe "--user-data-dir=` + prof + `2"`:    prof, // sibling profile
		`chrome.exe --user-data-dir=C:\p\default2 --x`:  `C:\p\default`,
		`chrome.exe --user-data-dir="C:\p\default\sub"`: `C:\p\default`,
		`chrome.exe --user-data-dir=C:\p\default-other`: `C:\p\default`,
	}
	for c, p := range miss {
		if cmdlineHasProfile(c, p) {
			t.Errorf("false match: %s", c)
		}
	}
}

func TestCookieKeyPerBrowser(t *testing.T) {
	const cftMac = "/s/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"
	cases := []struct {
		goos, exe string
		mock      bool
		store     string
	}{
		{"darwin", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", false, ""},
		{"darwin", "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser", false, ""},
		{"darwin", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge", false, ""},
		{"darwin", cftMac, true, "basic"},
		{"linux", "/usr/bin/google-chrome", true, "basic"},
		{"windows", `C:\Program Files\Google\Chrome\Application\chrome.exe`, true, "basic"},
	}
	for _, c := range cases {
		mock, store := cookieKey(c.goos, c.exe)
		if mock != c.mock || store != c.store {
			t.Errorf("cookieKey(%s, %s) = %v, %q; want %v, %q", c.goos, c.exe, mock, store, c.mock, c.store)
		}
	}
	if a := cookieKeyArgs("darwin", "/Applications/Google Chrome.app"); len(a) != 0 {
		t.Errorf("vendor Chrome app args = %v, want none", a)
	}
	a := cookieKeyArgs("darwin", "/s/chrome-mac-arm64/Google Chrome for Testing.app")
	if !slices.Equal(a, []string{"--use-mock-keychain", "--password-store=basic"}) {
		t.Errorf("CfT app args = %v", a)
	}
}

func TestCftSandboxBlocked(t *testing.T) {
	reader := func(v string, err error) func(string) (string, error) {
		return func(p string) (string, error) {
			if p != usernsSysctl {
				t.Errorf("read %s", p)
			}
			return v, err
		}
	}
	err := cftSandboxBlocked("linux", reader("1\n", nil))
	if !errors.Is(err, ErrNoBrowser) || !strings.Contains(err.Error(), ".deb") || !strings.Contains(err.Error(), EnvBrowser) {
		t.Fatalf("restricted userns: %v", err)
	}
	for _, r := range []func(string) (string, error){reader("0\n", nil), reader("", errors.New("no such file"))} {
		if err := cftSandboxBlocked("linux", r); err != nil {
			t.Errorf("unrestricted: %v", err)
		}
	}
	if err := cftSandboxBlocked("darwin", func(string) (string, error) {
		t.Error("sysctl read off Linux")
		return "1", nil
	}); err != nil {
		t.Errorf("darwin: %v", err)
	}
}

func TestLinuxCandidatesAndHint(t *testing.T) {
	lin := candidates("linux", "", nil)
	for _, want := range []string{"brave", "brave-browser", "brave-browser-stable", "vivaldi-stable"} {
		if !slices.Contains(lin, want) {
			t.Errorf("linux candidates miss %s: %v", want, lin)
		}
	}
	if h := installHint("linux", "arm64"); !strings.Contains(h, "not the snap") || strings.Contains(h, "Edge") {
		t.Errorf("linux/arm64 hint = %q", h)
	}
	if h := installHint("linux", "amd64"); !strings.Contains(h, "Chrome, Brave or Edge") {
		t.Errorf("linux/amd64 hint = %q", h)
	}
}

func TestRetryWhile(t *testing.T) {
	busy := errors.New("busy")
	n := 0
	err := retryWhile(func() error {
		n++
		if n < 3 {
			return busy
		}
		return nil
	}, func(e error) bool { return errors.Is(e, busy) }, time.Second, time.Millisecond)
	if err != nil || n != 3 {
		t.Fatalf("retry: err %v after %d calls", err, n)
	}
	n = 0
	other := errors.New("other")
	if err := retryWhile(func() error { n++; return other }, func(e error) bool { return errors.Is(e, busy) }, time.Second, time.Millisecond); !errors.Is(err, other) || n != 1 {
		t.Fatalf("non-retryable: err %v after %d calls", err, n)
	}
	if err := retryWhile(func() error { return busy }, func(error) bool { return true }, 0, time.Millisecond); !errors.Is(err, busy) {
		t.Fatalf("deadline: %v", err)
	}
}
