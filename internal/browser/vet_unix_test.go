//go:build !darwin && !windows

package browser

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVetCandidateSkipsSnap: snap Chromium can't open the hidden profile
// dir, so it must be skipped and detection fall through to Chrome for Testing.
func TestVetCandidateSkipsSnap(t *testing.T) {
	dir := t.TempDir()
	launcher := filepath.Join(dir, "snap")
	wrapper := filepath.Join(dir, "chromium-browser")
	viaLink := filepath.Join(dir, "chromium")
	chrome := filepath.Join(dir, "google-chrome")
	for f, body := range map[string]string{
		launcher: "\x7fELF",
		wrapper:  "#!/bin/sh\nexec /snap/bin/chromium \"$@\"\n",
		chrome:   "\x7fELF real chrome",
	} {
		if err := os.WriteFile(f, []byte(body), 0o700); err != nil { //nolint:gosec // test fixture
			t.Fatal(err)
		}
	}
	if err := os.Symlink(launcher, viaLink); err != nil {
		t.Fatal(err)
	}
	for _, exe := range []string{wrapper, viaLink, "/snap/bin/chromium"} {
		if _, ok := vetCandidate(exe); ok {
			t.Errorf("snap target %s accepted", exe)
		}
	}
	if r, ok := vetCandidate(chrome); !ok || r != chrome {
		t.Errorf("regular browser rejected: %q %v", r, ok)
	}
	if p, _ := locate("", "", nil, []string{wrapper, viaLink}, vetCandidate, "/cft"); !p.cft {
		t.Errorf("locate picked a snap: %+v", p)
	}
}
