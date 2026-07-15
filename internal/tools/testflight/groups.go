package testflight

import (
	"context"
	"fmt"
	"net/url"

	"github.com/idoo/hamn/internal/tool"
)

type groupsListIn struct {
	App  tool.AppRef `json:"app,omitempty" jsonschema:"app id, bundle id, SKU or name; default: pinned app"`
	Type string      `json:"type,omitempty" jsonschema:"internal, external or all (default)"`
}

// groupOut is one beta group in the groups_list result. Counts are nil when
// Apple did not report a total.
type groupOut struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Internal          bool   `json:"internal"`
	Testers           *int   `json:"testers,omitempty"`
	Builds            *int   `json:"builds,omitempty"`
	AllBuilds         bool   `json:"all_builds,omitempty"`
	PublicLinkEnabled bool   `json:"public_link_enabled"`
	PublicLink        string `json:"public_link,omitempty"`
	PublicLinkLimit   *int   `json:"public_link_limit,omitempty"`
	FeedbackEnabled   bool   `json:"feedback_enabled"`
	Created           string `json:"created,omitempty"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "testflight_groups_list", Toolset: tool.TestFlight, Class: tool.Read, Idempotent: true,
		Title: "List TestFlight groups",
		Description: "Beta groups of an app: internal/external, tester and build counts, public link and feedback " +
			"settings, in one call. Pass a group's name or id as `group` to testflight_testers_* and testflight_distribute.",
		Enums: map[string][]any{"type": {"internal", "external", "all"}},
	}, groupsList))
}

func groupsList(ctx context.Context, env *tool.Env, in groupsListIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"filter[app]":        {app.ID},
		"fields[betaGroups]": {"name,isInternalGroup,hasAccessToAllBuilds,publicLinkEnabled,publicLink,publicLinkLimitEnabled,publicLinkLimit,feedbackEnabled,createdDate,betaTesters,builds"},
		// Counts come from relationships.*.meta.paging.total; one included
		// item per relationship keeps the response small.
		"include":             {"betaTesters,builds"},
		"fields[betaTesters]": {"state"},
		"fields[builds]":      {"version"},
		"limit[betaTesters]":  {"1"},
		"limit[builds]":       {"1"},
		"limit":               {"200"},
		"sort":                {"name"},
	}
	switch in.Type {
	case "internal":
		q.Set("filter[isInternalGroup]", "true")
	case "external":
		q.Set("filter[isInternalGroup]", "false")
	}
	resp, err := env.ASC.Get(ctx, "/v1/betaGroups", q)
	if err != nil {
		return nil, fmt.Errorf("list beta groups: %w", err)
	}
	totals, err := relationshipTotals(resp.Body)
	if err != nil {
		return nil, err
	}
	groups := make([]groupOut, 0, len(resp.Data))
	for _, r := range resp.Data {
		groups = append(groups, groupFromResource(r, totals[r.ID]))
	}
	res := tool.OK(map[string]any{"app": appOut(app), "groups": groups, "count": len(groups)})
	if resp.Links["next"] != "" {
		res.Warn("the app has more than 200 beta groups; only the first 200 by name are listed")
	}
	if len(groups) > 0 {
		res.Then(tool.NextStep{Tool: "testflight_testers_list", Args: map[string]any{"app": app.ID, "group": groups[0].ID}})
	}
	return res, nil
}

func groupFromResource(r tool.Resource, totals map[string]int) groupOut {
	g := groupOut{ID: r.ID, Name: r.Str("name"), PublicLink: r.Str("publicLink"), Created: r.Str("createdDate")}
	g.Internal, _ = r.Bool("isInternalGroup")
	g.AllBuilds, _ = r.Bool("hasAccessToAllBuilds")
	g.PublicLinkEnabled, _ = r.Bool("publicLinkEnabled")
	g.FeedbackEnabled, _ = r.Bool("feedbackEnabled")
	if on, _ := r.Bool("publicLinkLimitEnabled"); on {
		if n, ok := r.Int("publicLinkLimit"); ok {
			g.PublicLinkLimit = &n
		}
	}
	g.Testers = relCount(r, "betaTesters", totals)
	g.Builds = relCount(r, "builds", totals)
	return g
}

// relCount returns the relationship's paging total, or 0 when the included
// relationship is empty, or nil when unknown.
func relCount(r tool.Resource, rel string, totals map[string]int) *int {
	if n, ok := totals[rel]; ok {
		return &n
	}
	if v, ok := r.Rel[rel]; ok {
		if ids, _ := v.([]string); len(ids) == 0 {
			zero := 0
			return &zero
		}
	}
	return nil
}
