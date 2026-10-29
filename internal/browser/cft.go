package browser

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Chrome for Testing is the pinned fallback when no Chromium-based browser is
// installed (or ASC_MCP_BROWSER=bundled). Hashes are of the official zips.
const cftVersion = "156.0.8078.47"

var cftSHA256 = map[string]string{
	"mac-arm64": "ad7f2b49a4dc679af286b1b1b386670d93ca7868079f69012d21ba0ac8293df3",
	"mac-x64":   "5a38b8e3d621514fff0775dcae678a152ce378890188c88eef79fc97aa59fccd",
	"linux64":   "e6c94490f0b6b337225aeb2d2a5034858516b7017356980ba94f5176d8774a50",
	"win64":     "a4e0d57245e167e98a9d465af1e3774d2b4ec9f04e483d19bcd97a6f63cf46dd",
}

// cftPlatform maps GOOS/GOARCH to a Chrome for Testing platform, "" if none.
func cftPlatform(goos, goarch string) string {
	switch {
	case goos == "darwin" && goarch == "arm64":
		return "mac-arm64"
	case goos == "darwin" && goarch == "amd64":
		return "mac-x64"
	case goos == "linux" && goarch == "amd64":
		return "linux64"
	case goos == "windows" && (goarch == "amd64" || goarch == "arm64"):
		// Windows 11 on ARM runs the x64 build under emulation.
		return "win64"
	}
	return ""
}

func cftURL(platform string) string {
	return "https://storage.googleapis.com/chrome-for-testing-public/" + cftVersion + "/" + platform + "/chrome-" + platform + ".zip"
}

// cftDir is where one platform's build is extracted.
func cftDir(binDir, platform string) string {
	return filepath.Join(binDir, cftVersion, platform)
}

// cftExe is the executable inside an extracted build ("" for no platform).
func cftExe(binDir, platform string) string {
	if platform == "" || binDir == "" {
		return ""
	}
	top := filepath.Join(cftDir(binDir, platform), "chrome-"+platform)
	switch {
	case strings.HasPrefix(platform, "mac-"):
		return filepath.Join(top, "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing")
	case platform == "win64":
		return filepath.Join(top, "chrome.exe")
	default:
		return filepath.Join(top, "chrome")
	}
}

// ensureCfT returns the pinned Chrome for Testing executable, downloading,
// verifying and extracting it first if needed.
func ensureCfT(ctx context.Context, binDir string, log io.Writer) (string, error) {
	platform := cftPlatform(runtime.GOOS, runtime.GOARCH)
	if platform == "" {
		return "", fmt.Errorf("%w: Chrome for Testing has no build for %s/%s", ErrNoBrowser, runtime.GOOS, runtime.GOARCH)
	}
	exe := cftExe(binDir, platform)
	if isFile(exe) {
		return exe, nil
	}
	if log == nil {
		log = io.Discard
	}
	verDir := filepath.Join(binDir, cftVersion)
	if err := os.MkdirAll(verDir, 0o700); err != nil {
		return "", fmt.Errorf("browser: %w", err)
	}
	fmt.Fprintf(log, "downloading Chrome for Testing %s (~200 MB) once\n", cftVersion)
	zf, err := os.CreateTemp(verDir, ".download-*.zip")
	if err != nil {
		return "", fmt.Errorf("browser: %w", err)
	}
	defer os.Remove(zf.Name())
	sum, err := download(ctx, cftURL(platform), zf)
	if cerr := zf.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("browser: download Chrome for Testing: %w", err)
	}
	if err := verifyAndExtract(zf.Name(), sum, cftSHA256[platform], cftDir(binDir, platform)); err != nil {
		return "", err
	}
	if !isFile(exe) {
		return "", fmt.Errorf("browser: Chrome for Testing extracted but %s is missing", exe)
	}
	fmt.Fprintf(log, "Chrome for Testing %s ready\n", cftVersion)
	return exe, nil
}

// usernsSysctl is 1 by default on Ubuntu 23.10+: AppArmor then denies the
// unprivileged user namespaces Chromium's sandbox needs to any binary without
// an AppArmor profile, such as Chrome for Testing under our state dir.
const usernsSysctl = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"

var readSysctl = func(p string) (string, error) {
	b, err := os.ReadFile(p) //nolint:gosec // fixed /proc path
	return string(b), err
}

// cftSandboxBlocked fails fast, before any download, where Chrome for Testing
// would die with "No usable sandbox!". read reads a sysctl file. The sandbox
// is never disabled to work around it.
func cftSandboxBlocked(goos string, read func(string) (string, error)) error {
	if goos != "linux" {
		return nil
	}
	v, err := read(usernsSysctl)
	if err != nil || strings.TrimSpace(v) != "1" {
		return nil
	}
	return fmt.Errorf("%w: Chrome for Testing can't start its sandbox here (AppArmor restricts unprivileged user namespaces: kernel.apparmor_restrict_unprivileged_userns=1); install Google Chrome, Brave or Edge from their .deb packages (they ship AppArmor profiles), or set %s to such a browser", ErrNoBrowser, EnvBrowser)
}

