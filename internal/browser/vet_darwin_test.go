//go:build darwin

package browser

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeBundle creates root/<name>.app with an Info.plist whose content is the
// bundle id (the fake plutil returns the file as is) and returns its exe.
func fakeBundle(t *testing.T, root, name, id string) string {
	t.Helper()
	app := filepath.Join(root, name+".app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(app, "Contents", "MacOS", name)
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	return exe
}

// TestVetMacExeRejectsUntrustedCandidates: a fallback candidate (not the
// default browser) must pass the same symlink → folder → vendor signature
// chain, so a dropped-in "Chromium" or unsigned copy never sees the password.
func TestVetMacExeRejectsUntrustedCandidates(t *testing.T) {
	home := t.TempDir()
	apps := filepath.Join(home, "Applications")
	elsewhere := filepath.Join(home, "Downloads")

	oldRun, oldVerify := runCmd, verifyApp
	t.Cleanup(func() { runCmd, verifyApp = oldRun, oldVerify })
	runCmd = func(name string, args ...string) ([]byte, error) {
		if name != "plutil" {
			return nil, errors.New("unexpected " + name)
		}
		return os.ReadFile(args[len(args)-1]) //nolint:gosec // test fixture path
	}
	signed := map[string]bool{} // app path → vendor signature valid
	verifyApp = func(app, id, team string) bool {
		return signed[app] && macTeamID(id) == team
	}

	good := fakeBundle(t, apps, "Google Chrome", "com.google.Chrome")
	unsigned := fakeBundle(t, apps, "Brave Browser", "com.brave.Browser")
	chromium := fakeBundle(t, apps, "Chromium", "org.chromium.Chromium")
	impostor := fakeBundle(t, apps, "Microsoft Edge", "com.evil.edge")
	outside := fakeBundle(t, elsewhere, "Google Chrome", "com.google.Chrome")
	link := filepath.Join(apps, "Linked.app")
	if err := os.Symlink(filepath.Dir(filepath.Dir(filepath.Dir(outside))), link); err != nil {
		t.Fatal(err)
	}
	for _, exe := range []string{good, chromium, impostor, outside} {
		r, _ := filepath.EvalSymlinks(exe)
		signed[appBundle(r)] = true // even a valid signature can't rescue a wrong id or folder
	}

	if r, ok := vetMacExe(good, home); !ok || filepath.Base(r) != "Google Chrome" {
		t.Errorf("vendor-signed Chrome in ~/Applications rejected: %q %v", r, ok)
	}
	for name, exe := range map[string]string{
		"unsigned Brave":              unsigned,
		"Chromium (no vendor pin)":    chromium,
		"non-vendor bundle id":        impostor,
		"outside Applications":        outside,
		"symlink out of Applications": filepath.Join(link, "Contents", "MacOS", "Google Chrome"),
	} {
		if r, ok := vetMacExe(exe, home); ok {
			t.Errorf("%s accepted as %q", name, r)
		}
	}

	// End to end: locate never falls back to a rejected candidate.
	vet := func(e string) (string, bool) { return vetMacExe(e, home) }
	if p, _ := locate("", "", func() defaultBrowser { return defaultBrowser{Name: "Safari"} }, []string{unsigned, chromium, impostor}, vet, "/cft"); !p.cft {
		t.Errorf("locate used an untrusted candidate: %+v", p)
	}
}
