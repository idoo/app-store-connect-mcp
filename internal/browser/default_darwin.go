//go:build darwin

package browser

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runCmd runs a helper tool and returns its combined output (codesign -d
// writes to stderr). Tests swap it to avoid real subprocesses.
var runCmd = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput() //nolint:gosec // G204: callers pass fixed tool names (plutil, codesign, defaults); no shell
}

// macTeamIDs pins the Apple Developer Team ID that signs each vendor's
// browser, by bundle-id prefix. Bundle ids missing here (Chromium, Vivaldi,
// Opera, Arc) are never launched as the default browser; detection falls
// through to the other candidates.
var macTeamIDs = []struct{ prefix, team string }{
	{"com.google.chrome", "EQHXZ8M8AV"},
	{"com.brave.browser", "KL8N8XSYF4"},
	{"com.microsoft.edgemac", "UBF8T346G9"},
}

func macTeamID(bundleID string) string {
	id := strings.ToLower(strings.TrimSpace(bundleID))
	for _, t := range macTeamIDs {
		if id == t.prefix || strings.HasPrefix(id, t.prefix+".") {
			return t.team
		}
	}
	return ""
}

var detectDefault = func() defaultBrowser {
	home, _ := os.UserHomeDir()
	out, err := runCmd("plutil", "-convert", "json", "-o", "-",
		filepath.Join(home, "Library", "Preferences", "com.apple.LaunchServices", "com.apple.launchservices.secure.plist"))
	if err != nil {
		return defaultBrowser{}
	}
	id := httpsHandlerFromPlistJSON(out)
	if id == "" {
		return defaultBrowser{}
	}
	kb, ok := macBrowser(id)
	if !ok {
		return defaultBrowser{Name: id}
	}
	d := defaultBrowser{Name: kb.name, Chromium: kb.chromium}
	if !kb.chromium {
		return d
	}
	d.Exe = macDefaultExe(id, kb.name, home)
	return d
}

// macDefaultExe finds the executable of a trusted, vendor-signed install of
// bundle id, or "".
func macDefaultExe(id, name, home string) string {
	team := macTeamID(id)
	if team == "" {
		return ""
	}
	for _, app := range macAppPaths(id, name, home) {
		resolved, err := filepath.EvalSymlinks(app)
		if err != nil || !trustedAppDir(resolved, home) || !vendorSigned(resolved, id, team) {
			continue
		}
		bin, err := runCmd("plutil", "-extract", "CFBundleExecutable", "raw", filepath.Join(resolved, "Contents", "Info.plist"))
		if err != nil {
			continue
		}
		exe := filepath.Join(resolved, "Contents", "MacOS", strings.TrimSpace(string(bin)))
		if isFile(exe) {
			return exe
		}
	}
	return ""
}

// macAppPaths lists candidate .app bundles for a bundle id: Spotlight hits
// inside the Applications folders first, then the usual install locations.
// Spotlight also indexes copies in repos, Downloads and mounted images; those
// are dropped.
func macAppPaths(id, name, home string) []string {
	var apps []string
	if out, err := runCmd("mdfind", "kMDItemCFBundleIdentifier == '"+strings.ReplaceAll(id, "'", "")+"'c"); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimSpace(l); strings.HasSuffix(l, ".app") && trustedAppDir(l, home) {
				apps = append(apps, l)
			}
		}
	}
	apps = append(apps, filepath.Join("/Applications", name+".app"))
	if home != "" {
		apps = append(apps, filepath.Join(home, "Applications", name+".app"))
	}
	return apps
}

// trustedAppDir reports whether app lies under /Applications or
// ~/Applications.
func trustedAppDir(app, home string) bool {
	if !filepath.IsAbs(app) {
		return false
	}
	app = filepath.Clean(app)
	roots := []string{"/Applications"}
	if home != "" && filepath.IsAbs(home) {
		roots = append(roots, filepath.Join(home, "Applications"))
		if r, err := filepath.EvalSymlinks(home); err == nil && r != home {
			roots = append(roots, filepath.Join(r, "Applications"))
		}
	}
	for _, r := range roots {
		if strings.HasPrefix(app, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// vendorSigned reports whether app has an intact signature from team with
// the expected bundle identifier.
func vendorSigned(app, id, team string) bool {
	// No --strict: browsers that update themselves in place (Brave) carry
	// extended-attribute "detritus" that --strict rejects; the signature and
	// sealed resources are still verified.
	if _, err := runCmd("codesign", "--verify", "--deep", app); err != nil {
		return false
	}
	out, err := runCmd("codesign", "-dv", "--verbose=2", app)
	if err != nil {
		return false
	}
	var gotTeam, gotID string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch k {
		case "TeamIdentifier":
			gotTeam = strings.TrimSpace(v)
		case "Identifier":
			gotID = strings.TrimSpace(v)
		}
	}
	return gotTeam == team && strings.EqualFold(gotID, id)
}
