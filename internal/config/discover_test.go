package config

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestDiscover(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "proj")
	altool := filepath.Join(home, ".appstoreconnect", "private_keys")
	writeFile(t, filepath.Join(altool, "AuthKey_ALT111.p8"), testPEM, 0o600)
	writeFile(t, filepath.Join(altool, "AuthKey_FAST22.p8"), testPEM, 0o600)
	writeFile(t, filepath.Join(altool, "notes.txt"), "x", 0o600)
	writeFile(t, filepath.Join(home, ".private_keys", "AuthKey_DOT333.p8"), testPEM, 0o600)
	// fastlane api_key.json with an inline key; the same id also has a .p8.
	fj := filepath.Join(cwd, "fastlane", "api_key.json")
	writeFile(t, fj, `{"key_id":"FAST22","issuer_id":"ISS","key":"k","duration":1200}`, 0o600)
	env := envMap(map[string]string{FastlaneKeyID: "ENV444", FastlaneIssuerID: "ISS2", FastlaneKeyPath: "/e.p8", EnvVendorNumber: "77"})

	got := Discover(home, cwd, env)
	ids := make([]string, len(got))
	for i, c := range got {
		ids[i] = c.Profile.KeyID
	}
	if want := []string{"ALT111", "DOT333", "ENV444", "FAST22"}; !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	byID := map[string]Candidate{}
	for _, c := range got {
		byID[c.Profile.KeyID] = c
	}
	tests := []struct {
		id      string
		key     KeyRef
		issuer  string
		missing []string
		sources int
	}{
		{"ALT111", KeyRef{Source: KeyFromFile, Path: filepath.Join(altool, "AuthKey_ALT111.p8"), KeyID: "ALT111"}, "", []string{"issuer_id or key_kind", "vendor_number"}, 1},
		{"DOT333", KeyRef{Source: KeyFromFile, Path: filepath.Join(home, ".private_keys", "AuthKey_DOT333.p8"), KeyID: "DOT333"}, "", []string{"issuer_id or key_kind", "vendor_number"}, 1},
		{"ENV444", KeyRef{Source: KeyFromFile, Path: "/e.p8", KeyID: "ENV444"}, "ISS2", nil, 1},
		{"FAST22", KeyRef{Source: KeyFromFastlaneJSON, Path: fj, KeyID: "FAST22"}, "ISS", []string{"vendor_number"}, 2},
	}
	for _, tt := range tests {
		c := byID[tt.id]
		if c.Key != tt.key || c.Profile.IssuerID != tt.issuer || !slices.Equal(c.Missing, tt.missing) || len(c.Sources) != tt.sources {
			t.Errorf("%s: got %+v", tt.id, c)
		}
	}
}

func TestDiscoverEmpty(t *testing.T) {
	home := t.TempDir()
	if got := Discover(home, home, envMap(nil)); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}
