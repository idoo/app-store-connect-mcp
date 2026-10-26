package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/config"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const testIssuer = "57246542-96fe-1a63-e053-0824d011072a"

// setHome points every per-OS home and config location at home: HOME and
// XDG_CONFIG_HOME on unix; USERPROFILE (os.UserHomeDir) and AppData /
// LocalAppData (config and state dirs) on Windows.
func setHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, ".config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, ".local", "state"))
}

// sandbox isolates HOME, the config dir and ASC_* variables; it returns HOME
// and the config path.
func sandbox(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	setHome(t, home)
	for _, k := range []string{
		config.EnvKeyID, config.EnvIssuerID, config.EnvKeyKind, config.EnvKeyPath, config.EnvKey, config.EnvVendorNumber,
		config.FastlaneKeyID, config.FastlaneIssuerID, config.FastlaneKeyPath, config.FastlaneKey,
		config.EnvProfile, config.EnvBaseURL, "ASC_MCP_ALLOW_WRITES", "ASC_MCP_TOOLSETS",
	} {
		t.Setenv(k, "")
	}
	// Discover scans the cwd for fastlane/api_key.json; use an empty dir.
	t.Chdir(home)
	return home, filepath.Join(home, ".config", "app-store-connect-mcp", "config.toml")
}

// writeKey writes a fresh P-256 .p8 file and returns its path.
func writeKey(t *testing.T, dir, keyID string) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "AuthKey_"+keyID+".p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCLI(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var out, errb strings.Builder
	err := run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	return out.String(), errb.String(), err
}

func TestHelpNeverRunsTheCommand(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--help"}, "-http-max-sessions"},
		{[]string{"-h"}, "Usage:"},
		{[]string{"serve", "--help"}, "-root"},
		{[]string{"auth", "add", "--help"}, "app-store-connect-mcp auth add"},
		{[]string{"auth", "import", "--help"}, "app-store-connect-mcp auth import"},
		{[]string{"auth", "remove", "--help"}, "app-store-connect-mcp auth remove"},
		{[]string{"auth", "test", "--help"}, "app-store-connect-mcp auth test"},
		{[]string{"auth", "set", "--help"}, "app-store-connect-mcp auth set"},
		{[]string{"config", "--help"}, "app-store-connect-mcp config"},
		{[]string{"config", "install", "cursor", "--help"}, "app-store-connect-mcp config"},
		{[]string{"skills", "--help"}, "app-store-connect-mcp skills"},
		{[]string{"skills", "install", "--help"}, "app-store-connect-mcp skills"},
		{[]string{"doctor", "--help"}, "app-store-connect-mcp doctor"},
		{[]string{"version", "--help"}, "usage: app-store-connect-mcp version"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			home, _ := sandbox(t)
			// Stdin would answer every prompt, so a started command would write a profile.
			out, errOut, err := runCLI(t, "1\ndefault\nn\nteam\n"+testIssuer+"\n\n\nn\n", tc.args...)
			if err != nil {
				t.Fatalf("err = %v (stderr %q)", err, errOut)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("stdout lacks %q:\n%s", tc.want, out)
			}
			if strings.Contains(errOut, "Profile name") || strings.Contains(errOut, "Key ID") {
				t.Errorf("prompted: %q", errOut)
			}
			var files []string
			_ = filepath.WalkDir(home, func(p string, d fs.DirEntry, _ error) error {
				if d != nil && !d.IsDir() {
					files = append(files, p)
				}
				return nil
			})
			if len(files) != 0 {
				t.Errorf("help wrote files: %v", files)
			}
		})
	}
}

