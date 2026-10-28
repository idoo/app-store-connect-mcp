// Package browser drives a dedicated Chromium-based browser profile through a
// sign-in on the vendor's own web page and captures the resulting session
// cookies over the DevTools protocol. It never touches the user's own browser
// profile, cookies or Keychain items.
package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/state"
)

// ErrNoBrowser means no Chrome, Brave, Edge or Chromium executable was found.
var ErrNoBrowser = errors.New("no Chromium-based browser found")

// EnvBrowser overrides browser auto-detection. The value "bundled" forces the
// pinned Chrome for Testing build.
const EnvBrowser = "ASC_MCP_BROWSER"

// Bundled is the browser path or $ASC_MCP_BROWSER value that selects Chrome for Testing.
const Bundled = "bundled"

// resolveBrowser resolves the browser executable: an explicit path first,
// then $ASC_MCP_BROWSER ("bundled" forces Chrome for Testing), then the
// system default browser if it is Chromium-based, then other installed
// Chromium browsers, then Chrome for Testing, downloaded when needed. It
// also returns the browser's display name and an optional note explaining
// why it is not the user's default browser.
func resolveBrowser(ctx context.Context, path, binDir string, log io.Writer) (exe, name, note string, err error) {
	p, tried := locateHere(path, binDir)
	if !p.cft {
		return p.exe, displayName(p.exe), p.note, nil
	}
	if p.exe == "" {
		return "", "", "", noBrowser(tried, p.exe, runtime.GOOS, runtime.GOARCH)
	}
	if err := cftSandboxBlocked(runtime.GOOS, readSysctl); err != nil {
		return "", "", "", err
	}
	if !isFile(p.exe) {
		if p.exe, err = ensureCfT(ctx, binDir, log); err != nil {
			return "", "", "", err
		}
	}
	if runtime.GOOS == "linux" {
		if err := cftRuns(ctx, p.exe); err != nil {
			return "", "", "", err
		}
	}
	return p.exe, "Chrome for Testing", p.note, nil
}

func locateHere(path, binDir string) (pick, []string) {
	home, _ := os.UserHomeDir()
	return locate(path, os.Getenv(EnvBrowser), detectDefault, candidates(runtime.GOOS, home, os.Getenv), vetCandidate, cftExe(binDir, cftPlatform(runtime.GOOS, runtime.GOARCH)))
}

// pick is locate's answer. cft means exe is the Chrome for Testing path,
// which may not be downloaded yet ("" when the platform has no build). note,
// if set, tells the user why their default browser is not used.
type pick struct {
	exe  string
	cft  bool
	note string
}

// locate applies the precedence. def is only consulted when neither an
// explicit path nor the environment chooses the browser. vet (nil = accept)
// screens each auto-detected candidate; explicit choices skip it.
func locate(path, env string, def func() defaultBrowser, cands []string, vet func(string) (string, bool), cftPath string) (pick, []string) {
	var tried []string
	for _, p := range []string{path, env} {
		if p == "" {
			continue
		}
		if strings.EqualFold(p, Bundled) {
			return pick{exe: cftPath, cft: true}, tried
		}
		if r, ok := resolve(p); ok {
			return pick{exe: r}, nil
		}
		tried = append(tried, p)
	}
	var d defaultBrowser
	if def != nil {
		d = def()
	}
	if d.Chromium && d.Exe != "" {
		if r, ok := resolve(d.Exe); ok {
			return pick{exe: r}, nil
		}
		tried = append(tried, d.Exe)
	}
	because := func(using string) string {
		if d.Name == "" {
			return ""
		}
		return "Your default browser (" + d.Name + ") can't be driven; using " + using + " in a separate profile."
	}
	for _, c := range cands {
		r, ok := resolve(c)
		if ok && vet != nil {
			r, ok = vet(r)
		}
		if ok {
			return pick{exe: r, note: because(displayName(r))}, nil
		}
		tried = append(tried, c)
	}
	note := because("Chrome for Testing " + cftVersion)
	if note == "" {
		note = "No Chromium-based browser found; using Chrome for Testing " + cftVersion + " in a separate profile."
	}
	return pick{exe: cftPath, cft: true, note: note}, tried
}

