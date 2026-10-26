package config

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool"
)

// envMap returns a getenv backed by m.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	const body = "default_profile = \"a\"\n[profiles.a]\nkey_id = \"K1\"\nissuer_id = \"I\"\nkey_path = \"/k.p8\"\n"
	tests := []struct {
		name     string
		mode     os.FileMode
		wantErr  string
		wantWarn bool
	}{
		{"owner only", 0o600, "", false},
		{"readable by others", 0o644, "", true},
		{"group writable", 0o620, "writable by group or others", false},
		{"world writable", 0o602, "writable by group or others", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			writeFile(t, path, body, tt.mode)
			cfg, warns, err := Load(path)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := len(warns) > 0; got != tt.wantWarn {
				t.Fatalf("warnings = %v, want warn=%v", warns, tt.wantWarn)
			}
			if cfg.DefaultProfile != "a" {
				t.Fatalf("default_profile = %q", cfg.DefaultProfile)
			}
		})
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	cfg, warns, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || len(warns) != 0 || len(cfg.Profiles) != 0 {
		t.Fatalf("cfg=%+v warns=%v err=%v", cfg, warns, err)
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"unknown key", "default_profil = \"a\"", "default_profil"},
		{"no key id", "[profiles.a]\nkey_path = \"/k\"", "key_id is required"},
		{"bad kind", "[profiles.a]\nkey_id=\"K\"\nkey_kind=\"org\"\nkey_path=\"/k\"", "key_kind"},
		{"path and keychain", "[profiles.a]\nkey_id=\"K\"\nkey_path=\"/k\"\nkey_source=\"keychain\"", "not both"},
		{"no key", "[profiles.a]\nkey_id=\"K\"", "key_path or key_source"},
		{"individual with issuer", "[profiles.a]\nkey_id=\"K\"\nissuer_id=\"I\"\nkey_kind=\"individual\"\nkey_path=\"/k\"", "issuer_id must be empty"},
		{"undefined default", "default_profile=\"b\"", "not defined"},
		{"relative trusted pin", "trusted_pins=[\"proj\"]", "absolute"},
		{"reserved env name", "[profiles.env]\nkey_id=\"K\"\nkey_path=\"/k\"", "reserved"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.toml")
	in := &Config{
		DefaultProfile: "a",
		TrustedPins:    []string{filepath.Join(dir, "src", "app")},
		Profiles: map[string]Profile{
			"a": {KeyID: "K1", IssuerID: "I", KeyKind: tool.KeyTeam, KeyPath: "/k.p8", AllowWrites: true},
			"b": {KeyID: "K2", KeyKind: tool.KeyIndividual, KeySource: KeySourceKeychain},
		},
	}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %04o, want 0600", info.Mode().Perm())
		}
	}
	out, warns, err := Load(path)
	if err != nil || len(warns) != 0 {
		t.Fatalf("load: %v %v", err, warns)
	}
	if out.Profiles["b"] != in.Profiles["b"] || out.Profiles["a"] != in.Profiles["a"] || !slices.Equal(out.TrustedPins, in.TrustedPins) {
		t.Fatalf("round trip mismatch: %+v", out)
	}
	if err := Save(path, &Config{DefaultProfile: "missing"}); err == nil {
		t.Fatal("Save accepted an invalid config")
	}
}

func TestDefaultPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix layout")
	}
	tests := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"HOME": "/h"}, "/h/.config/hamn/config.toml"},
		{map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "/x"}, "/x/hamn/config.toml"},
		{map[string]string{"HOME": "/h", "XDG_CONFIG_HOME": "rel"}, "/h/.config/hamn/config.toml"},
	}
	for _, tt := range tests {
		got, err := DefaultPath(envMap(tt.env))
		if err != nil || got != tt.want {
			t.Fatalf("DefaultPath(%v) = %q, %v; want %q", tt.env, got, err, tt.want)
		}
	}
}

func TestResolvedKind(t *testing.T) {
	tests := []struct {
		name     string
		p        Profile
		kind     tool.KeyKind
		explicit bool
	}{
		{"issuer means team", Profile{IssuerID: "I"}, tool.KeyTeam, true},
		{"no issuer means individual", Profile{}, tool.KeyIndividual, false},
		{"explicit team", Profile{KeyKind: tool.KeyTeam}, tool.KeyTeam, true},
		{"explicit individual", Profile{KeyKind: tool.KeyIndividual}, tool.KeyIndividual, true},
	}
	for _, tt := range tests {
		if kind, explicit := tt.p.ResolvedKind(); kind != tt.kind || explicit != tt.explicit {
			t.Errorf("%s: ResolvedKind() = %s, %v; want %s, %v", tt.name, kind, explicit, tt.kind, tt.explicit)
		}
	}
}
