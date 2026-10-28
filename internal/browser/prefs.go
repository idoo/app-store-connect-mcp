package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// writePreferences turns off the password manager and autofill in the
// profile before the browser starts, so an Apple ID password is never offered
// for saving into a dedicated profile that only exists to hold the session.
// Existing settings are kept; only these keys are forced.
func writePreferences(profile string) error {
	dir := filepath.Join(profile, "Default")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("browser: create profile: %w", err)
	}
	path := filepath.Join(dir, "Preferences")
	prefs := map[string]any{}
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // fixed file inside our own 0700 profile dir
		_ = json.Unmarshal(b, &prefs) // unreadable: Chrome would reset it too
		if prefs == nil {
			prefs = map[string]any{}
		}
	}
	sub := func(k string) map[string]any {
		m, ok := prefs[k].(map[string]any)
		if !ok {
			m = map[string]any{}
			prefs[k] = m
		}
		return m
	}
	prefs["credentials_enable_service"] = false
	prefs["credentials_enable_autosignin"] = false
	sub("profile")["password_manager_enabled"] = false
	af := sub("autofill")
	af["enabled"] = false
	af["profile_enabled"] = false
	af["credit_card_enabled"] = false
	b, err := json.Marshal(prefs)
	if err != nil {
		return fmt.Errorf("browser: encode preferences: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("browser: write preferences: %w", err)
	}
	return nil
}
