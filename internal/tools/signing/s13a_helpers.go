package signing

// Shared helpers of the bundle ID, capability and certificate tools
// (bundles.go, certificates.go).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// Enums of App Store Connect spec 4.5.1.
var (
	// s13aPlatforms is BundleIdPlatform.
	s13aPlatforms = []any{"IOS", "MAC_OS", "UNIVERSAL"}
	// s13aCapabilityTypes is CapabilityType.
	s13aCapabilityTypes = []any{
		"ICLOUD", "IN_APP_PURCHASE", "GAME_CENTER", "PUSH_NOTIFICATIONS", "WALLET", "INTER_APP_AUDIO", "MAPS",
		"ASSOCIATED_DOMAINS", "PERSONAL_VPN", "APP_GROUPS", "HEALTHKIT", "HOMEKIT", "WIRELESS_ACCESSORY_CONFIGURATION",
		"APPLE_PAY", "DATA_PROTECTION", "SIRIKIT", "NETWORK_EXTENSIONS", "MULTIPATH", "HOT_SPOT", "NFC_TAG_READING",
		"CLASSKIT", "AUTOFILL_CREDENTIAL_PROVIDER", "ACCESS_WIFI_INFORMATION", "NETWORK_CUSTOM_PROTOCOL",
		"COREMEDIA_HLS_LOW_LATENCY", "SYSTEM_EXTENSION_INSTALL", "USER_MANAGEMENT", "APPLE_ID_AUTH",
	}
	// s13aSettingKeys is CapabilitySetting.key.
	s13aSettingKeys = []any{"ICLOUD_VERSION", "DATA_PROTECTION_PERMISSION_LEVEL", "APPLE_ID_AUTH_APP_CONSENT"}
	// s13aOptionKeys is CapabilityOption.key.
	s13aOptionKeys = []any{
		"XCODE_5", "XCODE_6", "COMPLETE_PROTECTION", "PROTECTED_UNLESS_OPEN",
		"PROTECTED_UNTIL_FIRST_USER_AUTH", "PRIMARY_APP_CONSENT",
	}
	// s13aCertificateTypes is CertificateType.
	s13aCertificateTypes = []any{
		"APPLE_PAY", "APPLE_PAY_MERCHANT_IDENTITY", "APPLE_PAY_PSP_IDENTITY", "APPLE_PAY_RSA", "DEVELOPER_ID_KEXT",
		"DEVELOPER_ID_KEXT_G2", "DEVELOPER_ID_APPLICATION", "DEVELOPER_ID_APPLICATION_G2", "DEVELOPMENT", "DISTRIBUTION",
		"IDENTITY_ACCESS", "IOS_DEVELOPMENT", "IOS_DISTRIBUTION", "MAC_APP_DISTRIBUTION", "MAC_INSTALLER_DISTRIBUTION",
		"MAC_APP_DEVELOPMENT", "PASS_TYPE_ID", "PASS_TYPE_ID_WITH_NFC",
	}
)

// Sparse fieldsets. bundleIdCapabilities must be selected on bundleIds for
// the relationship (and thus the include) to be returned.
var (
	s13aBundleFields = []string{"identifier", "name", "platform", "seedId", "bundleIdCapabilities"}
	s13aCapFields    = []string{"capabilityType", "settings"}
)

// s13aCapIncludeLimit is the spec maximum of limit[bundleIdCapabilities];
// Apple's default would silently cut the included capabilities short.
const s13aCapIncludeLimit = "50"

// s13aSettingOut is a capability setting reduced to its enabled options.
type s13aSettingOut struct {
	Key     string   `json:"key"`
	Enabled []string `json:"enabled,omitempty"`
}

// s13aCapOut is one enabled capability of a bundle ID.
type s13aCapOut struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Settings []s13aSettingOut `json:"settings,omitempty"`
}

// s13aBundleOut is a bundle ID with its capabilities.
type s13aBundleOut struct {
	ID           string       `json:"id"`
	Identifier   string       `json:"identifier"`
	Name         string       `json:"name"`
	Platform     string       `json:"platform"`
	SeedID       string       `json:"seed_id,omitempty"`
	Capabilities []s13aCapOut `json:"capabilities"`
}

// s13aInvalid wraps a validation problem as invalid_arguments.
func s13aInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{tool.ErrInvalidArgs}, args...)...)
}

// s13aStatus returns the HTTP status of an App Store Connect error, or 0.
func s13aStatus(err error) int {
	var ae *tool.APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// s13aIncluded indexes included resources by type and id.
func s13aIncluded(inc []tool.Resource) map[string]tool.Resource {
	m := make(map[string]tool.Resource, len(inc))
	for _, r := range inc {
		m[r.Type+"/"+r.ID] = r
	}
	return m
}

// s13aSettings decodes the settings attribute of a capability (an array of
// CapabilitySetting) into a map of setting key → option key → enabled.
func s13aSettings(v any) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	list, _ := v.([]any)
	for _, s := range list {
		sm, _ := s.(map[string]any)
		key, _ := sm["key"].(string)
		if key == "" {
			continue
		}
		opts := map[string]bool{}
		ol, _ := sm["options"].([]any)
		for _, o := range ol {
			om, _ := o.(map[string]any)
			ok, _ := om["key"].(string)
			if ok == "" {
				continue
			}
			en, _ := om["enabled"].(bool)
			opts[ok] = en
		}
		out[key] = opts
	}
	return out
}

