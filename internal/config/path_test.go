package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExpandHome(t *testing.T) {
	env := envMap(map[string]string{"HOME": "/h"})
	tests := map[string]string{
		"~":         "/h",
		"~/k/x.p8":  filepath.Join("/h", "k", "x.p8"),
		"/abs/x.p8": "/abs/x.p8",
		"rel/x.p8":  "rel/x.p8",
		"~bob/x.p8": "~bob/x.p8",
		"/a/~/x.p8": "/a/~/x.p8",
		"":          "",
	}
	for in, want := range tests {
		if got := ExpandHome(in, env); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveExpandsTildeKeyPaths(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, ".config", "app-store-connect-mcp", "config.toml")
	writeFile(t, cfgPath, "[profiles.a]\nkey_id = \"K1\"\nissuer_id = \"I\"\nkey_path = \"~/keys/AuthKey_K1.p8\"\n", 0o600)
	want := filepath.Join(home, "keys", "AuthKey_K1.p8")

	r, err := Resolve(Options{Getenv: envMap(map[string]string{"HOME": home}), ConfigPath: cfgPath, Cwd: home, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if r.Profile.Key.Path != want {
		t.Errorf("config key_path = %q, want %q", r.Profile.Key.Path, want)
	}

	env := envMap(map[string]string{"HOME": home, EnvKeyID: "K2", EnvIssuerID: "I", EnvKeyPath: "~/keys/AuthKey_K2.p8"})
	r, err = Resolve(Options{Getenv: env, ConfigPath: cfgPath, Cwd: home, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "keys", "AuthKey_K2.p8"); r.Profile.Key.Path != want {
		t.Errorf("env key path = %q, want %q", r.Profile.Key.Path, want)
	}
}

func TestEnvProfileUsesConfigEnvSettings(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "app-store-connect-mcp", "config.toml")
	writeFile(t, cfgPath, "[env]\nvendor_number = \"8800\"\ndefault_app = \"com.x\"\n", 0o600)
	base := map[string]string{EnvKeyID: "K1", EnvIssuerID: "I", EnvKeyPath: "/k.p8"}

	r, err := Resolve(Options{Getenv: envMap(base), ConfigPath: cfgPath, Cwd: home, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if r.Profile.Name != EnvProfileName || r.Profile.VendorNumber != "8800" || r.Profile.DefaultApp != "com.x" {
		t.Fatalf("profile = %+v", r.Profile)
	}
	// The variable still wins over the stored setting.
	r, err = Resolve(Options{Getenv: envMap(merge(base, map[string]string{EnvVendorNumber: "1"})), ConfigPath: cfgPath, Cwd: home, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if r.Profile.VendorNumber != "1" {
		t.Fatalf("vendor = %q, want env value", r.Profile.VendorNumber)
	}
}

func TestLoadParseErrorHasPosition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, path, "default_profile = \"a\"\nkey =\n", 0o600)
	_, _, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v, want line 2", err)
	}
	if n := strings.Count(err.Error(), path); n != 1 {
		t.Errorf("path appears %d times in %q", n, err)
	}
}

func TestLoadRefusesWritableDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := filepath.Join(t.TempDir(), "app-store-connect-mcp")
	path := filepath.Join(dir, "config.toml")
	writeFile(t, path, "", 0o600)
	if err := os.Chmod(dir, 0o777); err != nil { //nolint:gosec // the test needs a world-writable dir.
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "chmod 700") {
		t.Fatalf("err = %v, want refusal", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}
