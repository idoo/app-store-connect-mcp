package release

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

type publishIn struct {
	App      tool.AppRef     `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform tool.Platform   `json:"platform,omitempty"`
	Version  tool.VersionRef `json:"version,omitempty" jsonschema:"default: the version awaiting release"`
	tool.ConfirmFlag
}

type phasedIn struct {
	App      tool.AppRef     `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform tool.Platform   `json:"platform,omitempty"`
	Version  tool.VersionRef `json:"version,omitempty" jsonschema:"default: newest phased"`
	Action   string          `json:"action"`
	tool.ConfirmFlag
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "release_publish", Toolset: tool.Release, Class: tool.Destructive, Idempotent: true,
		Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Release an approved version",
		Description: "Release a PENDING_DEVELOPER_RELEASE version to the App Store; other states are refused. Without " +
			"confirm:true returns a preview.",
		Enums: map[string][]any{"platform": platformEnum},
	}, releasePublish))
	tool.Register(tool.New(tool.Meta{
		Name: "release_phased", Toolset: tool.Release, Class: tool.Destructive, Idempotent: true,
		Confirm: tool.ConfirmByFlag,
		Title:   "Control a phased release",
		Description: "Pause, resume or complete a 7-day phased release, or remove one not yet started; shows day and %. " +
			"Enable it before release (release_prepare phased:true). Without confirm:true previews; complete needs human approval.",
		Enums: map[string][]any{"platform": platformEnum, "action": {"pause", "resume", "complete", "remove", "start"}},
	}, releasePhased))
}

// distributedStates mean the version was released (or is being released).
var distributedStates = []string{"PROCESSING_FOR_DISTRIBUTION", "READY_FOR_DISTRIBUTION"}

type publishOut struct {
	App      tool.App      `json:"app"`
	Platform tool.Platform `json:"platform"`
	Version  versionView   `json:"version"`
	Phased   *phasedView   `json:"phased_release,omitempty"`
	Action   string        `json:"action,omitempty"`
}

func releasePublish(ctx context.Context, env *tool.Env, in publishIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := app.Platform(in.Platform)
	d, err := loadVersionPhased(ctx, env, app, platform, in.Version, func(v *versionDetail) bool {
		return v.Version.Str("appVersionState") == "PENDING_DEVELOPER_RELEASE"
	})
	if err != nil {
		return nil, err
	}
	out := publishOut{App: app, Platform: platform, Version: viewVersion(d.Version)}
	if p := phasedOf(d); p != nil {
		out.Phased = viewPhased(*p)
	}
	switch st := out.Version.State; {
	case slices.Contains(distributedStates, st):
		return tool.Already(out), nil
	case st != "PENDING_DEVELOPER_RELEASE":
		return nil, tool.WithNext(
			fmt.Errorf("%w: version %s is %s; only an approved version in PENDING_DEVELOPER_RELEASE can be released", errWrongState, out.Version.String, st),
			reviewStatusNext(app, platform),
		)
	}
	intended := []string{"appVersionState PENDING_DEVELOPER_RELEASE → PROCESSING_FOR_DISTRIBUTION → READY_FOR_DISTRIBUTION"}
	if out.Phased != nil && out.Phased.State == "INACTIVE" {
		intended = append(intended, "phased release starts: day 1, 1% of automatic updates")
	}
	preview := tool.PlanPreview{
		Summary:      fmt.Sprintf("release %s %s to the App Store now", app.Name, out.Version.String),
		Current:      out.Version,
		Intended:     intended,
		Affected:     []string{app.ID, out.Version.ID},
		Irreversible: []string{"the version goes live for customers; it cannot be withdrawn, only replaced by a newer version"},
	}
	wctx, res := tool.RequireConfirm(ctx, env, in.Confirm, preview)
	if res != nil {
		return res, nil
	}
	if _, err := doWrite(wctx, env, http.MethodPost, "/v1/appStoreVersionReleaseRequests",
		jsonapiBody("appStoreVersionReleaseRequests", "", nil, map[string]string{"appStoreVersion": "appStoreVersions:" + out.Version.ID})); err != nil {
		return nil, err
	}
	out.Action = "release_requested"
	r := tool.OK(out)
	if out.Phased != nil {
		r.Then(tool.NextStep{Tool: "release_phased", Args: map[string]any{"app": app.ID, "platform": string(platform), "version": out.Version.String, "action": "pause"}})
	}
	return r.Then(statusNext(app, platform, out.Version.String, nil)), nil
}

// phasedTransition is one PATCH action of release_phased.
type phasedTransition struct {
	from []string // states the action applies to
	to   string   // target phasedReleaseState
}

var phasedTransitions = map[string]phasedTransition{
	"pause":    {from: []string{"ACTIVE"}, to: "PAUSED"},
	"resume":   {from: []string{"PAUSED"}, to: "ACTIVE"},
	"complete": {from: []string{"ACTIVE", "PAUSED"}, to: "COMPLETE"},
	"remove":   {from: []string{"INACTIVE"}},
}

type phasedOut struct {
	App      tool.App      `json:"app"`
	Platform tool.Platform `json:"platform"`
	Version  versionView   `json:"version"`
	Action   string        `json:"action"`
	Status   string        `json:"status"`
	Before   *phasedView   `json:"before,omitempty"`
	After    *phasedView   `json:"after,omitempty"`
	Note     string        `json:"note,omitempty"`
}

