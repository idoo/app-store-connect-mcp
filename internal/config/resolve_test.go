package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool"
)

type resolveFixture struct {
	home, cfgPath, trusted, untrusted, plain string
}

// newResolveFixture builds a temp HOME with a config and three git projects:
// one listed in trusted_pins, one not, and one without a pin.
func newResolveFixture(t *testing.T) resolveFixture {
	t.Helper()
	home := t.TempDir()
	f := resolveFixture{
		home:      home,
		cfgPath:   filepath.Join(home, ".config", "hamn", "config.toml"),
		trusted:   filepath.Join(home, "trusted"),
		untrusted: filepath.Join(home, "untrusted"),
		plain:     filepath.Join(home, "plain"),
	}
	for _, d := range []string{f.trusted, f.untrusted, f.plain} {
		if err := os.MkdirAll(filepath.Join(d, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, f.cfgPath, fmt.Sprintf(`default_profile = "main"
trusted_pins = [%q]

[profiles.main]
key_id = "MAIN1"
issuer_id = "iss"
key_path = "/k/main.p8"
vendor_number = "111"
allow_writes = true

[profiles.ro]
key_id = "RO1"
issuer_id = "iss"
key_source = "keychain"

[profiles.rw]
key_id = "RW1"
key_kind = "individual"
key_path = "/k/rw.p8"
default_app = "com.rw"
allow_writes = true
allow_no_elicit = true
`, f.trusted), 0o600)
	return f
}

func boolp(b bool) *bool { return &b }

func TestResolvePrecedence(t *testing.T) {
	envCreds := map[string]string{EnvKeyID: "ENV1", EnvIssuerID: "iss", EnvKeyPath: "/k/env.p8"}
	type want struct {
		name, app, warn        string
		writes, pinned, locked bool
	}
	tests := []struct {
		name    string
		flags   Flags
		env     map[string]string
		project string // "trusted", "untrusted" or "" (plain)
		pin     string
		want    want
		wantErr string
	}{
		{name: "config default", want: want{name: "main", writes: true}},
		{name: "env HAMN_PROFILE", env: map[string]string{EnvProfile: "ro"}, want: want{name: "ro"}},
		{name: "flag beats env", flags: Flags{Profile: "rw"}, env: map[string]string{EnvProfile: "ro"}, want: want{name: "rw", app: "com.rw", writes: true}},
		{name: "env credentials beat default", env: envCreds, want: want{name: EnvProfileName}},
		{name: "HAMN_PROFILE beats env credentials", env: merge(envCreds, map[string]string{EnvProfile: "ro"}), want: want{name: "ro"}},
		{name: "env credentials beat pin", env: envCreds, project: "trusted", pin: `profile = "rw"`, want: want{name: EnvProfileName}},
		{name: "untrusted pin selects read-only profile", project: "untrusted", pin: `profile = "ro"`, want: want{name: "ro", pinned: true, locked: true, warn: "chosen by untrusted pin"}},
		{name: "untrusted pin ignored when env enables writes", env: map[string]string{EnvAllowWrites: "1"}, project: "untrusted", pin: `profile = "ro"`, want: want{name: "main", writes: true, warn: `ignoring profile "ro" from untrusted pin`}},
		{name: "untrusted pin ignored when flag enables writes", flags: Flags{AllowWrites: boolp(true)}, project: "untrusted", pin: `profile = "ro"`, want: want{name: "main", writes: true, warn: "writes are on (HAMN_ALLOW_WRITES or --allow-writes)"}},
		{name: "trusted pin with writes on", env: map[string]string{EnvAllowWrites: "1"}, project: "trusted", pin: `profile = "ro"`, want: want{name: "ro", writes: true, pinned: true}},
		{name: "untrusted pin cannot select writable profile", project: "untrusted", pin: `profile = "rw"`, want: want{name: "main", writes: true, warn: "untrusted pin"}},
		{name: "trusted pin selects writable profile", project: "trusted", pin: `profile = "rw"`, want: want{name: "rw", app: "com.rw", writes: true, pinned: true}},
		{name: "pin disables writes", project: "untrusted", pin: "allow_writes = false", want: want{name: "main", pinned: true, locked: true}},
		{name: "pin disables writes over env", env: map[string]string{EnvAllowWrites: "1"}, project: "trusted", pin: "allow_writes = false", want: want{name: "main", pinned: true, locked: true, warn: "writes disabled by pin"}},
		{name: "pin cannot enable writes", env: map[string]string{EnvProfile: "ro"}, project: "trusted", pin: "allow_writes = true", want: want{name: "ro", warn: "can only disable"}},
		{name: "env disables writes", env: map[string]string{EnvAllowWrites: "false"}, want: want{name: "main"}},
		{name: "flag enables writes over env", flags: Flags{AllowWrites: boolp(true)}, env: map[string]string{EnvAllowWrites: "0", EnvProfile: "ro"}, want: want{name: "ro", writes: true}},
		{name: "untrusted pin app ignored for writable profile", project: "untrusted", pin: `app = "com.pin"`, want: want{name: "main", writes: true, warn: "ignoring app"}},
		{name: "untrusted pin app used for read-only profile", env: map[string]string{EnvProfile: "ro"}, project: "untrusted", pin: `app = "com.pin"`, want: want{name: "ro", app: "com.pin", pinned: true}},
		{name: "trusted pin app overrides profile app", flags: Flags{Profile: "rw"}, project: "trusted", pin: `app = "com.pin"`, want: want{name: "rw", app: "com.pin", writes: true, pinned: true}},
		{name: "unknown profile", env: map[string]string{EnvProfile: "nope"}, wantErr: "not defined"},
		{name: "pin names unknown profile", project: "trusted", pin: `profile = "nope"`, wantErr: "not defined"},
		{name: "env profile without credentials", flags: Flags{Profile: EnvProfileName}, wantErr: EnvKeyID},
		{name: "bad bool", env: map[string]string{EnvAllowWrites: "yes please"}, wantErr: EnvAllowWrites},
		{name: "bad key kind", env: merge(envCreds, map[string]string{EnvKeyKind: "org"}), wantErr: EnvKeyKind},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newResolveFixture(t)
			cwd := f.plain
			switch tt.project {
			case "trusted":
				cwd = f.trusted
			case "untrusted":
				cwd = f.untrusted
			}
			if tt.pin != "" {
				writeFile(t, filepath.Join(cwd, PinFileName), tt.pin+"\n", 0o600)
			}
			r, err := Resolve(Options{Flags: tt.flags, Getenv: envMap(tt.env), ConfigPath: f.cfgPath, Cwd: cwd, Home: f.home})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p := r.Profile
			got := want{name: p.Name, app: p.DefaultApp, writes: p.AllowWrites, pinned: p.PinSource != "", locked: r.WritesLocked}
			wantNoWarn := tt.want
			wantNoWarn.warn = ""
			if got != wantNoWarn {
				t.Fatalf("got %+v, want %+v (warnings %v)", got, tt.want, r.Warnings)
			}
			if tt.want.warn != "" && !slices.ContainsFunc(r.Warnings, func(w string) bool { return strings.Contains(w, tt.want.warn) }) {
				t.Fatalf("warnings %v lack %q", r.Warnings, tt.want.warn)
			}
		})
	}
}

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func TestResolveProfileDetails(t *testing.T) {
	f := newResolveFixture(t)
	r, err := Resolve(Options{Flags: Flags{Profile: "rw"}, Getenv: envMap(nil), ConfigPath: f.cfgPath, Cwd: f.plain, Home: f.home})
	if err != nil {
		t.Fatal(err)
	}
	want := tool.ProfileInfo{Name: "rw", KeyKind: tool.KeyIndividual, DefaultApp: "com.rw", AllowWrites: true, AllowNoElicit: true}
	if got := r.Profile.Info(); got != want {
		t.Fatalf("Info() = %+v, want %+v", got, want)
	}
	if r.Profile.Key != (KeyRef{Source: KeyFromFile, Path: "/k/rw.p8", KeyID: "RW1"}) || len(r.Profile.Problems) != 0 {
		t.Fatalf("key=%v problems=%v", r.Profile.Key, r.Profile.Problems)
	}
	r, err = Resolve(Options{Flags: Flags{Profile: "ro"}, Getenv: envMap(nil), ConfigPath: f.cfgPath, Cwd: f.plain, Home: f.home})
	if err != nil {
		t.Fatal(err)
	}
	if r.Profile.Key != (KeyRef{Source: KeyFromKeychain, KeyID: "RO1"}) || r.Profile.KeyKind != tool.KeyTeam {
		t.Fatalf("ro profile = %+v", r.Profile)
	}
}