// cftRuns starts `chrome --version` so a missing shared library shows up as
// an install hint instead of a raw loader error at launch.
func cftRuns(ctx context.Context, exe string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "--version").CombinedOutput() //nolint:gosec // our own Chrome for Testing path
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("%w: Chrome for Testing can't run here: %s; install google-chrome-stable, or its libraries (Debian/Ubuntu: apt install libnss3 libgbm1 libatk-bridge2.0-0 libasound2 libxkbcommon0 libgtk-3-0), or set %s to an installed browser", ErrNoBrowser, msg, EnvBrowser)
}

// download streams url into w and returns the hex sha256 of what it wrote.
func download(ctx context.Context, url string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyAndExtract checks the zip's hash and atomically extracts it to dest:
// first into a sibling temp dir, then renamed into place.
func verifyAndExtract(zipPath, gotSum, wantSum, dest string) error {
	if wantSum == "" || !strings.EqualFold(gotSum, wantSum) {
		return fmt.Errorf("browser: Chrome for Testing checksum mismatch (got %s, want %s); refusing to use it", gotSum, wantSum)
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("browser: %w", err)
	}
	tmp, err := os.MkdirTemp(parent, ".extract-"+filepath.Base(dest)+"-")
	if err != nil {
		return fmt.Errorf("browser: %w", err)
	}
	if err := extractZip(zipPath, tmp); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("browser: extract Chrome for Testing: %w", err)
	}
	// Defender scanning the fresh exe/dll files can block the rename for a
	// moment on Windows; retry rather than throw away a 200 MB extract.
	if err := retryBusy(func() error { return os.Rename(tmp, dest) }); err != nil {
		os.RemoveAll(tmp)
		if _, serr := os.Stat(dest); serr == nil {
			return nil // another run finished first
		}
		return fmt.Errorf("browser: install Chrome for Testing: %w", err)
	}
	return nil
}

var errUnsafeZip = errors.New("unsafe path in archive")

// rooted reports whether an archive path or symlink target (already
// FromSlash'd) is absolute, has a volume, or starts at a root: Windows treats
// `\etc` as rooted on the current drive although filepath.IsAbs says no.
func rooted(p string) bool {
	return filepath.IsAbs(p) || filepath.VolumeName(p) != "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`)
}

const maxZipEntry = 2 << 30

// extractZip extracts into dest, keeping permission bits and symlinks. It
// refuses entries or symlink targets that would land outside dest.
func extractZip(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for _, f := range r.File {
		rel := filepath.FromSlash(f.Name)
		if rooted(rel) {
			return fmt.Errorf("%w: %q", errUnsafeZip, f.Name)
		}
		p := filepath.Join(root, rel)
		if !within(root, p) {
			return fmt.Errorf("%w: %q", errUnsafeZip, f.Name)
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(p, 0o755); err != nil { //nolint:gosec // G301: Chrome for Testing app bundle dirs; standard 0755 like the archive
				return err
			}
		case mode&os.ModeSymlink != 0:
			target, err := readSmall(f, 4096)
			if err != nil {
				return err
			}
			t := filepath.FromSlash(target)
			if t == "" || rooted(t) || !within(root, filepath.Join(filepath.Dir(p), t)) {
				return fmt.Errorf("%w: symlink %q -> %q", errUnsafeZip, f.Name, target)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // G301: see above
				return err
			}
			if err := os.Symlink(t, p); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // G301: see above
				return err
			}
			if err := writeEntry(f, p, mode.Perm()|0o600); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: %q has unsupported type %v", errUnsafeZip, f.Name, mode.Type())
		}
	}
	return nil
}

func writeEntry(f *zip.File, p string, perm os.FileMode) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm) //nolint:gosec // G304: p was checked to stay inside dest by extractZip
	if err != nil {
		return err
	}
	// Cap each entry against decompression bombs; the largest real Chrome for
	// Testing entry is a few hundred MB.
	n, err := io.Copy(out, io.LimitReader(rc, maxZipEntry+1))
	if err == nil && n > maxZipEntry {
		err = fmt.Errorf("%w: %q exceeds %d bytes", errUnsafeZip, f.Name, maxZipEntry)
	}
	if err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(p, perm) // undo umask
}

func readSmall(f *zip.File, limit int64) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > limit {
		return "", fmt.Errorf("%w: symlink %q target too long", errUnsafeZip, f.Name)
	}
	return string(b), nil
}

// within reports whether p is root or lexically inside it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func isFile(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
