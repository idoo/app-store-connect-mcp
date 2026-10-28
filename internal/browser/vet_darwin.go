//go:build darwin

package browser

import (
	"os"
	"path/filepath"
	"strings"
)

// verifyApp checks an .app's signature against the pinned team and bundle
// id. Tests swap it.
var verifyApp = vendorSigned

// vetCandidate admits an auto-detected macOS browser only if, after
// resolving symlinks, it is an .app under /Applications or ~/Applications
// whose bundle id has a pinned vendor team and whose signature matches it.
func vetCandidate(exe string) (string, bool) {
	home, _ := os.UserHomeDir()
	return vetMacExe(exe, home)
}

func vetMacExe(exe, home string) (string, bool) {
	r, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", false
	}
	app := appBundle(r)
	if app == "" || !trustedAppDir(app, home) {
		return "", false
	}
	out, err := runCmd("plutil", "-extract", "CFBundleIdentifier", "raw", filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(out))
	team := macTeamID(id)
	if team == "" || !verifyApp(app, id, team) {
		return "", false
	}
	return r, true
}
