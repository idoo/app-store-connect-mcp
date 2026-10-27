package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestWritePreferencesDisablesPasswordManager: the profile must never offer
// to store the Apple ID password.
func TestWritePreferencesDisablesPasswordManager(t *testing.T) {
	profile := t.TempDir()
	path := filepath.Join(profile, "Default", "Preferences")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// An existing profile where the user had re-enabled saving.
	old := `{"credentials_enable_service":true,"profile":{"password_manager_enabled":true,"name":"keep"},"autofill":{"profile_enabled":true}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePreferences(profile); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path) //nolint:gosec // test fixture
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Service  *bool `json:"credentials_enable_service"`
		AutoSign *bool `json:"credentials_enable_autosignin"`
		Profile  struct {
			PM   *bool  `json:"password_manager_enabled"`
			Name string `json:"name"`
		} `json:"profile"`
		Autofill struct {
			Enabled *bool `json:"enabled"`
			Profile *bool `json:"profile_enabled"`
			Card    *bool `json:"credit_card_enabled"`
		} `json:"autofill"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]*bool{
		"credentials_enable_service": p.Service, "credentials_enable_autosignin": p.AutoSign,
		"password_manager_enabled": p.Profile.PM, "autofill.enabled": p.Autofill.Enabled,
		"autofill.profile_enabled": p.Autofill.Profile, "autofill.credit_card_enabled": p.Autofill.Card,
	} {
		if v == nil || *v {
			t.Errorf("%s not forced off: %s", name, b)
		}
	}
	if p.Profile.Name != "keep" {
		t.Errorf("unrelated setting lost: %s", b)
	}

	// Fresh profile: file created before first launch.
	fresh := t.TempDir()
	if err := writePreferences(fresh); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(fresh, "Default", "Preferences")); err != nil || (fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/') {
		t.Errorf("fresh Preferences: %v %v", fi, err)
	}
}