func TestResolveEnvCredentials(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		kind       tool.KeyKind
		key        KeyRef
		problem    string
		warn       string
		noProblems bool
	}{
		{
			name: "team key", env: map[string]string{EnvKeyID: "K1", EnvIssuerID: "I", EnvKeyPath: "/k.p8", EnvVendorNumber: "9"},
			kind: tool.KeyTeam, key: KeyRef{Source: KeyFromFile, Path: "/k.p8", KeyID: "K1"}, noProblems: true,
		},
		{
			name: "missing issuer and kind", env: map[string]string{EnvKeyID: "K1", EnvKeyPath: "/k.p8"},
			kind: tool.KeyTeam, key: KeyRef{Source: KeyFromFile, Path: "/k.p8", KeyID: "K1"}, problem: "ASC_KEY_KIND=individual",
		},
		{
			name: "explicit individual", env: map[string]string{EnvKeyID: "K1", EnvKeyKind: "Individual", EnvKeyPath: "/k.p8"},
			kind: tool.KeyIndividual, key: KeyRef{Source: KeyFromFile, Path: "/k.p8", KeyID: "K1"}, noProblems: true,
		},
		{
			name: "team kind without issuer", env: map[string]string{EnvKeyID: "K1", EnvKeyKind: "team", EnvKeyPath: "/k.p8"},
			kind: tool.KeyTeam, key: KeyRef{Source: KeyFromFile, Path: "/k.p8", KeyID: "K1"}, problem: "Issuer ID is missing",
		},
		{
			name: "inline key warns", env: map[string]string{EnvKeyID: "K1", EnvIssuerID: "I", EnvKey: testPEM},
			kind: tool.KeyTeam, key: KeyRef{Source: KeyFromEnv, EnvVar: EnvKey, KeyID: "K1"}, warn: "inline", noProblems: true,
		},
		{
			name: "no key", env: map[string]string{EnvKeyID: "K1", EnvIssuerID: "I"},
			kind: tool.KeyTeam, problem: "private key missing",
		},
		{
			name: "ASC_KEY_PATH named", env: map[string]string{EnvKeyID: "K1", EnvIssuerID: "I", "ASC_KEY_PATH": "/k.p8"},
			kind: tool.KeyTeam, problem: `ASC_KEY_PATH is set but hamn reads ASC_PRIVATE_KEY_PATH; export ASC_PRIVATE_KEY_PATH="$ASC_KEY_PATH" or run hamn auth import`,
		},
		{
			name: "fastlane names", env: map[string]string{FastlaneKeyID: "F1", FastlaneIssuerID: "I", FastlaneKeyPath: "/f.p8"},
			kind: tool.KeyTeam, key: KeyRef{Source: KeyFromFile, Path: "/f.p8", KeyID: "F1"}, noProblems: true,
		},
		{
			name: "ASC names win over fastlane", env: map[string]string{EnvKeyID: "A1", FastlaneKeyID: "F1", EnvIssuerID: "I", FastlaneKeyPath: "/f.p8", EnvKeyPath: "/a.p8"},
			kind: tool.KeyTeam, key: KeyRef{Source: KeyFromFile, Path: "/a.p8", KeyID: "A1"}, noProblems: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			r, err := Resolve(Options{Getenv: envMap(tt.env), ConfigPath: filepath.Join(home, "none.toml"), Cwd: home, Home: home})
			if err != nil {
				t.Fatal(err)
			}
			p := r.Profile
			if p.Name != EnvProfileName || p.KeyKind != tt.kind || p.Key != tt.key {
				t.Fatalf("profile = %+v", p)
			}
			if tt.noProblems && len(p.Problems) != 0 {
				t.Fatalf("unexpected problems %v", p.Problems)
			}
			if tt.problem != "" && !slices.ContainsFunc(p.Problems, func(s string) bool { return strings.Contains(s, tt.problem) }) {
				t.Fatalf("problems %v lack %q", p.Problems, tt.problem)
			}
			if tt.warn != "" && !slices.ContainsFunc(r.Warnings, func(s string) bool { return strings.Contains(s, tt.warn) }) {
				t.Fatalf("warnings %v lack %q", r.Warnings, tt.warn)
			}
			for _, s := range []string{fmt.Sprintf("%v", r), fmt.Sprintf("%+v", r), fmt.Sprintf("%#v", r)} {
				if strings.Contains(s, "BEGIN") {
					t.Fatal("resolved config holds key material")
				}
			}
		})
	}
}

