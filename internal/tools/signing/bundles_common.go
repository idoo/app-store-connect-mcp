package signing

// Shared helpers of the bundle ID, capability and certificate tools
// (bundles.go, certificates.go).

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Enums of App Store Connect spec 4.5.1.
var (
	// platforms is BundleIdPlatform.
	platforms = []any{"IOS", "MAC_OS", "UNIVERSAL"}
	// capabilityTypes is CapabilityType.
	capabilityTypes = []any{
		"ICLOUD", "IN_APP_PURCHASE", "GAME_CENTER", "PUSH_NOTIFICATIONS", "WALLET", "INTER_APP_AUDIO", "MAPS",
		"ASSOCIATED_DOMAINS", "PERSONAL_VPN", "APP_GROUPS", "HEALTHKIT", "HOMEKIT", "WIRELESS_ACCESSORY_CONFIGURATION",
		"APPLE_PAY", "DATA_PROTECTION", "SIRIKIT", "NETWORK_EXTENSIONS", "MULTIPATH", "HOT_SPOT", "NFC_TAG_READING",
		"CLASSKIT", "AUTOFILL_CREDENTIAL_PROVIDER", "ACCESS_WIFI_INFORMATION", "NETWORK_CUSTOM_PROTOCOL",
		"COREMEDIA_HLS_LOW_LATENCY", "SYSTEM_EXTENSION_INSTALL", "USER_MANAGEMENT", "APPLE_ID_AUTH",
	}
	// settingKeys is CapabilitySetting.key.
	settingKeys = []any{"ICLOUD_VERSION", "DATA_PROTECTION_PERMISSION_LEVEL", "APPLE_ID_AUTH_APP_CONSENT"}
	// optionKeys is CapabilityOption.key.
	optionKeys = []any{
		"XCODE_5", "XCODE_6", "COMPLETE_PROTECTION", "PROTECTED_UNLESS_OPEN",
		"PROTECTED_UNTIL_FIRST_USER_AUTH", "PRIMARY_APP_CONSENT",
	}
	// certificateTypes is CertificateType.
	certificateTypes = []any{
		"APPLE_PAY", "APPLE_PAY_MERCHANT_IDENTITY", "APPLE_PAY_PSP_IDENTITY", "APPLE_PAY_RSA", "DEVELOPER_ID_KEXT",
		"DEVELOPER_ID_KEXT_G2", "DEVELOPER_ID_APPLICATION", "DEVELOPER_ID_APPLICATION_G2", "DEVELOPMENT", "DISTRIBUTION",
		"IDENTITY_ACCESS", "IOS_DEVELOPMENT", "IOS_DISTRIBUTION", "MAC_APP_DISTRIBUTION", "MAC_INSTALLER_DISTRIBUTION",
		"MAC_APP_DEVELOPMENT", "PASS_TYPE_ID", "PASS_TYPE_ID_WITH_NFC",
	}
)

// Sparse fieldsets. bundleIdCapabilities must be selected on bundleIds for
// the relationship (and thus the include) to be returned.
var (
	bundleFields = []string{"identifier", "name", "platform", "seedId", "bundleIdCapabilities"}
	capFields    = []string{"capabilityType", "settings"}
)

// capIncludeLimit is the spec maximum of limit[bundleIdCapabilities];
// Apple's default would silently cut the included capabilities short.
const capIncludeLimit = "50"

// settingOut is a capability setting reduced to its enabled options.
type settingOut struct {
	Key     string   `json:"key"`
	Enabled []string `json:"enabled,omitempty"`
}

// capOut is one enabled capability of a bundle ID.
type capOut struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Settings []settingOut `json:"settings,omitempty"`
}

// bundleOut is a bundle ID with its capabilities.
type bundleOut struct {
	ID           string         `json:"id"`
	Identifier   string         `json:"identifier"`
	Name         tool.Untrusted `json:"name"`
	Platform     string         `json:"platform"`
	SeedID       string         `json:"seed_id,omitempty"`
	Capabilities []capOut       `json:"capabilities"`
}

