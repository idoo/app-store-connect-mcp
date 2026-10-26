package testflight

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

// maxPublicLinkLimit is Apple's cap on testers joining via one public link
// (the external tester limit of an app).
const maxPublicLinkLimit = 10000

type groupsUpsertIn struct {
	App               tool.AppRef `json:"app,omitempty"`
	Name              string      `json:"name"`
	Internal          bool        `json:"internal,omitempty" jsonschema:"new group only"`
	PublicLinkEnabled *bool       `json:"public_link_enabled,omitempty"`
	PublicLinkLimit   *int        `json:"public_link_limit,omitempty" jsonschema:"1-10000; 0 = none"`
	FeedbackEnabled   *bool       `json:"feedback_enabled,omitempty"`
	tool.ConfirmFlag
}

// groupUpsertOut is the testflight_groups_upsert result.
type groupUpsertOut struct {
	App     appRef         `json:"app"`
	Outcome string         `json:"outcome"`
	Group   groupOut       `json:"group"`
	Changed map[string]any `json:"changed,omitempty"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "testflight_groups_upsert", Toolset: tool.TestFlight, Class: tool.Write, Idempotent: true,
		Confirm: tool.ConfirmByFlag,
		Title:   "Create or update a TestFlight group",
		Description: "Create a beta group by name, or change its public link, limit and feedback. Changes to an " +
			"existing group need confirm:true; turning a public link on needs a human.",
	}, groupsUpsert))
}

// groupSettingAttrs are the betaGroups attributes the upsert reads and sets.
const groupSettingAttrs = "name,isInternalGroup,hasAccessToAllBuilds,publicLinkEnabled,publicLink," +
	"publicLinkLimitEnabled,publicLinkLimit,feedbackEnabled,createdDate"

func groupsUpsert(ctx context.Context, env *tool.Env, in groupsUpsertIn) (*tool.Result, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, tool.InvalidArgs("name is required")
	}
	if l := in.PublicLinkLimit; l != nil && (*l < 0 || *l > maxPublicLinkLimit) {
		return nil, tool.InvalidArgs("public_link_limit must be 1-%d, or 0 for no limit", maxPublicLinkLimit)
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	existing, found, err := findGroupByName(ctx, env, app.ID, name)
	if err != nil {
		return nil, err
	}
	internal := in.Internal
	if found {
		internal, _ = existing.Bool("isInternalGroup")
	}
	if internal && (in.PublicLinkEnabled != nil || in.PublicLinkLimit != nil) {
		return nil, tool.InvalidArgs("internal groups have no public link; omit public_link_*")
	}
	want := wantedGroupAttrs(in)
	if !found {
		return createGroup(ctx, env, app, name, in.Internal, want, in.Confirm)
	}

	cur := currentGroupAttrs(existing)
	changed := map[string]any{}
	for k, v := range want {
		if cur[k] != v {
			changed[k] = v
		}
	}
	g := groupFromResource(existing, nil)
	out := groupUpsertOut{App: appOut(app), Outcome: outcomeAlready, Group: g}
	if len(changed) == 0 {
		return tool.Already(out), nil
	}
	before := map[string]any{}
	for k := range changed {
		before[k] = cur[k]
	}
	preview := tool.PlanPreview{
		Summary:  fmt.Sprintf("update beta group %q of %s: %s", g.Name, app.Name, strings.Join(slices.Sorted(maps.Keys(changed)), ", ")),
		Current:  before,
		Intended: changed,
		Affected: []string{fmt.Sprintf("group %q (%s)", g.Name, g.ID)},
	}
	gate := tool.RequireConfirm
	if changed["publicLinkEnabled"] == true {
		preview.Irreversible = publicLinkNote
		gate = tool.RequireHumanConfirm
	}
	wctx, res := gate(ctx, env, in.Confirm, preview)
	if res != nil {
		return res, nil
	}
	resp, err := write(wctx, env, http.MethodPatch, "/v1/betaGroups/"+existing.ID, map[string]any{
		"data": map[string]any{"type": "betaGroups", "id": existing.ID, "attributes": changed},
	})
	if err != nil {
		return nil, fmt.Errorf("update beta group %q: %w", g.Name, err)
	}
	if r, ok := resp.First(); ok {
		out.Group = groupFromResource(r, nil)
	}
	out.Outcome, out.Changed = outcomeUpdated, changed
	return tool.OK(out), nil
}

// publicLinkNote is the irreversibility note of turning a public link on.
var publicLinkNote = []string{"anyone with the public link can join the group and install its approved builds"}

// createGroup creates the group. Plain creates are additive and run at once;
// one that opens a public link needs confirm:true and a human.
func createGroup(ctx context.Context, env *tool.Env, app tool.App, name string, internal bool, attrs map[string]any, confirm bool) (*tool.Result, error) {
	attrs["name"] = name
	if internal {
		attrs["isInternalGroup"] = true
	}
	var (
		wctx context.Context
		res  *tool.Result
	)
	if attrs["publicLinkEnabled"] == true {
		wctx, res = tool.RequireHumanConfirm(ctx, env, confirm, tool.PlanPreview{
			Summary:      fmt.Sprintf("create beta group %q of %s with a public link", name, app.Name),
			Intended:     attrs,
			Affected:     []string{"app " + app.ID},
			Irreversible: publicLinkNote,
		})
	} else {
		wctx, res = tool.Unconfirmed(ctx, env)
	}
	if res != nil {
		return res, nil
	}
	resp, err := write(wctx, env, http.MethodPost, "/v1/betaGroups", map[string]any{
		"data": map[string]any{
			"type": "betaGroups", "attributes": attrs,
			"relationships": map[string]any{"app": toOne("apps", app.ID)},
		},
	})
	if tool.IsConflict(err) {
		// Created meanwhile (e.g. a replayed call): report the stored group.
		if r, found, ferr := findGroupByName(ctx, env, app.ID, name); ferr == nil && found {
			return tool.Already(groupUpsertOut{App: appOut(app), Outcome: outcomeAlready, Group: groupFromResource(r, nil)}), nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("create beta group %q: %w", name, err)
	}
	out := groupUpsertOut{App: appOut(app), Outcome: outcomeCreated, Group: groupOut{Name: name, Internal: internal}}
	if r, ok := resp.First(); ok {
		out.Group = groupFromResource(r, nil)
	}
	return tool.OK(out).Then(tool.NextStep{Tool: "testflight_testers_add", Args: map[string]any{"app": app.ID, "group": out.Group.ID}}), nil
}

// wantedGroupAttrs maps the requested settings to betaGroups attributes.
func wantedGroupAttrs(in groupsUpsertIn) map[string]any {
	a := map[string]any{}
	if in.PublicLinkEnabled != nil {
		a["publicLinkEnabled"] = *in.PublicLinkEnabled
	}
	if in.FeedbackEnabled != nil {
		a["feedbackEnabled"] = *in.FeedbackEnabled
	}
	if l := in.PublicLinkLimit; l != nil {
		a["publicLinkLimitEnabled"] = *l > 0
		if *l > 0 {
			a["publicLinkLimit"] = *l
		}
	}
	return a
}

// currentGroupAttrs reads the settable attributes of a group, with unset
// (null) booleans as false, comparable with wantedGroupAttrs values.
func currentGroupAttrs(r tool.Resource) map[string]any {
	a := map[string]any{}
	for _, k := range []string{"publicLinkEnabled", "feedbackEnabled", "publicLinkLimitEnabled"} {
		a[k], _ = r.Bool(k)
	}
	if n, ok := r.Int("publicLinkLimit"); ok {
		a["publicLinkLimit"] = n
	}
	return a
}

// findGroupByName returns the app's beta group named name (case-insensitive).
func findGroupByName(ctx context.Context, env *tool.Env, appID, name string) (tool.Resource, bool, error) {
	resp, err := env.ASC.Get(ctx, "/v1/betaGroups", url.Values{
		"filter[app]":        {appID},
		"filter[name]":       {name},
		"fields[betaGroups]": {groupSettingAttrs},
		"limit":              {"200"},
	})
	if err != nil {
		return tool.Resource{}, false, fmt.Errorf("find beta group %q: %w", name, err)
	}
	for _, r := range resp.Data {
		if strings.EqualFold(strings.TrimSpace(r.Str("name")), name) { // trusted: compared, never echoed
			return r, true, nil
		}
	}
	return tool.Resource{}, false, nil
}
