package signing

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "signing_bundle_ids_list", Toolset: tool.Signing, Class: tool.Read, Idempotent: true, NeedsTeamKey: true,
		Title: "List bundle IDs",
		Description: "Registered bundle IDs (identifier, name, platform, seed id) with their enabled capabilities and settings. " +
			"Filter by identifier, platform or a name/identifier substring. Use before signing_capabilities_set or signing_profile_create.",
		Enums: map[string][]any{"platform": platforms},
	}, bundleIDsList))
	tool.Register(tool.New(tool.Meta{
		Name: "signing_bundle_id_create", Toolset: tool.Signing, Class: tool.Write, Idempotent: true, NeedsTeamKey: true,
		Title: "Register a bundle ID",
		Description: "Register a bundle identifier (explicit or wildcard com.example.*) for IOS, MAC_OS or UNIVERSAL. " +
			"Idempotent: an already registered identifier returns already:true with the existing bundle ID; nothing is changed.",
		Enums: map[string][]any{"platform": platforms},
	}, bundleIDCreate))
	tool.Register(tool.New(tool.Meta{
		Name: "signing_capabilities_set", Toolset: tool.Signing, Class: tool.Destructive, NeedsTeamKey: true,
		Confirm: tool.ConfirmByFlag,
		Title:   "Enable or disable bundle ID capabilities",
		Description: "Enable capabilities (with settings) on a bundle ID, or disable them. Enabling missing ones applies at once; " +
			"already enabled ones are skipped. Changing settings needs confirm:true; disabling also needs the user's approval. Both invalidate profiles.",
		Enums: map[string][]any{
			"enable.type":                 capabilityTypes,
			"enable.settings.key":         settingKeys,
			"enable.settings.options.key": optionKeys,
			"disable":                     capabilityTypes,
		},
	}, capabilitiesSet))
}

// --- signing_bundle_ids_list ---

type bundleIDsListIn struct {
	Identifier string `json:"identifier,omitempty" jsonschema:"bundle identifier filter passed to Apple, e.g. com.example.app"`
	Platform   string `json:"platform,omitempty"`
	Query      string `json:"query,omitempty" jsonschema:"case-insensitive substring of the name or identifier"`
	MaxItems   int    `json:"max_items,omitempty" jsonschema:"maximum bundle IDs to read (default 200, max 1000)"`
	Cursor     string `json:"cursor,omitempty" jsonschema:"next_cursor of a previous call"`
}