// parseSettings decodes the settings attribute of a capability (an array of
// CapabilitySetting) into a map of setting key → option key → enabled.
func parseSettings(v any) map[string]map[string]bool {
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

// flattenCap flattens one bundleIdCapabilities resource.
func flattenCap(r tool.Resource) capOut {
	c := capOut{ID: r.ID, Type: r.Str("capabilityType")}
	settings := parseSettings(r.Attributes["settings"])
	for _, key := range slices.Sorted(maps.Keys(settings)) {
		s := settingOut{Key: key}
		for _, opt := range slices.Sorted(maps.Keys(settings[key])) {
			if settings[key][opt] {
				s.Enabled = append(s.Enabled, opt)
			}
		}
		c.Settings = append(c.Settings, s)
	}
	return c
}

// flattenBundle flattens a bundleIds resource with its included capabilities,
// sorted by capability type.
func flattenBundle(r tool.Resource, inc map[string]tool.Resource) bundleOut {
	b := bundleOut{
		ID: r.ID, Identifier: r.Str("identifier"), Name: r.Text("name"),
		Platform: r.Str("platform"), SeedID: r.Str("seedId"), Capabilities: []capOut{},
	}
	for _, id := range r.RelIDs("bundleIdCapabilities") {
		if c, ok := inc["bundleIdCapabilities/"+id]; ok {
			b.Capabilities = append(b.Capabilities, flattenCap(c))
		}
	}
	slices.SortFunc(b.Capabilities, func(x, y capOut) int { return strings.Compare(x.Type, y.Type) })
	return b
}

// bundleQuery is the query reading bundle IDs with their capabilities.
func bundleQuery() url.Values {
	return url.Values{
		"fields[bundleIds]":            {strings.Join(bundleFields, ",")},
		"fields[bundleIdCapabilities]": {strings.Join(capFields, ",")},
		"include":                      {"bundleIdCapabilities"},
		"limit[bundleIdCapabilities]":  {capIncludeLimit},
	}
}

// isIdentifier reports whether ref is a bundle identifier (reverse DNS
// or wildcard) rather than an App Store Connect resource id.
func isIdentifier(ref string) bool { return strings.ContainsAny(ref, ".*") }

// findIdentifier returns the bundle ID registered for exactly identifier
// (Apple's filter is not an exact match), with capabilities.
func findIdentifier(ctx context.Context, env *tool.Env, identifier string) (bundleOut, bool, error) {
	q := bundleQuery()
	q.Set("filter[identifier]", identifier)
	res, err := env.ASC.List(ctx, "/v1/bundleIds", tool.ListOptions{Query: q, MaxItems: 200})
	if err != nil {
		return bundleOut{}, false, fmt.Errorf("find bundle ID %s: %w", identifier, err)
	}
	inc := tool.IndexIncluded(res.Included)
	for _, r := range res.Data {
		if strings.EqualFold(r.Str("identifier"), identifier) {
			return flattenBundle(r, inc), true, nil
		}
	}
	return bundleOut{}, false, nil
}

// lookupBundle resolves a bundle identifier or resource id to the bundle
// ID with its capabilities.
func lookupBundle(ctx context.Context, env *tool.Env, ref string) (bundleOut, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return bundleOut{}, tool.InvalidArgs("bundle_id is required")
	}
	notFound := &tool.ErrNotFound{
		Kind: "bundle ID", Ref: ref,
		Hint: "pass a registered bundle identifier (e.g. com.example.app) or its id",
		Next: []tool.NextStep{{Tool: "signing_bundle_ids_list"}},
	}
	if isIdentifier(ref) {
		b, ok, err := findIdentifier(ctx, env, ref)
		if err != nil {
			return bundleOut{}, err
		}
		if !ok {
			return bundleOut{}, notFound
		}
		return b, nil
	}
	resp, err := env.ASC.Get(ctx, "/v1/bundleIds/"+url.PathEscape(ref), bundleQuery())
	if tool.APIStatus(err) == http.StatusNotFound {
		return bundleOut{}, notFound
	}
	if err != nil {
		return bundleOut{}, fmt.Errorf("read bundle ID %s: %w", ref, err)
	}
	r, ok := resp.First()
	if !ok {
		return bundleOut{}, notFound
	}
	return flattenBundle(r, tool.IndexIncluded(resp.Included)), nil
}

// checkEnum validates v against an enum list.
func checkEnum(arg, v string, enum []any) error {
	if slices.Contains(enum, any(v)) {
		return nil
	}
	return tool.InvalidArgs("%s %q is not one of %v", arg, v, enum)
}
