package config

import (
	"fmt"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// EnvProfileName is the reserved name of the profile built from ASC_* variables.
const EnvProfileName = "env"

// Environment variable names for single-account credentials. The fastlane
// names are accepted as fallbacks.
const (
	EnvKeyID        = "ASC_KEY_ID"
	EnvIssuerID     = "ASC_ISSUER_ID"
	EnvKeyKind      = "ASC_KEY_KIND"
	EnvKeyPath      = "ASC_PRIVATE_KEY_PATH"
	EnvKey          = "ASC_PRIVATE_KEY"
	EnvVendorNumber = "ASC_VENDOR_NUMBER"

	FastlaneKeyID    = "APP_STORE_CONNECT_API_KEY_KEY_ID"
	FastlaneIssuerID = "APP_STORE_CONNECT_API_KEY_ISSUER_ID"
	FastlaneKeyPath  = "APP_STORE_CONNECT_API_KEY_KEY_FILEPATH"
	FastlaneKey      = "APP_STORE_CONNECT_API_KEY_KEY"
)

// envCreds is the credentials found in the environment.
type envCreds struct {
	Profile  Profile
	Key      KeyRef
	Problems []string
	Warnings []string
}

// first returns the first non-empty variable among names, and its name.
func first(getenv func(string) string, names ...string) (value, name string) {
	for _, n := range names {
		if v := strings.TrimSpace(getenv(n)); v != "" {
			return v, n
		}
	}
	return "", ""
}

// envCredentials reads ASC_* (or fastlane) variables. ok is false when no key
// id is set. An invalid ASC_KEY_KIND is an error.
func envCredentials(getenv func(string) string) (c envCreds, ok bool, err error) {
	keyID, _ := first(getenv, EnvKeyID, FastlaneKeyID)
	if keyID == "" {
		return c, false, nil
	}
	issuer, _ := first(getenv, EnvIssuerID, FastlaneIssuerID)
	kind := tool.KeyKind(strings.ToLower(strings.TrimSpace(getenv(EnvKeyKind))))
	if kind != "" && kind != tool.KeyTeam && kind != tool.KeyIndividual {
		return c, false, fmt.Errorf("%s=%q: must be %q or %q", EnvKeyKind, kind, tool.KeyTeam, tool.KeyIndividual)
	}
	c.Profile = Profile{
		KeyID:        keyID,
		IssuerID:     issuer,
		KeyKind:      kind,
		VendorNumber: strings.TrimSpace(getenv(EnvVendorNumber)),
	}
	path, _ := first(getenv, EnvKeyPath, FastlaneKeyPath)
	path = ExpandHome(path, getenv)
	_, inlineVar := first(getenv, EnvKey, FastlaneKey)
	switch {
	case path != "":
		c.Profile.KeyPath = path
		c.Key = KeyRef{Source: KeyFromFile, Path: path, KeyID: keyID}
	case inlineVar != "":
		c.Key = KeyRef{Source: KeyFromEnv, EnvVar: inlineVar, KeyID: keyID}
		c.Warnings = append(c.Warnings, inlineVar+" holds the private key inline, so it is stored in your MCP client config; prefer "+EnvKeyPath)
	default:
		c.Problems = append(c.Problems, "private key missing: set "+EnvKeyPath+" to your AuthKey_"+keyID+".p8")
		if strings.TrimSpace(getenv("ASC_KEY_PATH")) != "" {
			c.Problems = append(c.Problems, `ASC_KEY_PATH is set but hamn reads ASC_PRIVATE_KEY_PATH; export ASC_PRIVATE_KEY_PATH="$ASC_KEY_PATH" or run hamn auth import`)
		}
	}
	c.Problems = append(c.Problems, kindProblems(c.Profile, EnvIssuerID)...)
	return c, true, nil
}

// kindProblems explains an inferred or inconsistent key kind. fix names the
// setting that holds a team key's issuer id.
func kindProblems(p Profile, fix string) []string {
	switch {
	case p.KeyKind == "" && p.IssuerID == "":
		return []string{"no issuer ID: using an Individual key. Set " + fix + " for a team key."}
	case p.KeyKind == tool.KeyTeam && p.IssuerID == "":
		return []string{"key kind is team but the Issuer ID is missing (Users and Access → Integrations → App Store Connect API)."}
	case p.KeyKind == tool.KeyIndividual && p.IssuerID != "":
		return []string{"key kind is individual but an Issuer ID is set; Individual keys have no issuer."}
	}
	return nil
}
