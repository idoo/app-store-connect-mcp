package metadata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/metadata"
	"github.com/idoo/app-store-connect-mcp/internal/resolve"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// platformEnum lists the App Store Connect platforms accepted by `platform`.
var platformEnum = []any{string(tool.IOS), string(tool.MacOS), string(tool.TVOS), string(tool.VisionOS)}

// App info and version states.
const (
	infoStateLive    = "READY_FOR_DISTRIBUTION"
	versionStateLive = "READY_FOR_DISTRIBUTION"
)

// infoEditable lists the AppInfo states whose fields Apple accepts writes
// for; WAITING_FOR_REVIEW, IN_REVIEW, PENDING_RELEASE and ACCEPTED are locked.
var infoEditable = []string{"PREPARE_FOR_SUBMISSION", "DEVELOPER_REJECTED", "REJECTED", "READY_FOR_REVIEW"}

// maxShown caps old/new values echoed in plan results.
const maxShown = 160

// appOut is the resolved app echoed in results.
type appOut struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BundleID string `json:"bundle_id,omitempty"`
}

// versionOut is the resolved version echoed in results.
type versionOut struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	State   string `json:"state,omitempty"`
}

// appInfoOut is the app info echoed in results.
type appInfoOut struct {
	ID    string `json:"id"`
	State string `json:"state,omitempty"`
}

// target is a resolved app, platform and version.
type target struct{ resolve.Resolved }

func (t *target) appOut() appOut {
	return appOut{ID: t.App.ID, Name: t.App.Name, BundleID: t.App.BundleID}
}

func (t *target) versionOut() versionOut {
	return versionOut{ID: t.Version.ID, Version: t.Version.String, State: t.Version.State}
}

// resolveTarget resolves app, platform and version. With fallbackLive an
// unspecified version falls back to the live one when nothing is editable.
func resolveTarget(ctx context.Context, env *tool.Env, appRef tool.AppRef, platform tool.Platform, ref tool.VersionRef, fallbackLive bool) (*target, error) {
	r, err := resolve.Target(ctx, env, appRef, platform, ref, resolve.Fallback{Live: fallbackLive, FirstVersion: true})
	if err != nil {
		return nil, err
	}
	return &target{*r}, nil
}

// appInfoSet is the app's app infos by role; any may be nil.
type appInfoSet struct {
	// Editable is the app info writes go to.
	Editable *tool.Resource
	// Live is the app info of the live version.
	Live *tool.Resource
	// Locked is an app info in review or approved (not editable, not live).
	Locked   *tool.Resource
	Included []tool.Resource
}

// lockedState is the state of the locked app info, or "".
func (s appInfoSet) lockedState() string {
	if s.Locked == nil {
		return ""
	}
	return s.Locked.Str("state")
}

// appInfos sorts the app's app infos into editable (explicit allow-list),
// live and locked. include is an optional include= value.
func appInfos(ctx context.Context, env *tool.Env, appID, include string) (appInfoSet, error) {
	q := url.Values{}
	if include != "" {
		q.Set("include", include)
	}
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+appID+"/appInfos", q)
	if err != nil {
		return appInfoSet{}, fmt.Errorf("list app infos: %w", err)
	}
	s := appInfoSet{Included: resp.Included}
	for i := range resp.Data {
		r := &resp.Data[i]
		state := r.Str("state")
		switch {
		case state == infoStateLive:
			if s.Live == nil {
				s.Live = r
			}
		case slices.Contains(infoEditable, state):
			if s.Editable == nil {
				s.Editable = r
			}
		case state != "REPLACED_WITH_NEW_INFO":
			if s.Locked == nil {
				s.Locked = r
			}
		}
	}
	return s, nil
}

// attrStr unwraps a string attribute, including untrusted-wrapped text.
func attrStr(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case tool.Untrusted:
		return string(s)
	case map[string]any:
		if u, ok := s["untrusted"].(string); ok && len(s) == 1 {
			return u
		}
	}
	return ""
}

// truncate shortens s to maxShown runes for display.
func truncate(s string) string {
	if utf8.RuneCountInString(s) <= maxShown {
		return s
	}
	r := []rune(s)
	return string(r[:maxShown]) + "…"
}

