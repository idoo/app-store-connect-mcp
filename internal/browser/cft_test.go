package browser

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type zipEntry struct {
	name, body string
	mode       os.FileMode
}

func makeZip(t *testing.T, entries []zipEntry) (path, sum string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "a.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(e.mode)
		ew, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ew.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	b, _ := os.ReadFile(path)
	s := sha256.Sum256(b)
	return path, hex.EncodeToString(s[:])
}

func TestExtractZipKeepsSymlinksAndExecBits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	zp, sum := makeZip(t, []zipEntry{
		{name: "top/", mode: os.ModeDir | 0o755},
		{name: "top/Versions/A/bin", body: "#!/bin/sh\n", mode: 0o755},
		{name: "top/Versions/A/data.txt", body: "x", mode: 0o644},
		{name: "top/Versions/Current", body: "A", mode: os.ModeSymlink | 0o777},
	})
	dest := filepath.Join(t.TempDir(), "out")
	if err := verifyAndExtract(zp, sum, sum, dest); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dest, "top/Versions/A/bin"))
	if err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("exec bit lost: %v %v", fi, err)
	}
	if fi, _ := os.Stat(filepath.Join(dest, "top/Versions/A/data.txt")); fi.Mode().Perm()&0o111 != 0 {
		t.Errorf("data.txt became executable: %v", fi.Mode())
	}
	if tgt, err := os.Readlink(filepath.Join(dest, "top/Versions/Current")); err != nil || tgt != "A" {
		t.Fatalf("symlink = %q, %v", tgt, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "top/Versions/Current/bin")); err != nil {
		t.Errorf("symlink does not resolve: %v", err)
	}
	// No temp dirs left next to dest.
	ents, _ := os.ReadDir(filepath.Dir(dest))
	if len(ents) != 1 {
		t.Errorf("leftovers next to dest: %v", ents)
	}
}

func TestExtractZipRefusesEscapes(t *testing.T) {
	cases := map[string][]zipEntry{
		"dotdot":           {{name: "../evil", body: "x", mode: 0o644}},
		"nested dotdot":    {{name: "a/../../evil", body: "x", mode: 0o644}},
		"absolute":         {{name: "/etc/evil", body: "x", mode: 0o644}},
		"symlink escape":   {{name: "a/link", body: "../../outside", mode: os.ModeSymlink | 0o777}},
		"symlink absolute": {{name: "link", body: "/etc", mode: os.ModeSymlink | 0o777}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			zp, sum := makeZip(t, entries)
			base := t.TempDir()
			dest := filepath.Join(base, "out")
			err := verifyAndExtract(zp, sum, sum, dest)
			if !errors.Is(err, errUnsafeZip) {
				t.Fatalf("err = %v, want errUnsafeZip", err)
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Error("dest created despite refusal")
			}
			if _, err := os.Lstat(filepath.Join(base, "evil")); err == nil {
				t.Error("file written outside dest")
			}
		})
	}
}

func TestVerifyRefusesHashMismatch(t *testing.T) {
	zp, sum := makeZip(t, []zipEntry{{name: "f", body: "x", mode: 0o644}})
	dest := filepath.Join(t.TempDir(), "out")
	err := verifyAndExtract(zp, sum, "00"+sum[2:], dest)
	if err == nil {
		t.Fatal("mismatch accepted")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("extracted despite mismatch")
	}
	if err := verifyAndExtract(zp, sum, "", dest); err == nil {
		t.Fatal("empty pinned hash accepted")
	}
}

func TestPinnedHashes(t *testing.T) {
	for _, p := range []string{"mac-arm64", "mac-x64", "linux64", "win64"} {
		if len(cftSHA256[p]) != 64 {
			t.Errorf("%s: hash %q", p, cftSHA256[p])
		}
	}
	if got := cftURL("mac-arm64"); got != "https://storage.googleapis.com/chrome-for-testing-public/"+cftVersion+"/mac-arm64/chrome-mac-arm64.zip" {
		t.Errorf("cftURL = %s", got)
	}
}