type bundleIDsListOut struct {
	// SeedID is hoisted here when every listed bundle ID shares it (the
	// usual single-team case); rows then omit it.
	SeedID     string      `json:"seed_id,omitempty"`
	BundleIDs  []bundleRow `json:"bundle_ids"`
	Returned   int         `json:"returned"`
	Total      int         `json:"total,omitempty"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// bundleRow is a compact bundle ID: capabilities by type only (their ids are
// just <bundle id>_<type>), settings only for capabilities that have some.
type bundleRow struct {
	ID           string                  `json:"id"`
	Identifier   string                  `json:"identifier"`
	Name         string                  `json:"name"`
	Platform     string                  `json:"platform"`
	SeedID       string                  `json:"seed_id,omitempty"`
	Capabilities []string                `json:"capabilities"`
	Settings     map[string][]settingOut `json:"settings,omitempty"`
}

func compactBundle(b bundleOut) bundleRow {
	r := bundleRow{
		ID: b.ID, Identifier: b.Identifier, Name: b.Name, Platform: b.Platform, SeedID: b.SeedID,
		Capabilities: make([]string, len(b.Capabilities)),
	}
	for i, c := range b.Capabilities {
		r.Capabilities[i] = c.Type
		if len(c.Settings) > 0 {
			if r.Settings == nil {
				r.Settings = map[string][]settingOut{}
			}
			r.Settings[c.Type] = c.Settings
		}
	}
	return r
}

func bundleIDsList(ctx context.Context, env *tool.Env, in bundleIDsListIn) (*tool.Result, error) {
	if in.MaxItems < 0 || in.MaxItems > 1000 {
		return nil, invalidArgs("max_items must be between 1 and 1000")
	}
	q := bundleQuery()
	q.Set("sort", "identifier")
	if in.Identifier != "" {
		q.Set("filter[identifier]", in.Identifier)
	}
	if in.Platform != "" {
		q.Set("filter[platform]", in.Platform)
	}
	res, err := env.ASC.List(ctx, "/v1/bundleIds", tool.ListOptions{Query: q, MaxItems: in.MaxItems, Cursor: in.Cursor})
	if err != nil {
		return nil, fmt.Errorf("list bundle IDs: %w", err)
	}
	inc := indexIncluded(res.Included)
	needle := strings.ToLower(strings.TrimSpace(in.Query))
	out := bundleIDsListOut{BundleIDs: []bundleRow{}, Total: res.Total, NextCursor: res.NextCursor}
	seeds := map[string]bool{}
	for _, r := range res.Data {
		b := flattenBundle(r, inc)
		if needle != "" && !strings.Contains(strings.ToLower(b.Identifier), needle) && !strings.Contains(strings.ToLower(b.Name), needle) {
			continue
		}
		seeds[b.SeedID] = true
		out.BundleIDs = append(out.BundleIDs, compactBundle(b))
	}
	if len(seeds) == 1 {
		for s := range seeds {
			out.SeedID = s
		}
		for i := range out.BundleIDs {
			out.BundleIDs[i].SeedID = ""
		}
	}
	out.Returned = len(out.BundleIDs)
	return tool.OK(out), nil
}

// --- signing_bundle_id_create ---

type bundleIDCreateIn struct {
	Identifier string `json:"identifier" jsonschema:"reverse-DNS bundle identifier, or a wildcard ending in .*"`
	Name       string `json:"name" jsonschema:"display name in the developer portal"`
	Platform   string `json:"platform"`
}

type bundleIDCreateOut struct {
	BundleID bundleOut `json:"bundle_id"`
	Created  bool      `json:"created"`
}

// identifierRE is Apple's bundle identifier alphabet, optionally a
// trailing wildcard.
var identifierRE = regexp.MustCompile(`^(\*|[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*(\.\*)?)$`)

func bundleIDCreate(ctx context.Context, env *tool.Env, in bundleIDCreateIn) (*tool.Result, error) {
	in.Identifier = strings.TrimSpace(in.Identifier)
	in.Name = strings.TrimSpace(in.Name)
	if !identifierRE.MatchString(in.Identifier) || len(in.Identifier) > 155 {
		return nil, invalidArgs("identifier %q is not a valid bundle identifier", in.Identifier)
	}
	if in.Name == "" {
		return nil, invalidArgs("name is required")
	}
	next := func(b bundleOut) tool.NextStep {
		return tool.NextStep{Tool: "signing_capabilities_set", Args: map[string]any{"bundle_id": b.Identifier}}
	}
	already := func(b bundleOut) *tool.Result {
		res := tool.Already(bundleIDCreateOut{BundleID: b}).Then(next(b))
		if b.Platform != in.Platform {
			res.Warn(fmt.Sprintf("%s is already registered for platform %s, not %s", b.Identifier, b.Platform, in.Platform))
		}
		return res
	}
	if b, ok, err := findIdentifier(ctx, env, in.Identifier); err != nil {
		return nil, err
	} else if ok {
		return already(b), nil
	}
	body := map[string]any{"data": map[string]any{
		"type":       "bundleIds",
		"attributes": map[string]any{"identifier": in.Identifier, "name": in.Name, "platform": in.Platform},
	}}
	resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: "/v1/bundleIds", Body: body, Write: true})
	if apiStatus(err) == http.StatusConflict {
		// Registered concurrently or under another team member: read back.
		b, ok, rerr := findIdentifier(ctx, env, in.Identifier)
		if rerr == nil && ok {
			return already(b), nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("register bundle ID %s: %w", in.Identifier, err)
	}
	r, ok := resp.First()
	if !ok || r.ID == "" {
		return nil, fmt.Errorf("register bundle ID %s: response has no resource: %w", in.Identifier, tool.ErrOutcomeUnknown)
	}
	b := flattenBundle(r, indexIncluded(resp.Included))
	return tool.OK(bundleIDCreateOut{BundleID: b, Created: true}).Then(next(b)), nil
}

// --- signing_capabilities_set ---

type capOptionIn struct {
	Key     string `json:"key"`
	Enabled *bool  `json:"enabled,omitempty" jsonschema:"default true"`
}

type capSettingIn struct {
	Key     string        `json:"key"`
	Options []capOptionIn `json:"options"`
}

type capEnableIn struct {
	Type     string         `json:"type"`
	Settings []capSettingIn `json:"settings,omitempty" jsonschema:"e.g. ICLOUD_VERSION XCODE_6, DATA_PROTECTION_PERMISSION_LEVEL"`
}

type capsSetIn struct {
	tool.ConfirmFlag
	BundleID string        `json:"bundle_id" jsonschema:"bundle identifier (com.example.app) or bundle ID resource id"`
	Enable   []capEnableIn `json:"enable,omitempty"`
	Disable  []string      `json:"disable,omitempty" jsonschema:"capability types to disable (needs confirm)"`
}

// Per-capability outcomes of signing_capabilities_set.
const (
	capEnabled         = "enabled"
	capUpdated         = "settings_updated"
	capDisabled        = "disabled"
	capAlreadyEnabled  = "already_enabled"
	capAlreadyDisabled = "already_disabled"
	capFailed          = "failed"
)

type capItem struct {
	Type   string `json:"type"`
	Action string `json:"action"`
	ID     string `json:"id,omitempty"`
	Error  string `json:"error,omitempty"`
}

type capsSetOut struct {
	BundleID   string    `json:"bundle_id"`
	Identifier string    `json:"identifier"`
	Items      []capItem `json:"items"`
	Failed     int       `json:"failed"`
}

// capChange is one planned mutation.
type capChange struct {
	enable capEnableIn
	cur    capOut // zero for a new capability
	action string // capEnabled, capUpdated or capDisabled
}

// capSettingsBody renders settings as CapabilitySetting objects.
func capSettingsBody(in []capSettingIn) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, s := range in {
		opts := make([]map[string]any, 0, len(s.Options))
		for _, o := range s.Options {
			opts = append(opts, map[string]any{"key": o.Key, "enabled": o.Enabled == nil || *o.Enabled})
		}
		out = append(out, map[string]any{"key": s.Key, "options": opts})
	}
	return out
}

// capSettingsSatisfied reports whether every wanted option already has the
// wanted enabled state on the remote capability.
func capSettingsSatisfied(cur capOut, want []capSettingIn) bool {
	have := map[string][]string{}
	for _, s := range cur.Settings {
		have[s.Key] = s.Enabled
	}
	for _, s := range want {
		enabled, ok := have[s.Key]
		if !ok {
			return false
		}
		for _, o := range s.Options {
			if (o.Enabled == nil || *o.Enabled) != slices.Contains(enabled, o.Key) {
				return false
			}
		}
	}
	return true
}

// capValidate rejects empty, duplicate and contradictory capability lists.
func capValidate(in capsSetIn) error {
	if len(in.Enable) == 0 && len(in.Disable) == 0 {
		return invalidArgs("pass at least one capability in enable or disable")
	}
	seen := map[string]string{}
	for _, e := range in.Enable {
		if err := checkEnum("enable.type", e.Type, capabilityTypes); err != nil {
			return err
		}
		if seen[e.Type] != "" {
			return invalidArgs("capability %s is listed twice", e.Type)
		}
		seen[e.Type] = "enable"
		for _, s := range e.Settings {
			if len(s.Options) == 0 {
				return invalidArgs("setting %s of %s needs at least one option", s.Key, e.Type)
			}
		}
	}
	for _, d := range in.Disable {
		if err := checkEnum("disable", d, capabilityTypes); err != nil {
			return err
		}
		if prev := seen[d]; prev != "" {
			return invalidArgs("capability %s is listed in both enable and disable, or twice", d)
		}
		seen[d] = "disable"
	}
	return nil
}

func capabilitiesSet(ctx context.Context, env *tool.Env, in capsSetIn) (*tool.Result, error) {
	if err := capValidate(in); err != nil {
		return nil, err
	}
	b, err := lookupBundle(ctx, env, in.BundleID)
	if err != nil {
		return nil, err
	}
	current := map[string]capOut{}
	for _, c := range b.Capabilities {
		current[c.Type] = c
	}
	out := capsSetOut{BundleID: b.ID, Identifier: b.Identifier, Items: []capItem{}}
	var changes []capChange
	gated, disabling := false, false
	for _, e := range in.Enable {
		cur, ok := current[e.Type]
		switch {
		case !ok:
			changes = append(changes, capChange{enable: e, action: capEnabled})
		case len(e.Settings) > 0 && !capSettingsSatisfied(cur, e.Settings):
			changes = append(changes, capChange{enable: e, cur: cur, action: capUpdated})
			gated = true
		default:
			out.Items = append(out.Items, capItem{Type: e.Type, Action: capAlreadyEnabled, ID: cur.ID})
		}
	}
	for _, d := range in.Disable {
		cur, ok := current[d]
		if !ok {
			out.Items = append(out.Items, capItem{Type: d, Action: capAlreadyDisabled})
			continue
		}
		changes = append(changes, capChange{enable: capEnableIn{Type: d}, cur: cur, action: capDisabled})
		gated, disabling = true, true
	}
	if len(changes) == 0 {
		return tool.Already(out), nil
	}

	var wctx context.Context
	var res *tool.Result
	switch {
	case disabling:
		// Disabling removes entitlements shipped apps rely on (push, iCloud,
		// App Groups) and invalidates every profile: a human must approve.
		wctx, res = tool.RequireHumanConfirm(ctx, env, in.Confirm, capPreview(b, changes))
	case gated:
		wctx, res = tool.RequireConfirm(ctx, env, in.Confirm, capPreview(b, changes))
	default:
		wctx, res = tool.Unconfirmed(ctx, env)
	}
	if res != nil {
		return res, nil
	}

	for _, ch := range changes {
		item := capApply(wctx, env, b.ID, ch)
		if item.Action == capFailed {
			out.Failed++
		}
		out.Items = append(out.Items, item)
	}
	result := tool.OK(out)
	if out.Failed > 0 {
		result.Warn(fmt.Sprintf("%d capability change(s) failed; re-run signing_bundle_ids_list to read the current state", out.Failed))
	}
	return result, nil
}

// capPreview describes the gated changes for the approval prompt.
func capPreview(b bundleOut, changes []capChange) tool.PlanPreview {
	cur := make([]string, 0, len(b.Capabilities))
	for _, c := range b.Capabilities {
		cur = append(cur, c.Type)
	}
	intended := make([]string, 0, len(changes))
	for _, ch := range changes {
		intended = append(intended, ch.action+" "+ch.enable.Type)
	}
	p := tool.PlanPreview{
		Summary:  fmt.Sprintf("change capabilities of bundle ID %s (%s)", b.Identifier, b.ID),
		Current:  map[string]any{"capabilities": cur},
		Intended: intended,
		Affected: []string{b.Identifier},
		Irreversible: []string{
			"provisioning profiles using this bundle ID become invalid and must be regenerated",
		},
	}
	for _, ch := range changes {
		if ch.action == capDisabled {
			p.Irreversible = append(p.Irreversible, "disabling a capability drops its configuration (e.g. iCloud containers, app groups)")
			break
		}
	}
	return p
}

// capApply performs one capability mutation with a write-capable context.
func capApply(ctx context.Context, env *tool.Env, bundleID string, ch capChange) capItem {
	item := capItem{Type: ch.enable.Type, Action: ch.action, ID: ch.cur.ID}
	var err error
	switch ch.action {
	case capEnabled:
		attrs := map[string]any{"capabilityType": ch.enable.Type}
		if len(ch.enable.Settings) > 0 {
			attrs["settings"] = capSettingsBody(ch.enable.Settings)
		}
		var resp *tool.Response
		resp, err = env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: "/v1/bundleIdCapabilities", Write: true, Body: map[string]any{
			"data": map[string]any{
				"type": "bundleIdCapabilities", "attributes": attrs,
				"relationships": map[string]any{"bundleId": map[string]any{"data": map[string]string{"type": "bundleIds", "id": bundleID}}},
			},
		}})
		if r, ok := resp.First(); err == nil && ok {
			item.ID = r.ID
		}
	case capUpdated:
		_, err = env.ASC.Do(ctx, tool.Request{
			Method: http.MethodPatch, Path: "/v1/bundleIdCapabilities/" + url.PathEscape(ch.cur.ID), Write: true,
			Body: map[string]any{"data": map[string]any{
				"type": "bundleIdCapabilities", "id": ch.cur.ID,
				"attributes": map[string]any{"capabilityType": ch.enable.Type, "settings": capSettingsBody(ch.enable.Settings)},
			}},
		})
	case capDisabled:
		_, err = env.ASC.Do(ctx, tool.Request{Method: http.MethodDelete, Path: "/v1/bundleIdCapabilities/" + url.PathEscape(ch.cur.ID), Write: true})
		if apiStatus(err) == http.StatusNotFound {
			return capItem{Type: ch.enable.Type, Action: capAlreadyDisabled}
		}
	}
	if err != nil {
		b := tool.BodyFor(err)
		item.Action = capFailed
		item.Error = strings.TrimSpace(b.Error + ": " + b.Message)
	}
	return item
}