func noBrowser(tried []string, cftPath, goos, goarch string) error {
	msg := "no Chrome for Testing build for this platform"
	if cftPath != "" {
		msg = "Chrome for Testing " + cftVersion + " not downloaded yet (`app-store-connect-mcp web login` fetches it)"
	}
	return fmt.Errorf("%w (tried: %s; %s); %s, or point %s at a browser (Chromium only this way)", ErrNoBrowser, strings.Join(tried, ", "), msg, installHint(goos, goarch), EnvBrowser)
}

// installHint names browsers that exist for the platform. Linux arm64 has no
// Chrome, Edge or Chrome for Testing build, and Ubuntu's chromium is a snap
// (which can't use our profile).
func installHint(goos, goarch string) string {
	if goos == "linux" && goarch == "arm64" {
		return "install the Debian/Fedora `chromium` package (not the snap) or Brave"
	}
	return "install Chrome, Brave or Edge"
}

// defaultBinDir is <state dir>/browser-bin.
func defaultBinDir() string {
	d, err := state.DefaultDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "browser-bin")
}

// appBundle returns the .app bundle containing a macOS executable, or "".
func appBundle(exe string) string {
	i := strings.Index(exe, ".app/Contents/MacOS/")
	if i < 0 {
		return ""
	}
	return exe[:i+len(".app")]
}

// resolve accepts an absolute/relative path to an existing file or a bare
// command name found on PATH.
func resolve(p string) (string, bool) {
	if !strings.ContainsRune(p, filepath.Separator) && !strings.ContainsRune(p, '/') {
		r, err := exec.LookPath(p)
		return r, err == nil
	}
	fi, err := os.Stat(p)         //nolint:gosec // G703: stat only; p is the user's own browser path setting
	if err != nil || fi.IsDir() { //nolint:gosec // G703: stat only; p is the user's own browser path setting
		return "", false
	}
	return p, true
}

// candidates lists well-known browser executables for goos. Linux entries are
// bare command names resolved on PATH. macOS lists only vendor-signed
// browsers (Chromium is unsigned; it is used only when named explicitly).
func candidates(goos, home string, getenv func(string) string) []string {
	switch goos {
	case "darwin":
		apps := []struct{ bundle, bin string }{
			{"Google Chrome", "Google Chrome"},
			{"Brave Browser", "Brave Browser"},
			{"Microsoft Edge", "Microsoft Edge"},
		}
		roots := []string{"/Applications"}
		if home != "" {
			roots = append(roots, filepath.Join(home, "Applications"))
		}
		var out []string
		for _, a := range apps {
			for _, r := range roots {
				out = append(out, r+"/"+a.bundle+".app/Contents/MacOS/"+a.bin)
			}
		}
		return out
	case "windows":
		rels := []string{
			`Google\Chrome\Application\chrome.exe`,
			`BraveSoftware\Brave-Browser\Application\brave.exe`,
			`Microsoft\Edge\Application\msedge.exe`,
			`Chromium\Application\chrome.exe`,
		}
		var roots []string
		for _, k := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if v := getenv(k); v != "" {
				roots = append(roots, v)
			}
		}
		var out []string
		for _, rel := range rels {
			for _, r := range roots {
				out = append(out, strings.TrimRight(r, `\`)+`\`+rel)
			}
		}
		return out
	default:
		return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "brave-browser", "brave-browser-stable", "brave", "microsoft-edge", "microsoft-edge-stable", "vivaldi-stable"}
	}
}

// ProfileExists reports whether dir looks like a profile a previous login
// used (Chromium has written its cookie database there).
func ProfileExists(dir string) bool {
	if dir == "" {
		return false
	}
	for _, p := range []string{
		filepath.Join(dir, "Default", "Network", "Cookies"),
		filepath.Join(dir, "Default", "Cookies"),
	} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return true
		}
	}
	return false
}