func TestCftPlatformAndExe(t *testing.T) {
	cases := map[[2]string]string{
		{"darwin", "arm64"}: "mac-arm64", {"darwin", "amd64"}: "mac-x64",
		{"linux", "amd64"}: "linux64", {"windows", "amd64"}: "win64",
		{"windows", "arm64"}: "win64", // x64 emulation on Windows 11 ARM
		{"linux", "arm64"}:   "", {"freebsd", "amd64"}: "",
	}
	for in, want := range cases {
		if got := cftPlatform(in[0], in[1]); got != want {
			t.Errorf("cftPlatform(%v) = %q, want %q", in, got, want)
		}
	}
	want := filepath.Join("/b", cftVersion, "mac-arm64", "chrome-mac-arm64", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing")
	if got := cftExe("/b", "mac-arm64"); got != want {
		t.Errorf("cftExe = %s", got)
	}
	if got := cftExe("/b", "linux64"); filepath.Base(got) != "chrome" {
		t.Errorf("linux exe = %s", got)
	}
	if got := cftExe("/b", "win64"); filepath.Base(got) != "chrome.exe" {
		t.Errorf("win exe = %s", got)
	}
	if cftExe("/b", "") != "" || cftExe("", "linux64") != "" {
		t.Error("empty platform/bin dir must give empty exe")
	}
}

func TestLocatePrecedence(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	explicit, env, installed, brave := mk("explicit"), mk("env"), mk("installed"), mk("brave-browser")
	missing := filepath.Join(dir, "missing")
	cft := filepath.Join(dir, "cft", "chrome")

	none := func() defaultBrowser { return defaultBrowser{} }
	braveDef := func() defaultBrowser { return defaultBrowser{Name: "Brave Browser", Exe: brave, Chromium: true} }
	safari := func() defaultBrowser { return defaultBrowser{Name: "Safari"} }
	braveGone := func() defaultBrowser { return defaultBrowser{Name: "Brave Browser", Exe: missing, Chromium: true} }
	mustNotCall := func() defaultBrowser {
		t.Error("default browser detected despite explicit choice")
		return defaultBrowser{}
	}

	type tc struct {
		path, env string
		def       func() defaultBrowser
		cands     []string
		wantExe   string
		wantCfT   bool
		wantNote  string
	}
	for name, c := range map[string]tc{
		"explicit beats env":          {explicit, env, mustNotCall, []string{installed}, explicit, false, ""},
		"env beats default":           {"", env, mustNotCall, []string{installed}, env, false, ""},
		"missing explicit falls back": {missing, "", none, []string{installed}, installed, false, ""},
		"bundled path forces CfT":     {"bundled", env, mustNotCall, []string{installed}, cft, true, ""},
		"bundled env forces CfT":      {"", "BUNDLED", mustNotCall, []string{installed}, cft, true, ""},
		"explicit beats bundled env":  {explicit, "bundled", mustNotCall, []string{installed}, explicit, false, ""},
		"chromium default first":      {"", "", braveDef, []string{installed}, brave, false, ""},
		"safari default falls through": {
			"", "", safari,
			[]string{missing, installed},
			installed, false,
			"Your default browser (Safari) can't be driven; using installed in a separate profile.",
		},
		"default exe missing": {
			"", "", braveGone,
			[]string{installed},
			installed, false,
			"Your default browser (Brave Browser) can't be driven; using installed in a separate profile.",
		},
		"installed beats CfT": {"", "", none, []string{missing, installed}, installed, false, ""},
		"nothing installed → CfT": {
			"", "", none,
			[]string{missing},
			cft, true,
			"No Chromium-based browser found; using Chrome for Testing " + cftVersion + " in a separate profile.",
		},
		"safari and nothing → CfT": {
			"", "", safari, nil, cft, true,
			"Your default browser (Safari) can't be driven; using Chrome for Testing " + cftVersion + " in a separate profile.",
		},
	} {
		p, _ := locate(c.path, c.env, c.def, c.cands, nil, cft)
		if p.exe != c.wantExe || p.cft != c.wantCfT || p.note != c.wantNote {
			t.Errorf("%s: locate = %+v; want %q,%v,%q", name, p, c.wantExe, c.wantCfT, c.wantNote)
		}
	}
	_, tried := locate(missing, "", none, []string{missing}, nil, cft)
	if len(tried) != 2 {
		t.Errorf("tried = %v", tried)
	}
	// A rejected candidate is skipped exactly like a missing one; explicit
	// choices are never vetted.
	reject := func(string) (string, bool) { return "", false }
	if p, _ := locate("", "", none, []string{installed}, reject, cft); !p.cft {
		t.Errorf("vetted-out candidate used: %+v", p)
	}
	if p, _ := locate(explicit, "", mustNotCall, []string{installed}, reject, cft); p.exe != explicit {
		t.Errorf("explicit path vetted: %+v", p)
	}
}

func TestResolveBrowserUsesExtractedCfT(t *testing.T) {
	plat := cftPlatform(runtime.GOOS, runtime.GOARCH)
	if plat == "" {
		t.Skip("no CfT build for this platform")
	}
	bin := t.TempDir()
	exe := cftExe(bin, plat)
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	// A runnable stand-in, so the Linux `--version` probe passes.
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	orig := readSysctl
	readSysctl = func(string) (string, error) { return "0\n", nil }
	t.Cleanup(func() { readSysctl = orig })
	t.Setenv(EnvBrowser, Bundled)
	got, name, _, err := resolveBrowser(t.Context(), "", bin, nil)
	if err != nil || got != exe || name != "Chrome for Testing" {
		t.Fatalf("resolveBrowser = %q, %q, %v; want %q", got, name, err, exe)
	}
}

func TestAppBundle(t *testing.T) {
	cases := map[string]string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome":                               "/Applications/Google Chrome.app",
		"/s/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing": "/s/chrome-mac-arm64/Google Chrome for Testing.app",
		"/usr/bin/chromium": "",
	}
	for in, want := range cases {
		if got := appBundle(in); got != want {
			t.Errorf("appBundle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDevToolsActivePort(t *testing.T) {
	ws, ok := parseDevToolsActivePort([]byte("53122\n/devtools/browser/0b1c-22\n"))
	if !ok || ws != "ws://127.0.0.1:53122/devtools/browser/0b1c-22" {
		t.Fatalf("got %q %v", ws, ok)
	}
	for _, bad := range []string{"", "53122\n", "abc\n/devtools/browser/x", "0\n/devtools/browser/x", "1\n/other"} {
		if _, ok := parseDevToolsActivePort([]byte(bad)); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}
