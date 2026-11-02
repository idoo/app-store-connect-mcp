package browser

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	const signIn = "https://idmsa.apple.com/appleauth/auth/signin?token=SECRET"
	const home = "https://appstoreconnect.apple.com/apps"
	errBoom := errors.New("boom")
	needsHuman := func(u string) bool { return strings.Contains(u, "idmsa.apple.com") }
	check := func(done bool, err error) func(context.Context, string, []Cookie) (bool, error) {
		return func(context.Context, string, []Cookie) (bool, error) { return done, err }
	}
	cases := []struct {
		name     string
		headless bool
		url      string
		r        Request
		done     bool
		wantErr  error
	}{
		{"done", true, home, Request{Check: check(true, nil), NeedsHuman: needsHuman}, true, nil},
		{"done wins over sign-in page", true, signIn, Request{Check: check(true, nil), NeedsHuman: needsHuman}, true, nil},
		{"headless sign-in page", true, signIn, Request{Check: check(false, nil), NeedsHuman: needsHuman}, false, ErrInteractionRequired},
		{"interactive sign-in page waits", false, signIn, Request{Check: check(false, nil), NeedsHuman: needsHuman}, false, nil},
		{"headless other page waits", true, home, Request{Check: check(false, nil), NeedsHuman: needsHuman}, false, nil},
		{"no NeedsHuman", true, signIn, Request{Check: check(false, nil)}, false, nil},
		{"check error", false, home, Request{Check: check(false, errBoom)}, false, errBoom},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done, err := decide(context.Background(), c.headless, c.url, nil, c.r)
			if done != c.done {
				t.Errorf("done = %v, want %v", done, c.done)
			}
			if c.wantErr == nil && err != nil || c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Errorf("err = %v, want %v", err, c.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Errorf("error leaks the page query: %v", err)
			}
		})
	}
}

func TestDecidePassesCookiesAndURL(t *testing.T) {
	in := []Cookie{{Name: "myacinfo", Value: "v", Domain: ".apple.com"}}
	var gotURL string
	var gotN int
	r := Request{Check: func(_ context.Context, u string, cs []Cookie) (bool, error) {
		gotURL, gotN = u, len(cs)
		return false, nil
	}}
	if _, err := decide(context.Background(), false, "https://x.example/", in, r); err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://x.example/" || gotN != 1 {
		t.Errorf("Check got %q, %d cookies", gotURL, gotN)
	}
}

func TestWrapTimeoutAndClose(t *testing.T) {
	parent := context.Background()
	expired, cancel := context.WithTimeout(parent, 0)
	defer cancel()
	<-expired.Done()

	s := &session{ctx: expired, parent: parent, timeout: 0}
	err := s.wrap(errors.New("x"))
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrInteractionRequired) {
		t.Errorf("interactive timeout: %v", err)
	}
	s.headless = true
	err = s.wrap(errors.New("x"))
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrInteractionRequired) {
		t.Errorf("headless timeout: %v", err)
	}

	gone, cancelGone := context.WithCancel(parent)
	cancelGone()
	s = &session{ctx: gone, parent: parent}
	if err := s.wrap(errors.New("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("browser gone: %v", err)
	}

	caller, cancelCaller := context.WithCancel(parent)
	cancelCaller()
	s = &session{ctx: gone, parent: caller}
	if err := s.wrap(errors.New("x")); !errors.Is(err, context.Canceled) || errors.Is(err, ErrClosed) {
		t.Errorf("caller cancelled: %v", err)
	}
}

func TestLoginValidates(t *testing.T) {
	ok := func(context.Context, string, []Cookie) (bool, error) { return true, nil }
	for name, c := range map[string]struct {
		o Options
		r Request
	}{
		"no StartURL":   {Options{ProfileDir: t.TempDir()}, Request{Check: ok}},
		"no Check":      {Options{ProfileDir: t.TempDir()}, Request{StartURL: "https://x/"}},
		"no ProfileDir": {Options{}, Request{StartURL: "https://x/", Check: ok}},
	} {
		if _, err := Login(context.Background(), c.o, c.r); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A renewal (or a second window) must never take over a profile another
// browser runs with, such as the human's open sign-in window.
func TestLoginLeavesBusyProfileAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	cmd := exec.Command("sh", "-c", "sleep 30; :", "sh", "--user-data-dir="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ok := func(context.Context, string, []Cookie) (bool, error) { return true, nil }
	modes := []bool{true}
	if HasDisplay() {
		modes = append(modes, false)
	}
	for _, headless := range modes {
		start := time.Now()
		_, err := Login(context.Background(), Options{ProfileDir: dir, Headless: headless, BinDir: t.TempDir(), BrowserPath: "/nonexistent"}, Request{StartURL: "https://x/", Check: ok})
		if !errors.Is(err, ErrProfileInUse) || time.Since(start) > 10*time.Second {
			t.Fatalf("headless=%v: err %v", headless, err)
		}
	}
	if len(profilePIDs(dir)) == 0 {
		t.Fatal("the other browser was stopped")
	}
}
