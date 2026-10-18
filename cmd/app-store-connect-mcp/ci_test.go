package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/idoo/app-store-connect-mcp/internal/config"
)

func TestServeAllowOrigin(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		env     string
		want    []string
		wantErr string
	}{
		{"none", nil, "", nil, ""},
		{"flag without allow-remote", []string{"--allow-origin", "https://a.example"}, "", nil, "need --allow-remote"},
		{"env without allow-remote", nil, "https://a.example", nil, "need --allow-remote"},
		{"flags and env", []string{"--allow-remote", "--allow-origin", "https://a.example", "--allow-origin=http://b.example:8080"}, " https://c.example ,", []string{"https://a.example", "http://b.example:8080", "https://c.example"}, ""},
		{"path rejected", []string{"--allow-remote", "--allow-origin", "https://a.example/x"}, "", nil, "exact origin"},
		{"scheme rejected", []string{"--allow-remote", "--allow-origin", "ftp://a.example"}, "", nil, "exact origin"},
		{"userinfo rejected", []string{"--allow-remote", "--allow-origin", "https://u@a.example"}, "", nil, "exact origin"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envHTTPAllowedOrigins, tt.env)
			sa, err := parseServeArgs(tt.args, &strings.Builder{})
			if tt.wantErr != "" {
				var ue *usageError
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || errors.As(err, &ue) {
					t.Fatalf("err = %v, want a non-usage error (exit 1) containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(sa.http.AllowedOrigins, tt.want) {
				t.Errorf("AllowedOrigins = %q, want %q", sa.http.AllowedOrigins, tt.want)
			}
		})
	}
}

func TestDoctorEmptyInlineKeyFails(t *testing.T) {
	home, _ := sandbox(t)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv(config.EnvKeyID, "ABC123")
	t.Setenv(config.EnvIssuerID, testIssuer)
	t.Setenv(config.EnvKey, "")
	out, _, err := runCLI(t, "", "doctor")
	if err == nil {
		t.Fatal("doctor succeeded with an empty ASC_PRIVATE_KEY")
	}
	if !strings.Contains(out, "FAIL  profile \"env\"") || !strings.Contains(out, config.ProblemEmptyKey) {
		t.Errorf("doctor output lacks the profile FAIL line:\n%s", out)
	}
	if !strings.Contains(out, "(set "+config.EnvVendorNumber+")") {
		t.Errorf("doctor vendor hint for the env profile should name %s:\n%s", config.EnvVendorNumber, out)
	}
}

func TestDoctorStateDirLine(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs POSIX directory permissions as a non-root user")
	}
	home, _ := sandbox(t)
	ro := filepath.Join(home, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	t.Setenv("XDG_STATE_HOME", ro)
	t.Setenv("LOCALAPPDATA", ro)
	out, _, _ := runCLI(t, "", "doctor")
	if !strings.Contains(out, "state dir "+filepath.Join(ro, "app-store-connect-mcp")+": not writable") {
		t.Errorf("doctor output lacks the state dir line:\n%s", out)
	}
}

func TestDoctorJSONGolden(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wd, "testdata", "doctor", "json.golden")
	home, _ := sandbox(t)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("ASC_MCP_ROOT", "")
	t.Setenv(config.EnvCI, "")
	t.Setenv(config.EnvAllowNoElicit, "")
	out, _, err := runCLI(t, "", "doctor", "--json")
	if err == nil {
		t.Error("doctor --json without credentials should exit non-zero")
	}
	// JSON escapes Windows backslashes, so match the encoded home path too.
	escHome, _ := json.Marshal(home)
	got := strings.ReplaceAll(out, strings.Trim(string(escHome), `"`), "$HOME")
	got = strings.ReplaceAll(got, home, "$HOME")
	got = strings.ReplaceAll(got, buildVersion(), "$VERSION")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixed test path
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if diff := cmp.Diff(string(want), got); diff != "" {
		t.Errorf("doctor --json mismatch (-want +got):\n%s", diff)
	}
}