// sourceIn is the desired-listing source shared by metadata_plan and
// metadata_apply.
type sourceIn struct {
	App         tool.AppRef     `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform    tool.Platform   `json:"platform,omitempty"`
	Version     tool.VersionRef `json:"version,omitempty" jsonschema:"editable (default), live or a version string"`
	Listing     map[string]any  `json:"listing,omitempty" jsonschema:"{\"locales\":{\"en-US\":{...}}} with name subtitle description keywords whatsNew promotionalText supportUrl marketingUrl privacyPolicyUrl privacyChoicesUrl privacyPolicyText; omitted: unchanged, \"\" clears"`
	FastlaneDir string          `json:"fastlane_dir,omitempty" jsonschema:"fastlane metadata dir in the project"`
	ExpoConfig  string          `json:"expo_config,omitempty" jsonschema:"EAS store.config.json path in the project"`
	Remove      []string        `json:"remove,omitempty" jsonschema:"locales to delete; nothing else is deleted"`
}

// desired reads the desired listing from exactly one source.
func (in sourceIn) desired(env *tool.Env) (*metadata.Listing, string, error) {
	n := 0
	for _, set := range []bool{in.Listing != nil, in.FastlaneDir != "", in.ExpoConfig != ""} {
		if set {
			n++
		}
	}
	if n > 1 || (n == 0 && len(in.Remove) == 0) {
		return nil, "", fmt.Errorf("%w: pass exactly one of listing, fastlane_dir or expo_config", tool.ErrInvalidArgs)
	}
	switch {
	case in.Listing != nil:
		b, err := json.Marshal(in.Listing)
		if err != nil {
			return nil, "", fmt.Errorf("%w: listing: %v", tool.ErrInvalidArgs, err)
		}
		l, err := metadata.FromJSON(b)
		return l, "listing", err
	case in.FastlaneDir != "":
		l, err := metadata.FromFastlane(env.Files, in.FastlaneDir)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %v", tool.ErrInvalidArgs, err)
		}
		return l, "fastlane_dir", nil
	case in.ExpoConfig != "":
		l, err := metadata.FromExpo(env.Files, in.ExpoConfig)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %v", tool.ErrInvalidArgs, err)
		}
		return l, "expo_config", nil
	default:
		return &metadata.Listing{Locales: map[string]*metadata.LocaleFields{}}, "remove", nil
	}
}

// applyKey is the canonical argument set a metadata_apply plan is bound to:
// resolved ids, the desired listing's digest and the removals.
type applyKey struct {
	App       string   `json:"app"`
	Platform  string   `json:"platform"`
	VersionID string   `json:"version_id"`
	AppInfoID string   `json:"app_info_id,omitempty"`
	Desired   string   `json:"desired_sha256"`
	Remove    []string `json:"remove,omitempty"`
}

// planned is a computed listing plan.
type planned struct {
	t         *target
	appInfoID string
	infoState string
	source    string
	remote    *metadata.Listing
	plan      *metadata.Plan
	key       applyKey
	// notes are result warnings, e.g. other platforms with active versions.
	notes []string
}

// activeVersion is an editable or in-flight App Store version.
type activeVersion struct {
	ID, Platform, Version, State string
}

// activeVersionStates are the states of versions that are being prepared or
// are submitted but not yet released.
var activeVersionStates = append(slices.Clone(infoEditable), append([]string{"METADATA_REJECTED", "INVALID_BINARY"}, inFlightLocked...)...)

// activeVersions lists the app's editable and in-flight versions on every
// platform with one GET.
func activeVersions(ctx context.Context, env *tool.Env, appID string) ([]activeVersion, error) {
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+appID+"/appStoreVersions", url.Values{
		"filter[appVersionState]":  {strings.Join(activeVersionStates, ",")},
		"fields[appStoreVersions]": {"platform,versionString,appVersionState"},
		"limit":                    {"10"},
	})
	if err != nil {
		return nil, fmt.Errorf("list active versions: %w", err)
	}
	out := make([]activeVersion, 0, len(resp.Data))
	for _, v := range resp.Data {
		out = append(out, activeVersion{ID: v.ID, Platform: v.Str("platform"), Version: v.Str("versionString"), State: v.Str("appVersionState")})
	}
	return out, nil
}

// otherPlatformNotes warns about active versions on platforms other than
// platform: version text is per platform.
func otherPlatformNotes(vs []activeVersion, platform tool.Platform) []string {
	var notes []string
	for _, v := range vs {
		if v.Platform != "" && v.Platform != string(platform) {
			notes = append(notes, fmt.Sprintf("%s also has %s (%s); version text is per platform; run again with platform", v.Platform, v.Version, v.State))
		}
	}
	return notes
}

// lockBlocker names the in-flight version holding the app-info lock, e.g.
// "MAC_OS 3.6", preferring the other platforms; "" when none is listed.
func lockBlocker(vs []activeVersion, platform tool.Platform) string {
	found := ""
	for _, v := range vs {
		if !slices.Contains(inFlightLocked, v.State) {
			continue
		}
		name := strings.TrimSpace(v.Platform + " " + v.Version)
		if v.Platform != string(platform) {
			return name
		}
		if found == "" {
			found = name
		}
	}
	return found
}

// computePlan pulls the remote listing and diffs the desired one against it.
// The returned plan may carry validation errors. With preview an
// unspecified version falls back to the live one when nothing is editable;
// such a plan is a read-only preview (target.FellBack) and is never proposed.
func computePlan(ctx context.Context, env *tool.Env, in sourceIn, preview bool) (*planned, error) {
	desired, source, err := in.desired(env)
	if err != nil {
		return nil, err
	}
	digest, err := json.Marshal(desired.Locales)
	if err != nil {
		return nil, fmt.Errorf("%w: desired digest: %v", tool.ErrInternal, err)
	}
	sum := sha256.Sum256(digest)

	t, err := resolveTarget(ctx, env, in.App, in.Platform, in.Version, preview)
	if err != nil {
		return nil, err
	}
	infos, err := appInfos(ctx, env, t.App.ID, "")
	if err != nil {
		return nil, err
	}
	p := &planned{t: t, source: source}
	var versions []activeVersion
	if len(t.App.Platforms) > 1 && (in.Platform == "" || (infos.Editable == nil && infos.Locked != nil)) {
		if versions, err = activeVersions(ctx, env, t.App.ID); err != nil {
			p.notes = append(p.notes, "other platforms not checked: "+err.Error())
		}
		if in.Platform == "" {
			p.notes = append(p.notes, otherPlatformNotes(versions, t.Platform)...)
		}
	}
	opts := metadata.DiffOptions{FirstVersion: t.FirstVersion}
	pullInfo := ""
	switch {
	case infos.Editable != nil:
		p.appInfoID, p.infoState = infos.Editable.ID, infos.Editable.Str("state")
		pullInfo = p.appInfoID
	case infos.Locked != nil:
		// Read-only: unchanged app-info fields pass, changes are errors.
		opts.AppInfoLocked = infos.lockedState()
		opts.Blocker = lockBlocker(versions, t.Platform)
		pullInfo = infos.Locked.ID
	case infos.Live != nil:
		opts.AppInfoState = infos.Live.Str("state")
	}
	opts.PromoOnly = !t.FellBack && (t.Version.State == versionStateLive || slices.Contains(inFlightLocked, t.Version.State))
	remote, err := metadata.Pull(ctx, env.ASC, t.Version.ID, pullInfo)
	if err != nil {
		return nil, err
	}
	p.remote = remote

	remove := make([]string, 0, len(in.Remove))
	for _, raw := range in.Remove {
		code, nerr := applemeta.NormalizeLocale(raw)
		if nerr != nil {
			code = raw
		}
		if !slices.Contains(remove, code) {
			remove = append(remove, code)
		}
	}
	slices.Sort(remove)
	opts.Remove = remove
	plan, err := metadata.Diff(remote, desired, opts)
	if err != nil && !errors.Is(err, metadata.ErrInvalidListing) {
		return nil, err
	}
	p.plan = plan
	p.key = applyKey{
		App: t.App.ID, Platform: string(t.Platform), VersionID: t.Version.ID, AppInfoID: p.appInfoID,
		Desired: hex.EncodeToString(sum[:]), Remove: remove,
	}
	if len(p.key.Remove) == 0 {
		p.key.Remove = nil
	}
	return p, nil
}

// fieldOut is one field change shown to the agent.
type fieldOut struct {
	Field string  `json:"field"`
	Old   *string `json:"old,omitempty"`
	New   string  `json:"new"`
}

// changeOut is one localization change shown to the agent.
type changeOut struct {
	Locale string         `json:"locale"`
	Level  metadata.Level `json:"level"`
	Op     metadata.Op    `json:"op"`
	Fields []fieldOut     `json:"fields,omitempty"`
}

// planOut is the result body of metadata_plan and of a metadata_apply preview.
type planOut struct {
	Status   string           `json:"status"`
	App      appOut           `json:"app"`
	Platform tool.Platform    `json:"platform"`
	Version  versionOut       `json:"version"`
	AppInfo  *appInfoOut      `json:"app_info,omitempty"`
	Source   string           `json:"source"`
	Counts   metadata.Counts  `json:"counts"`
	Changes  []changeOut      `json:"changes"`
	Warnings []metadata.Issue `json:"warnings,omitempty"`
	Errors   []metadata.Issue `json:"errors,omitempty"`
	PlanID   string           `json:"plan_id,omitempty"`
	// Applicable is false for a preview against the live version.
	Applicable *bool  `json:"applicable,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Plan statuses.
const (
	statusPlanned   = "planned"
	statusInvalid   = "invalid"
	statusNoChanges = "no_changes"
)

func (p *planned) out() planOut {
	o := planOut{
		App: p.t.appOut(), Platform: p.t.Platform, Version: p.t.versionOut(), Source: p.source,
		Counts: p.plan.Counts, Changes: []changeOut{}, Warnings: p.plan.Warnings, Errors: p.plan.Errors,
	}
	if p.appInfoID != "" {
		o.AppInfo = &appInfoOut{ID: p.appInfoID, State: p.infoState}
	}
	for _, c := range p.plan.Changes {
		co := changeOut{Locale: c.Locale, Level: c.Level, Op: c.Op}
		for _, f := range c.Fields {
			fo := fieldOut{Field: f.Field, New: truncate(f.New)}
			if f.Old != nil {
				old := truncate(*f.Old)
				fo.Old = &old
			}
			co.Fields = append(co.Fields, fo)
		}
		o.Changes = append(o.Changes, co)
	}
	switch {
	case len(p.plan.Errors) > 0:
		o.Status = statusInvalid
	case len(p.plan.Changes) == 0:
		o.Status = statusNoChanges
	default:
		o.Status = statusPlanned
	}
	return o
}

// preview renders the approval preview of the plan.
func (p *planned) preview() tool.PlanPreview {
	c := p.plan.Counts
	pv := tool.PlanPreview{
		Summary: fmt.Sprintf("listing of %s %s %s: %d update(s), %d new locale(s), %d deletion(s)",
			p.t.App.Name, p.t.Platform, p.t.Version.String, c.Updates, c.Creates, c.Deletes),
		Affected: []string{p.t.App.ID, p.t.Version.ID},
	}
	intended := make([]string, 0, len(p.plan.Changes))
	for _, ch := range p.plan.Changes {
		fields := make([]string, 0, len(ch.Fields))
		for _, f := range ch.Fields {
			fields = append(fields, f.Field)
		}
		line := fmt.Sprintf("%s %s %s", ch.Op, ch.Level, ch.Locale)
		if len(fields) > 0 {
			line += ": " + strings.Join(fields, ", ")
		}
		intended = append(intended, line)
		if ch.Op == metadata.OpDelete {
			pv.Irreversible = append(pv.Irreversible, fmt.Sprintf("deletes the %s localization %s and its text", ch.Level, ch.Locale))
		}
	}
	pv.Intended = intended
	return pv
}

// nextArgs echoes the caller's source arguments with resolved ids. An inline
// listing is not echoed: the caller resends the listing it planned (the plan
// is bound to its digest), which keeps big plans out of the result.
func (p *planned) nextArgs(in sourceIn) map[string]any {
	a := map[string]any{"app": p.t.App.ID, "platform": string(p.t.Platform), "version": p.t.Version.String}
	switch {
	case in.FastlaneDir != "":
		a["fastlane_dir"] = in.FastlaneDir
	case in.ExpoConfig != "":
		a["expo_config"] = in.ExpoConfig
	}
	if len(in.Remove) > 0 {
		a["remove"] = in.Remove
	}
	return a
}
