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

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/metadata"
	"github.com/idoo/hamn/internal/tool"
)

// platformEnum lists the App Store Connect platforms accepted by `platform`.
var platformEnum = []any{string(tool.IOS), string(tool.MacOS), string(tool.TVOS), string(tool.VisionOS)}

// App info states that are no longer editable.
const (
	infoStateLive     = "READY_FOR_DISTRIBUTION"
	infoStateReplaced = "REPLACED_WITH_NEW_INFO"
	versionStateLive  = "READY_FOR_DISTRIBUTION"
)

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
type target struct {
	App      tool.App
	Platform tool.Platform
	Version  tool.Version
	// FirstVersion is true when the app has never had a live version.
	FirstVersion bool
	// FellBack is true when no version was editable and the live one was used.
	FellBack bool
}

func (t *target) appOut() appOut {
	return appOut{ID: t.App.ID, Name: t.App.Name, BundleID: t.App.BundleID}
}

func (t *target) versionOut() versionOut {
	return versionOut{ID: t.Version.ID, Version: t.Version.String, State: t.Version.State}
}

// resolveTarget resolves app, platform and version. With fallbackLive an
// unspecified version falls back to the live one when nothing is editable.
func resolveTarget(ctx context.Context, env *tool.Env, appRef tool.AppRef, platform tool.Platform, ref tool.VersionRef, fallbackLive bool) (*target, error) {
	app, err := env.Resolve.App(ctx, appRef)
	if err != nil {
		return nil, err
	}
	t := &target{App: app, Platform: app.Platform(platform)}
	v, err := env.Resolve.Version(ctx, app.ID, t.Platform, ref)
	var nf *tool.ErrNotFound
	if err != nil && fallbackLive && (ref == "" || ref == tool.VersionEditable) && errors.As(err, &nf) {
		v, err = env.Resolve.Version(ctx, app.ID, t.Platform, tool.VersionLive)
		t.FellBack = err == nil
	}
	if err != nil {
		return nil, err
	}
	if v.ID == "" {
		return nil, tool.WithNext(&tool.ErrNotFound{Kind: "version", Ref: v.String, Hint: "the version does not exist yet; create it with release_prepare"},
			tool.NextStep{Tool: "release_prepare", Args: map[string]any{"app": app.ID, "platform": string(t.Platform), "version": v.String}})
	}
	t.Version = v
	if v.State != versionStateLive {
		if _, err := env.Resolve.Version(ctx, app.ID, t.Platform, tool.VersionLive); err != nil {
			if !errors.As(err, &nf) {
				return nil, err
			}
			t.FirstVersion = true
		}
	}
	return t, nil
}

// appInfos returns the app's editable app info (not live or replaced) and the
// live one; either may be nil. include is an optional include= value.
func appInfos(ctx context.Context, env *tool.Env, appID, include string) (editable, live *tool.Resource, included []tool.Resource, err error) {
	q := url.Values{}
	if include != "" {
		q.Set("include", include)
	}
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+appID+"/appInfos", q)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list app infos: %w", err)
	}
	for i := range resp.Data {
		r := &resp.Data[i]
		switch r.Str("state") {
		case infoStateLive:
			if live == nil {
				live = r
			}
		case infoStateReplaced:
		default:
			if editable == nil {
				editable = r
			}
		}
	}
	return editable, live, resp.Included, nil
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
	App         tool.AppRef     `json:"app,omitempty" jsonschema:"app id, bundle id, SKU or name; default: pinned app"`
	Platform    tool.Platform   `json:"platform,omitempty" jsonschema:"default: inferred from the app"`
	Version     tool.VersionRef `json:"version,omitempty" jsonschema:"editable (default), live or a version string"`
	Listing     map[string]any  `json:"listing,omitempty" jsonschema:"inline desired listing: {\"locales\":{\"en-US\":{\"description\":\"…\",\"keywords\":\"a,b\"}}}; keys name, subtitle, privacyPolicyUrl, privacyChoicesUrl, privacyPolicyText, description, keywords, marketingUrl, promotionalText, supportUrl, whatsNew; omitted keys stay unchanged, \"\" clears"`
	FastlaneDir string          `json:"fastlane_dir,omitempty" jsonschema:"fastlane deliver metadata directory (<dir>/<locale>/description.txt …), inside the project"`
	ExpoConfig  string          `json:"expo_config,omitempty" jsonschema:"path to EAS Metadata store.config.json, inside the project"`
	Remove      []string        `json:"remove,omitempty" jsonschema:"locales to delete; nothing else is ever deleted"`
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
}

// computePlan pulls the remote listing and diffs the desired one against it.
// The returned plan may carry validation errors.
func computePlan(ctx context.Context, env *tool.Env, in sourceIn) (*planned, error) {
	desired, source, err := in.desired(env)
	if err != nil {
		return nil, err
	}
	digest, err := json.Marshal(desired.Locales)
	if err != nil {
		return nil, fmt.Errorf("%w: desired digest: %v", tool.ErrInternal, err)
	}
	sum := sha256.Sum256(digest)

	t, err := resolveTarget(ctx, env, in.App, in.Platform, in.Version, false)
	if err != nil {
		return nil, err
	}
	editable, _, _, err := appInfos(ctx, env, t.App.ID, "")
	if err != nil {
		return nil, err
	}
	p := &planned{t: t, source: source}
	if editable != nil {
		p.appInfoID, p.infoState = editable.ID, editable.Str("state")
	}
	remote, err := metadata.Pull(ctx, env.ASC, t.Version.ID, p.appInfoID)
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
	plan, err := metadata.Diff(remote, desired, metadata.DiffOptions{Remove: remove, FirstVersion: t.FirstVersion})
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

// nextArgs echoes the caller's source arguments with resolved ids.
func (p *planned) nextArgs(in sourceIn) map[string]any {
	a := map[string]any{"app": p.t.App.ID, "platform": string(p.t.Platform), "version": p.t.Version.String}
	switch {
	case in.Listing != nil:
		a["listing"] = in.Listing
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