func TestResolveNoCredentials(t *testing.T) {
	home := t.TempDir()
	r, err := Resolve(Options{Flags: Flags{AllowWrites: boolp(true)}, Getenv: envMap(nil), ConfigPath: filepath.Join(home, "none.toml"), Cwd: home, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if r.Profile.Name != "" || r.Profile.AllowWrites || len(r.Profile.Problems) != 1 {
		t.Fatalf("profile = %+v", r.Profile)
	}
}

func TestResolveModes(t *testing.T) {
	tests := []struct {
		name    string
		flags   Flags
		env     map[string]string
		check   func(*Resolved) bool
		wantErr string
	}{
		{name: "defaults", check: func(r *Resolved) bool {
			return r.MaxResponseBytes == DefaultMaxResponseBytes && r.MaxRetries == DefaultMaxRetries &&
				r.BaseURL == "" && !r.Discovery && !r.NoConfirm && slices.Equal(r.Toolsets, tool.DefaultToolsets)
		}},
		{name: "env limits", env: map[string]string{EnvMaxResponseBytes: "8192", EnvMaxRetries: "0"}, check: func(r *Resolved) bool {
			return r.MaxResponseBytes == 8192 && r.MaxRetries == 0
		}},
		{name: "response bytes too small", env: map[string]string{EnvMaxResponseBytes: "10"}, wantErr: EnvMaxResponseBytes},
		{name: "retries not a number", env: map[string]string{EnvMaxRetries: "many"}, wantErr: EnvMaxRetries},
		{name: "toolsets env", env: map[string]string{EnvToolsets: "+signing"}, check: func(r *Resolved) bool {
			return slices.Contains(r.Toolsets, tool.Signing)
		}},
		{name: "toolsets flag beats env", flags: Flags{Toolsets: strp("store")}, env: map[string]string{EnvToolsets: "all"}, check: func(r *Resolved) bool {
			return slices.Equal(r.Toolsets, []tool.Toolset{tool.Core, tool.Store})
		}},
		{name: "bad toolset", env: map[string]string{EnvToolsets: "nope"}, wantErr: "unknown toolset"},
		{name: "discovery env", env: map[string]string{EnvDiscovery: "1"}, check: func(r *Resolved) bool { return r.Discovery }},
		{name: "discovery flag beats env", flags: Flags{Discovery: boolp(false)}, env: map[string]string{EnvDiscovery: "1"}, check: func(r *Resolved) bool { return !r.Discovery }},
		{name: "loopback base url", env: map[string]string{EnvBaseURL: "http://127.0.0.1:9/"}, check: func(r *Resolved) bool { return r.BaseURL == "http://127.0.0.1:9" }},
		{name: "remote base url refused", env: map[string]string{EnvBaseURL: "https://example.com"}, wantErr: "not loopback"},
		{name: "no-confirm without CI", flags: Flags{NoConfirm: true}, check: func(r *Resolved) bool { return !r.NoConfirm && len(r.Warnings) == 1 }},
		{name: "no-confirm with CI", flags: Flags{NoConfirm: true}, env: map[string]string{EnvCI: "true"}, check: func(r *Resolved) bool { return r.NoConfirm && r.CI }},
		{name: "CI alone does not skip confirm", env: map[string]string{EnvCI: "1"}, check: func(r *Resolved) bool { return !r.NoConfirm }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			r, err := Resolve(Options{Flags: tt.flags, Getenv: envMap(tt.env), ConfigPath: filepath.Join(home, "none.toml"), Cwd: home, Home: home})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tt.check(r) {
				t.Fatalf("unexpected resolved %+v", r)
			}
		})
	}
}

func strp(s string) *string { return &s }
