package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func TestParseToolsets(t *testing.T) {
	def := tool.DefaultToolsets
	tests := []struct {
		expr    string
		want    []tool.Toolset
		wantErr string
	}{
		{"", def, ""},
		{"default", def, ""},
		{"all", tool.AllToolsets, ""},
		{"+signing", append(slices.Clone(def), tool.Signing), ""},
		{"+signing,-reports", []tool.Toolset{tool.Core, tool.Release, tool.TestFlight, tool.Metadata, tool.Reviews, tool.Screenshots, tool.Signing}, ""},
		{"release testflight", []tool.Toolset{tool.Core, tool.Release, tool.TestFlight}, ""},
		{"all,-admin,-webhooks", []tool.Toolset{tool.Core, tool.Release, tool.TestFlight, tool.Metadata, tool.Reviews, tool.Reports, tool.Screenshots, tool.Uploads, tool.Monetization, tool.Signing, tool.XcodeCloud, tool.Store, tool.Insights, tool.Library}, ""},
		{"STORE", []tool.Toolset{tool.Core, tool.Store}, ""},
		{"-core", nil, "core cannot be disabled"},
		{"bogus", nil, "unknown toolset"},
		{"+", nil, "unknown toolset"},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got, err := ParseToolsets(tt.expr)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestValidateBaseURL(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", true},
		{"https://localhost:9443/", "https://localhost:9443", true},
		{"http://[::1]:1234", "http://[::1]:1234", true},
		{"http://127.5.5.5", "http://127.5.5.5", true},
		{"https://api.appstoreconnect.apple.com", "", false},
		{"https://evil.example", "", false},
		{"http://10.0.0.1", "", false},
		{"ftp://127.0.0.1", "", false},
		{"http://user:pw@127.0.0.1", "", false},
		{"http://127.0.0.1/?x=1", "", false},
		{"http://localhost.evil.com", "", false},
	}
	for _, tt := range tests {
		got, err := ValidateBaseURL(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("ValidateBaseURL(%q) = %q, %v; want %q ok=%v", tt.in, got, err, tt.want, tt.ok)
		}
	}
}

// testPEM is a syntactically PEM-shaped fake key (not a real key).
const testPEM = "-----BEGIN " + "PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQgFAKE\n-----END " + "PRIVATE KEY-----"

func TestKeyRefRead(t *testing.T) {
	dir := t.TempDir()
	p8 := filepath.Join(dir, "AuthKey_ABC123.p8")
	writeFile(t, p8, testPEM+"\n", 0o600)
	fj := filepath.Join(dir, "api_key.json")
	writeFile(t, fj, `{"key_id":"ABC123","issuer_id":"I","key":"`+strings.ReplaceAll(testPEM, "\n", `\n`)+`"}`, 0o600)
	env := envMap(map[string]string{
		"PEM":     testPEM,
		"B64":     base64.StdEncoding.EncodeToString([]byte(testPEM)),
		"ESCAPED": strings.ReplaceAll(testPEM, "\n", `\n`),
		"JUNK":    "not-a-key-SECRETVALUE",
	})
	tests := []struct {
		name string
		ref  KeyRef
		ok   bool
	}{
		{"file", KeyRef{Source: KeyFromFile, Path: p8}, true},
		{"env pem", KeyRef{Source: KeyFromEnv, EnvVar: "PEM"}, true},
		{"env base64", KeyRef{Source: KeyFromEnv, EnvVar: "B64"}, true},
		{"env escaped newlines", KeyRef{Source: KeyFromEnv, EnvVar: "ESCAPED"}, true},
		{"fastlane json", KeyRef{Source: KeyFromFastlaneJSON, Path: fj}, true},
		{"env junk", KeyRef{Source: KeyFromEnv, EnvVar: "JUNK"}, false},
		{"env empty", KeyRef{Source: KeyFromEnv, EnvVar: "NONE"}, false},
		{"missing file", KeyRef{Source: KeyFromFile, Path: filepath.Join(dir, "x.p8")}, false},
		{"keychain", KeyRef{Source: KeyFromKeychain, KeyID: "ABC123"}, false},
		{"none", KeyRef{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.ref.Read(env)
			if !tt.ok {
				if err == nil {
					t.Fatal("want error")
				}
				if strings.Contains(err.Error(), "SECRETVALUE") {
					t.Fatalf("error leaks key material: %v", err)
				}
				return
			}
			if err != nil || string(got) != testPEM {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestFindPin(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "src", "repo")
	deep := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Above the git root: never reached.
	writeFile(t, filepath.Join(home, "src", PinFileName), `profile = "outside"`, 0o600)

	pin, err := FindPin(deep, home)
	if err != nil || pin != nil {
		t.Fatalf("pin above git root was used: %+v %v", pin, err)
	}
	writeFile(t, filepath.Join(repo, PinFileName), "profile = \"p\"\napp = \"com.x\"\nallow_writes = false\n", 0o600)
	pin, err = FindPin(deep, home)
	if err != nil || pin == nil || pin.Profile != "p" || pin.App != "com.x" || pin.AllowWrites == nil || *pin.AllowWrites {
		t.Fatalf("pin = %+v, %v", pin, err)
	}
	if pin.Path != filepath.Join(repo, PinFileName) {
		t.Fatalf("path = %q", pin.Path)
	}

	writeFile(t, filepath.Join(repo, PinFileName), "key_path = \"/k.p8\"\n", 0o600)
	if _, err := FindPin(deep, home); err == nil || !strings.Contains(err.Error(), "only profile, app and allow_writes") {
		t.Fatalf("key material in pin accepted: %v", err)
	}

	// No git: the walk stops at home and never reads above it.
	noGit := filepath.Join(home, "plain", "dir")
	if err := os.MkdirAll(noGit, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(filepath.Dir(home), PinFileName), `profile = "above-home"`, 0o600)
	t.Cleanup(func() { _ = os.Remove(filepath.Join(filepath.Dir(home), PinFileName)) })
	if pin, err := FindPin(noGit, home); err != nil || pin != nil {
		t.Fatalf("walk escaped home: %+v %v", pin, err)
	}
}

func TestFindPinLegacyNames(t *testing.T) {
	cfg := "[profiles.w]\nkey_id = \"K\"\nissuer_id = \"iss\"\nkey_path = \"/k.p8\"\nallow_writes = true\n"
	for _, name := range legacyPinNames {
		t.Run(name, func(t *testing.T) {
			setup := func(t *testing.T) (home, repo string) {
				home = t.TempDir()
				repo = filepath.Join(home, "repo")
				if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(home, "config.toml"), cfg, 0o600)
				return home, repo
			}
			resolve := func(home, repo string) (*Resolved, error) {
				return Resolve(Options{Getenv: envMap(nil), ConfigPath: filepath.Join(home, "config.toml"), Cwd: repo, Home: home})
			}

			t.Run("our schema refuses with the rename", func(t *testing.T) {
				home, repo := setup(t)
				old := filepath.Join(repo, name)
				writeFile(t, old, "allow_writes = false\n", 0o600)
				_, err := resolve(home, repo)
				if err == nil || !strings.Contains(err.Error(), "mv") || !strings.Contains(err.Error(), old) || !strings.Contains(err.Error(), filepath.Join(repo, PinFileName)) {
					t.Fatalf("err = %v", err)
				}
			})
			t.Run("foreign schema is ignored", func(t *testing.T) {
				home, repo := setup(t)
				writeFile(t, filepath.Join(repo, name), "[server]\nx = 1\n", 0o600)
				r, err := resolve(home, repo)
				if err != nil || r.Profile.PinSource != "" || !r.Profile.AllowWrites {
					t.Fatalf("r = %+v err = %v", r, err)
				}
			})
			t.Run("new pin wins over legacy", func(t *testing.T) {
				home, repo := setup(t)
				writeFile(t, filepath.Join(repo, name), "allow_writes = true\n", 0o600)
				writeFile(t, filepath.Join(repo, PinFileName), "allow_writes = false\n", 0o600)
				r, err := resolve(home, repo)
				if err != nil || r.Profile.PinSource != filepath.Join(repo, PinFileName) || r.Profile.AllowWrites {
					t.Fatalf("r = %+v err = %v", r, err)
				}
			})
		})
	}
}