func TestAuthImportKeyKind(t *testing.T) {
	setup := func(t *testing.T) string {
		home, cfgPath := sandbox(t)
		t.Setenv(config.EnvKeyID, "K1")
		t.Setenv(config.EnvKeyPath, writeKey(t, home, "K1"))
		t.Setenv(config.EnvVendorNumber, "77")
		return cfgPath
	}
	// Answers: candidate, name, keychain=n, then askCommon.
	const head = "1\ndefault\nn\n"

	t.Run("enter on key type is team and needs the issuer", func(t *testing.T) {
		cfgPath := setup(t)
		_, _, err := runCLI(t, head+"\n\n", "auth", "import", "--no-verify")
		if err == nil || !strings.Contains(err.Error(), "issuer id") {
			t.Fatalf("err = %v, want issuer required", err)
		}
		if _, err := os.Stat(cfgPath); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("config written: %v", err)
		}
	})
	t.Run("closed stdin never saves an individual key", func(t *testing.T) {
		cfgPath := setup(t)
		if _, _, err := runCLI(t, head, "auth", "import", "--no-verify"); err == nil {
			t.Fatal("want error on EOF at the key type question")
		}
		if _, err := os.Stat(cfgPath); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("config written: %v", err)
		}
	})
	t.Run("team with issuer keeps env vendor number", func(t *testing.T) {
		cfgPath := setup(t)
		out, _, err := runCLI(t, head+"\n"+testIssuer+"\n\n\nn\n", "auth", "import", "--no-verify")
		if err != nil {
			t.Fatal(err)
		}
		cfg, _, err := config.Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		p := cfg.Profiles["default"]
		if p.KeyKind != tool.KeyTeam || p.IssuerID != testIssuer || p.VendorNumber != "77" {
			t.Fatalf("profile = %+v", p)
		}
		if !strings.Contains(out, "next: app-store-connect-mcp config install") {
			t.Errorf("no next step:\n%s", out)
		}
	})
	t.Run("--kind individual skips the issuer", func(t *testing.T) {
		cfgPath := setup(t)
		if _, _, err := runCLI(t, head+"\n\nn\n", "auth", "import", "--no-verify", "--kind", "individual"); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := config.Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		if p := cfg.Profiles["default"]; p.KeyKind != tool.KeyIndividual || p.IssuerID != "" {
			t.Fatalf("profile = %+v", p)
		}
	})
}

func TestAuthImportVerifiesAgainstApple(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		wantErr string
	}{
		{"ok", http.StatusOK, ""},
		{"401 explains", http.StatusUnauthorized, "key id matches the AuthKey_<ID>.p8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cfgPath := sandbox(t)
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/apps" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					_, _ = w.Write([]byte(`{"data":[]}`))
					return
				}
				_, _ = w.Write([]byte(`{"errors":[{"status":"401","code":"NOT_AUTHORIZED","title":"Authentication credentials are missing or invalid."}]}`))
			}))
			defer srv.Close()
			t.Setenv(config.EnvBaseURL, srv.URL)
			t.Setenv(config.EnvKeyID, "K1")
			t.Setenv(config.EnvKeyPath, writeKey(t, home, "K1"))
			out, _, err := runCLI(t, "1\ndefault\nn\n\n"+testIssuer+"\n9\n\nn\n", "auth", "import")
			if hits.Load() == 0 {
				t.Fatal("Apple was not called")
			}
			if tc.wantErr == "" {
				if err != nil || !strings.Contains(out, "Apple accepted the key") {
					t.Fatalf("err = %v out = %q", err, out)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "is saved") {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if _, err := os.Stat(cfgPath); err != nil {
				t.Fatalf("profile not saved: %v", err)
			}
		})
	}
}

