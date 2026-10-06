package resolve

import (
	"context"
	"fmt"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// maxGroupHint caps the group names listed in a not-found hint.
const maxGroupHint = 10

// Group implements [tool.Resolver]: a beta group of the app by id or by
// case-insensitive exact name.
func (r *Resolver) Group(ctx context.Context, appID string, ref tool.GroupRef) (tool.Group, error) {
	s := strings.TrimSpace(string(ref))
	res, err := r.asc.List(ctx, "/v1/apps/"+appID+"/betaGroups", tool.ListOptions{
		Fields:   map[string][]string{"betaGroups": {"name", "isInternalGroup"}},
		Limit:    200,
		MaxItems: 1000,
	})
	if err != nil {
		return tool.Group{}, fmt.Errorf("list beta groups of app %s: %w", appID, err)
	}
	var hits []tool.Group
	for _, d := range res.Data {
		g := groupFromResource(d)
		if s != "" && g.ID == s {
			return g, nil
		}
		if s != "" && strings.EqualFold(g.Name, s) {
			hits = append(hits, g)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		names := make([]string, 0, maxGroupHint)
		for _, d := range res.Data[:min(len(res.Data), maxGroupHint)] {
			names = append(names, d.Str("name"))
		}
		hint := "the app has no beta groups"
		if len(names) > 0 {
			hint = "beta groups: " + strings.Join(names, ", ")
		}
		return tool.Group{}, &tool.ErrNotFound{
			Kind: "beta group", Ref: s, Hint: hint,
			Next: next("testflight_groups_list", map[string]any{"app": appID}),
		}
	}
	amb := &tool.ErrAmbiguous{Kind: "beta group", Ref: s, Next: next("testflight_groups_list", map[string]any{"app": appID})}
	for _, g := range hits[:min(len(hits), maxCandidates)] {
		label := g.Name + " (external)"
		if g.Internal {
			label = g.Name + " (internal)"
		}
		amb.Candidates = append(amb.Candidates, tool.Candidate{ID: g.ID, Label: label})
	}
	return tool.Group{}, amb
}

func groupFromResource(d tool.Resource) tool.Group {
	internal, _ := d.Bool("isInternalGroup")
	return tool.Group{ID: d.ID, Name: d.Str("name"), Internal: internal}
}
