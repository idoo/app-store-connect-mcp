//go:build darwin

package browser

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeCall struct {
	name string
	args []string
}

// fakeRunner answers mdfind, codesign and plutil without subprocesses.
func fakeRunner(t *testing.T, mdfind string, verifyOK bool, signInfo string, calls *[]fakeCall) {
	t.Helper()
	old := runCmd
	t.Cleanup(func() { runCmd = old })
	runCmd = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, fakeCall{name, args})
		switch {
		case name == "mdfind":
			return []byte(mdfind), nil
		case name == "codesign" && len(args) > 0 && args[0] == "--verify":
			if !verifyOK {
				return []byte("invalid signature"), errors.New("exit status 1")
			}
			return nil, nil
		case name == "codesign":
			return []byte(signInfo), nil
		case name == "plutil":
			return []byte("Google Chrome\n"), nil
		}
		return nil, errors.New("unexpected " + name)
	}
}

func TestMacAppPathsDropsUntrustedSpotlightHits(t *testing.T) {
	var calls []fakeCall
	home := "/Users/x"
	fakeRunner(t, strings.Join([]string{
		"/Users/x/code/repo/Google Chrome.app",
		"/Users/x/Downloads/Google Chrome.app",
		"/Volumes/Chrome/Google Chrome.app",
		"/ApplicationsEvil/Google Chrome.app",
		"/Applications/../tmp/Google Chrome.app",
		"/Applications/Google/Google Chrome.app",
		"/Users/x/Applications/Chrome Apps/Google Chrome.app",
	}, "\n"), true, "", &calls)
	got := macAppPaths("com.google.Chrome", "Google Chrome", home)
	want := []string{
		"/Applications/Google/Google Chrome.app",
		"/Users/x/Applications/Chrome Apps/Google Chrome.app",
		"/Applications/Google Chrome.app",
		"/Users/x/Applications/Google Chrome.app",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("macAppPaths = %q, want %q", got, want)
	}
}

// fakeApp makes a bundle under a temporary ~/Applications.
func fakeApp(t *testing.T) (home, app string) {
	t.Helper()
	home = t.TempDir()
	app = filepath.Join(home, "Applications", "Google Chrome.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "Google Chrome"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home, app
}

func TestMacDefaultExeSignature(t *testing.T) {
	good := "Executable=/x\nIdentifier=com.google.Chrome\nFormat=app bundle\nTeamIdentifier=EQHXZ8M8AV\n"
	cases := []struct {
		name     string
		verifyOK bool
		info     string
		ok       bool
	}{
		{"vendor signed", true, good, true},
		{"broken signature", false, good, false},
		{"wrong team", true, strings.Replace(good, "EQHXZ8M8AV", "ABCDE12345", 1), false},
		{"ad-hoc", true, "Identifier=com.google.Chrome\nTeamIdentifier=not set\n", false},
		{"other app of the team", true, strings.Replace(good, "com.google.Chrome", "com.google.Keystone", 1), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home, app := fakeApp(t)
			var calls []fakeCall
			fakeRunner(t, app+"\n", c.verifyOK, c.info, &calls)
			got := macDefaultExe("com.google.Chrome", "Google Chrome", home)
			if c.ok != (got != "") {
				t.Fatalf("macDefaultExe = %q, want ok=%v", got, c.ok)
			}
			if c.ok && !strings.HasSuffix(got, "/Contents/MacOS/Google Chrome") {
				t.Fatalf("macDefaultExe = %q", got)
			}
		})
	}
}

func TestMacDefaultExeRejectsRepoCopy(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "code", "repo", "Google Chrome.app")
	if err := os.MkdirAll(filepath.Join(repo, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "Contents", "MacOS", "Google Chrome"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink in ~/Applications pointing at the repo copy is no better.
	if err := os.MkdirAll(filepath.Join(home, "Applications"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, filepath.Join(home, "Applications", "Google Chrome.app")); err != nil {
		t.Fatal(err)
	}
	var calls []fakeCall
	fakeRunner(t, repo+"\n", true, "Identifier=com.google.Chrome\nTeamIdentifier=EQHXZ8M8AV\n", &calls)
	// The real /Applications fallback may exist on this machine; only the
	// repo copy (directly or via the symlink) must never be chosen.
	if got := macDefaultExe("com.google.Chrome", "Google Chrome", home); strings.Contains(got, "code/repo") || strings.HasPrefix(got, home) {
		t.Fatalf("macDefaultExe = %q, want repo copy rejected", got)
	}
	for _, c := range calls {
		if c.name == "codesign" && strings.Contains(strings.Join(c.args, " "), "code/repo") {
			t.Fatalf("codesign ran on untrusted path: %v", c.args)
		}
	}
}

func TestMacDefaultExeUnknownVendor(t *testing.T) {
	home, app := fakeApp(t)
	var calls []fakeCall
	fakeRunner(t, app+"\n", true, "Identifier=com.vivaldi.Vivaldi\nTeamIdentifier=ZZZZZZZZZZ\n", &calls)
	if got := macDefaultExe("com.vivaldi.Vivaldi", "Vivaldi", home); got != "" {
		t.Fatalf("macDefaultExe = %q, want unknown vendor skipped", got)
	}
	if len(calls) != 0 {
		t.Fatalf("ran %v for an unknown vendor", calls)
	}
}