// s13aCap flattens one bundleIdCapabilities resource.
func s13aCap(r tool.Resource) s13aCapOut {
	c := s13aCapOut{ID: r.ID, Type: r.Str("capabilityType")}
	settings := s13aSettings(r.Attributes["settings"])
	for _, key := range s13aSortedKeys(settings) {
		s := s13aSettingOut{Key: key}
		for _, opt := range s13aSortedKeys(settings[key]) {
			if settings[key][opt] {
				s.Enabled = append(s.Enabled, opt)
			}
		}
		c.Settings = append(c.Settings, s)
	}
	return c
}

// s13aBundle flattens a bundleIds resource with its included capabilities,
// sorted by capability type.
func s13aBundle(r tool.Resource, inc map[string]tool.Resource) s13aBundleOut {
	b := s13aBundleOut{
		ID: r.ID, Identifier: r.Str("identifier"), Name: r.Str("name"),
		Platform: r.Str("platform"), SeedID: r.Str("seedId"), Capabilities: []s13aCapOut{},
	}
	for _, id := range r.RelIDs("bundleIdCapabilities") {
		if c, ok := inc["bundleIdCapabilities/"+id]; ok {
			b.Capabilities = append(b.Capabilities, s13aCap(c))
		}
	}
	slices.SortFunc(b.Capabilities, func(x, y s13aCapOut) int { return strings.Compare(x.Type, y.Type) })
	return b
}

// s13aBundleQuery is the query reading bundle IDs with their capabilities.
func s13aBundleQuery() url.Values {
	return url.Values{
		"fields[bundleIds]":            {strings.Join(s13aBundleFields, ",")},
		"fields[bundleIdCapabilities]": {strings.Join(s13aCapFields, ",")},
		"include":                      {"bundleIdCapabilities"},
		"limit[bundleIdCapabilities]":  {s13aCapIncludeLimit},
	}
}

// s13aIsIdentifier reports whether ref is a bundle identifier (reverse DNS
// or wildcard) rather than an App Store Connect resource id.
func s13aIsIdentifier(ref string) bool { return strings.ContainsAny(ref, ".*") }

// s13aFindIdentifier returns the bundle ID registered for exactly identifier
// (Apple's filter is not an exact match), with capabilities.
func s13aFindIdentifier(ctx context.Context, env *tool.Env, identifier string) (s13aBundleOut, bool, error) {
	q := s13aBundleQuery()
	q.Set("filter[identifier]", identifier)
	res, err := env.ASC.List(ctx, "/v1/bundleIds", tool.ListOptions{Query: q, MaxItems: 200})
	if err != nil {
		return s13aBundleOut{}, false, fmt.Errorf("find bundle ID %s: %w", identifier, err)
	}
	inc := s13aIncluded(res.Included)
	for _, r := range res.Data {
		if strings.EqualFold(r.Str("identifier"), identifier) {
			return s13aBundle(r, inc), true, nil
		}
	}
	return s13aBundleOut{}, false, nil
}

// s13aLookupBundle resolves a bundle identifier or resource id to the bundle
// ID with its capabilities.
func s13aLookupBundle(ctx context.Context, env *tool.Env, ref string) (s13aBundleOut, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return s13aBundleOut{}, s13aInvalid("bundle_id is required")
	}
	notFound := &tool.ErrNotFound{
		Kind: "bundle ID", Ref: ref,
		Hint: "pass a registered bundle identifier (e.g. com.example.app) or its id",
		Next: []tool.NextStep{{Tool: "signing_bundle_ids_list"}},
	}
	if s13aIsIdentifier(ref) {
		b, ok, err := s13aFindIdentifier(ctx, env, ref)
		if err != nil {
			return s13aBundleOut{}, err
		}
		if !ok {
			return s13aBundleOut{}, notFound
		}
		return b, nil
	}
	resp, err := env.ASC.Get(ctx, "/v1/bundleIds/"+url.PathEscape(ref), s13aBundleQuery())
	if s13aStatus(err) == http.StatusNotFound {
		return s13aBundleOut{}, notFound
	}
	if err != nil {
		return s13aBundleOut{}, fmt.Errorf("read bundle ID %s: %w", ref, err)
	}
	r, ok := resp.First()
	if !ok {
		return s13aBundleOut{}, notFound
	}
	return s13aBundle(r, s13aIncluded(resp.Included)), nil
}

// s13aCheckEnum validates v against an enum list.
func s13aCheckEnum(arg, v string, enum []any) error {
	if slices.Contains(enum, any(v)) {
		return nil
	}
	return s13aInvalid("%s %q is not one of %v", arg, v, enum)
}

// s13aSortedKeys returns the keys of m in order.
func s13aSortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