func releasePhased(ctx context.Context, env *tool.Env, in phasedIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := app.Platform(in.Platform)
	tr, known := phasedTransitions[in.Action]
	if !known && in.Action != "start" {
		return nil, fmt.Errorf("%w: unknown action %q", tool.ErrInvalidArgs, in.Action)
	}
	d, err := loadVersionPhased(ctx, env, app, platform, in.Version, func(v *versionDetail) bool {
		p := phasedOf(v)
		return p != nil && (in.Action == "start" || slices.Contains(tr.from, p.Str("phasedReleaseState")) || p.Str("phasedReleaseState") == tr.to)
	})
	if err != nil {
		return nil, err
	}
	out := phasedOut{App: app, Platform: platform, Version: viewVersion(d.Version), Action: in.Action}
	ph := phasedOf(d)
	if ph != nil {
		out.Before = viewPhased(*ph)
	}
	if in.Action == "start" {
		return phasedStart(app, platform, out), nil
	}
	if ph == nil {
		if in.Action == "remove" {
			out.Status, out.Note = "none", "the version has no phased release"
			return tool.Already(out), nil
		}
		return nil, tool.WithNext(
			fmt.Errorf("%w: version %s (%s) has no phased release; phasing must be enabled before release", errWrongState, out.Version.String, out.Version.State),
			statusNext(app, platform, out.Version.String, nil),
		)
	}
	cur := out.Before.State
	if tr.to != "" && cur == tr.to {
		out.Status, out.After = cur, out.Before
		return tool.Already(out), nil
	}
	if !slices.Contains(tr.from, cur) {
		msg := fmt.Sprintf("phased release of %s is %s; %s needs %v", out.Version.String, cur, in.Action, tr.from)
		if in.Action == "remove" {
			msg += "; a started phased release cannot be removed (use complete to release to everyone now)"
		}
		return nil, fmt.Errorf("%w: %s", errWrongState, msg)
	}
	preview := tool.PlanPreview{Current: out.Before, Affected: []string{app.ID, out.Version.ID, ph.ID}}
	switch in.Action {
	case "remove":
		preview.Summary = fmt.Sprintf("remove the phased release of %s %s: it will release to all users at once", app.Name, out.Version.String)
		preview.Intended = []string{"phased release deleted"}
	case "complete":
		preview.Summary = fmt.Sprintf("complete the phased release of %s %s: all users get it now", app.Name, out.Version.String)
		preview.Intended = []string{"phasedReleaseState " + cur + " → COMPLETE (100%)"}
		preview.Irreversible = []string{"a completed phased release cannot be paused or restarted"}
	default:
		preview.Summary = fmt.Sprintf("%s the phased release of %s %s (day %d, %d%%)", in.Action, app.Name, out.Version.String, out.Before.CurrentDay, out.Before.Percent)
		preview.Intended = []string{"phasedReleaseState " + cur + " → " + tr.to}
	}
	gate := tool.RequireConfirm
	if in.Action == "complete" || in.Action == "remove" {
		// Releasing to 100% of users at once, or dropping the phased schedule,
		// is public and irreversible.
		gate = tool.RequireHumanConfirm
	}
	wctx, res := gate(ctx, env, in.Confirm, preview)
	if res != nil {
		return res, nil
	}
	path := "/v1/appStoreVersionPhasedReleases/" + ph.ID
	if in.Action == "remove" {
		if _, err := doWrite(wctx, env, http.MethodDelete, path, nil); err != nil && !goneOK(err) {
			return nil, err
		}
		out.Status = "removed"
		return tool.OK(out), nil
	}
	resp, err := doWrite(wctx, env, http.MethodPatch, path,
		jsonapiBody("appStoreVersionPhasedReleases", ph.ID, map[string]any{"phasedReleaseState": tr.to}, nil))
	if err != nil {
		return nil, err
	}
	after := *out.Before
	after.State = tr.to
	if r, ok := resp.First(); ok && r.Str("phasedReleaseState") != "" {
		after = *viewPhased(r)
	}
	if after.State == "COMPLETE" {
		after.Percent = 100
	}
	out.Status, out.After = after.State, &after
	if tr.to == "PAUSED" {
		out.Note = "pauses add up to at most 30 days in total; resume with action resume"
	}
	return tool.OK(out), nil
}

// phasedStart explains "start": phasing cannot be started by a call; it is
// opted into before release and begins when the version is released.
func phasedStart(app tool.App, platform tool.Platform, out phasedOut) *tool.Result {
	vs := out.Version.String
	if out.Before == nil {
		if slices.Contains(distributedStates, out.Version.State) || slices.Contains(releasedStates, out.Version.State) {
			out.Status = "manual"
			out.Note = "version " + vs + " is already released without phasing; phasing cannot be added after release. Enable it for the next version with release_prepare phased:true"
			return tool.OK(out).Then(statusNext(app, platform, "", map[string]any{"phased": true}))
		}
		out.Status = "not_enabled"
		out.Note = "enable phasing before release with release_prepare phased:true; it starts when the version is released"
		return tool.OK(out).Then(statusNext(app, platform, vs, map[string]any{"phased": true}))
	}
	switch out.Before.State {
	case "INACTIVE":
		out.Status = "scheduled"
		out.Note = "the phased release starts automatically when the version is released"
		if out.Version.State == "PENDING_DEVELOPER_RELEASE" {
			return tool.OK(out).Then(tool.NextStep{Tool: "release_publish", Args: versionArgs(app, platform, vs)})
		}
		return tool.OK(out)
	case "PAUSED":
		out.Status = "paused"
		out.Note = "the phased release is paused; use action resume"
		args := versionArgs(app, platform, vs)
		args["action"] = "resume"
		return tool.OK(out).Then(tool.NextStep{Tool: "release_phased", Args: args})
	}
	out.Status, out.After = out.Before.State, out.Before
	return tool.Already(out)
}