func TestAuthSet(t *testing.T) {
	_, cfgPath := sandbox(t)
	cfg := &config.Config{DefaultProfile: "me", Profiles: map[string]config.Profile{
		"me": {KeyID: "K1", KeyKind: tool.KeyIndividual, KeyPath: "/k.p8"},
	}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	out, _, err := runCLI(t, "", "auth", "set", "issuer_id="+testIssuer)
	if err != nil || !strings.Contains(out, "now team") {
		t.Fatalf("err = %v out = %q", err, out)
	}
	got, _, _ := config.Load(cfgPath)
	if p := got.Profiles["me"]; p.KeyKind != tool.KeyTeam || p.IssuerID != testIssuer {
		t.Fatalf("profile = %+v", p)
	}

	// A no-op set leaves the file alone.
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(cfgPath, past, past); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLI(t, "", "auth", "set", "me", "issuer_id="+testIssuer)
	if err != nil || !strings.Contains(out, "unchanged") {
		t.Fatalf("err = %v out = %q", err, out)
	}
	if fi, _ := os.Stat(cfgPath); !fi.ModTime().Equal(past) {
		t.Error("unchanged set rewrote the config")
	}

	// Env users: the vendor number lands in [env] and the env profile uses it.
	t.Setenv(config.EnvKeyID, "ENV1")
	t.Setenv(config.EnvIssuerID, testIssuer)
	t.Setenv(config.EnvKeyPath, "/e.p8")
	if out, _, err = runCLI(t, "", "auth", "set", "vendor_number=8800"); err != nil {
		t.Fatalf("err = %v out = %q", err, out)
	}
	r, err := config.Resolve(config.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Profile.Name != config.EnvProfileName || r.Profile.VendorNumber != "8800" {
		t.Fatalf("resolved = %+v", r.Profile)
	}
	if _, _, err := runCLI(t, "", "auth", "set", "env", "key_kind=team"); err == nil {
		t.Error("env profile accepted key_kind")
	}
}

func TestStableBinaryPath(t *testing.T) {
	prefix := t.TempDir()
	cellar := filepath.Join(prefix, "Cellar", "app-store-connect-mcp", "0.1.0", "bin", "app-store-connect-mcp")
	if err := os.MkdirAll(filepath.Dir(cellar), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cellar, []byte("bin"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(prefix, "bin", "app-store-connect-mcp")
	if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cellar, link); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	notFound := func(string) (string, error) { return "", exec.ErrNotFound }
	if got := stableBinaryPath(cellar, notFound); got != link {
		t.Errorf("cellar path = %q, want %q", got, link)
	}
	onPath := func(string) (string, error) { return link, nil }
	if got := stableBinaryPath(cellar, onPath); got != link {
		t.Errorf("PATH lookup = %q, want %q", got, link)
	}
	other := filepath.Join(t.TempDir(), "app-store-connect-mcp")
	if err := os.WriteFile(other, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := stableBinaryPath(other, onPath); got != other {
		t.Errorf("unrelated PATH app-store-connect-mcp used: %q", got)
	}
}

func TestCodexTimeouts(t *testing.T) {
	block := codexBlock(fakeBin, "")
	for _, want := range []string{"startup_timeout_sec = 20", "tool_timeout_sec = 330"} {
		if !strings.Contains(block, want) {
			t.Errorf("block lacks %q:\n%s", want, block)
		}
	}
	old := "[mcp_servers.app-store-connect-mcp]\ncommand = \"/old\"\ntool_timeout_sec = 900\n"
	out, err := mergeCodexTOML([]byte(old), block)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "tool_timeout_sec = 900") || strings.Contains(s, "tool_timeout_sec = 330") || !strings.Contains(s, "startup_timeout_sec = 20") {
		t.Errorf("user timeout not kept:\n%s", s)
	}
}

func TestInstallUnchangedIsNoOp(t *testing.T) {
	for _, client := range []string{"cursor", "codex"} {
		t.Run(client, func(t *testing.T) {
			home := t.TempDir()
			var out strings.Builder
			if err := install(client, fakeBin, "", home, &out); err != nil {
				t.Fatal(err)
			}
			path := clientConfigPath(client, home)
			past := time.Now().Add(-time.Hour).Truncate(time.Second)
			if err := os.Chtimes(path, past, past); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			if err := install(client, fakeBin, "", home, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "unchanged") {
				t.Errorf("out = %q", out.String())
			}
			if b, _ := filepath.Glob(path + ".app-store-connect-mcp-backup-*"); len(b) != 0 {
				t.Errorf("backups %v", b)
			}
			if fi, _ := os.Stat(path); !fi.ModTime().Equal(past) {
				t.Error("file rewritten")
			}
		})
	}
}

func TestInstallClaudeCodeFallback(t *testing.T) {
	orig, origTimeout := runClaude, claudeTimeout
	t.Cleanup(func() { runClaude, claudeTimeout = orig, origTimeout })

	runClaude = func(context.Context, []string, io.Writer, io.Writer) error {
		return &exec.Error{Name: "claude", Err: exec.ErrNotFound}
	}
	var out strings.Builder
	if err := install("claude-code", fakeBin, "", t.TempDir(), &out); err != nil {
		t.Fatalf("not found should fall back, got %v", err)
	}
	if !strings.Contains(out.String(), "claude mcp add app-store-connect-mcp -s user -- "+fakeBin) {
		t.Errorf("no command printed: %q", out.String())
	}

	claudeTimeout = 20 * time.Millisecond
	runClaude = func(ctx context.Context, _ []string, _, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	out.Reset()
	err := install("claude-code", fakeBin, "", t.TempDir(), &out)
	if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(out.String(), "claude mcp add") {
		t.Fatalf("err = %v out = %q", err, out.String())
	}
}

func TestServeFlags(t *testing.T) {
	sa, err := parseServeArgs([]string{"--http", "127.0.0.1:1", "--http-session-timeout", "5m", "--http-max-sessions", "3", "--root", "/proj"}, io.Discard)
	if err != nil || sa == nil {
		t.Fatalf("sa = %v err = %v", sa, err)
	}
	if sa.http.SessionTimeout != 5*time.Minute || sa.http.MaxSessions != 3 || sa.root != "/proj" || sa.httpAddr != "127.0.0.1:1" {
		t.Fatalf("parsed = %+v", sa)
	}
	if _, err := parseServeArgs([]string{"--http-session-timeout", "soon"}, io.Discard); err == nil {
		t.Error("bad duration accepted")
	}
}
