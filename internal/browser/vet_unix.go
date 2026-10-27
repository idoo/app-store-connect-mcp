//go:build !darwin && !windows

package browser

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// vetCandidate skips snap browsers: snap confinement blocks the hidden
// profile directory under $HOME, so detection falls through to Chrome for
// Testing instead.
func vetCandidate(exe string) (string, bool) {
	if isSnap(exe) {
		return "", false
	}
	return exe, true
}

// isSnap reports whether exe starts a snap: it lives under /snap/, resolves
// to the snap launcher, or is a wrapper script that execs /snap/bin/...
// (Ubuntu's /usr/bin/chromium-browser).
func isSnap(exe string) bool {
	r, err := filepath.EvalSymlinks(exe)
	if err != nil {
		r = exe
	}
	for _, p := range []string{exe, r} {
		if strings.HasPrefix(p, "/snap/") || filepath.Base(p) == "snap" {
			return true
		}
	}
	fi, err := os.Stat(r)
	if err != nil || fi.Size() > 16<<10 {
		return false
	}
	b, err := os.ReadFile(r) //nolint:gosec // small file on PATH, read only to detect a snap wrapper
	return err == nil && bytes.HasPrefix(b, []byte("#!")) && bytes.Contains(b, []byte("/snap/"))
}
