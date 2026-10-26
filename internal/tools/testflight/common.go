package testflight

// Shared helpers of the beta group and beta tester tools (groups.go,
// testers.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// maxTesterBatch caps the testers one add/remove call handles.
const maxTesterBatch = 100

// appRef is the resolved app echoed in results.
type appRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func appOut(a tool.App) appRef { return appRef{ID: a.ID, Name: a.Name} }

// testerOut is one beta tester in a result. Names and emails may be typed by
// the testers themselves (public links), so they stay wrapped as untrusted.
type testerOut struct {
	ID         string         `json:"id"`
	Email      tool.Untrusted `json:"email,omitempty"`
	Name       tool.Untrusted `json:"name,omitempty"`
	State      string         `json:"state,omitempty"`
	InviteType string         `json:"invite_type,omitempty"`
	Groups     []string       `json:"groups,omitempty"`
}

// testerFromResource builds a testerOut; groupNames maps the app's beta group
// ids to names (groups of other apps are left out).
func testerFromResource(r tool.Resource, groupNames map[string]string) testerOut {
	name := strings.TrimSpace(r.Str("firstName") + " " + r.Str("lastName")) // trusted: wrapped as tool.Untrusted below
	out := testerOut{
		ID:         r.ID,
		Email:      r.Text("email"),
		Name:       tool.Untrusted(name),
		State:      r.Str("state"),
		InviteType: r.Str("inviteType"),
	}
	for _, id := range r.RelIDs("betaGroups") {
		if n, ok := groupNames[id]; ok {
			out.Groups = append(out.Groups, n)
		}
	}
	return out
}

// emailKey is the case-insensitive identity of an email address.
func emailKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// testerFields is the sparse fieldset used by tester lookups.
var testerFields = []string{"email", "firstName", "lastName", "state", "inviteType"}

// findTesters lists the team's beta testers whose filterKey ("filter[email]"
// or "filter[id]") matches values, narrowed by scope (e.g. filter[betaGroups]
// or filter[apps]). One GET per call; values are at most maxTesterBatch.
func findTesters(ctx context.Context, env *tool.Env, scope url.Values, filterKey string, values []string) ([]tool.Resource, error) {
	if len(values) == 0 {
		return nil, nil
	}
	q := url.Values{filterKey: {strings.Join(values, ",")}}
	for k, v := range scope {
		q[k] = v
	}
	res, err := env.ASC.List(ctx, "/v1/betaTesters", tool.ListOptions{
		Query:    q,
		Fields:   map[string][]string{"betaTesters": testerFields},
		Limit:    200,
		MaxItems: 1000,
	})
	if err != nil {
		return nil, fmt.Errorf("look up beta testers: %w", err)
	}
	return res.Data, nil
}

// byEmail indexes testers by emailKey.
func byEmail(rs []tool.Resource) map[string]tool.Resource {
	m := make(map[string]tool.Resource, len(rs))
	for _, r := range rs {
		m[emailKey(r.Str("email"))] = r // trusted: lookup key only
	}
	return m
}

// appGroupNames maps the app's beta group ids to names (one GET).
func appGroupNames(ctx context.Context, env *tool.Env, appID string) (map[string]string, error) {
	res, err := env.ASC.List(ctx, "/v1/apps/"+appID+"/betaGroups", tool.ListOptions{
		Fields:   map[string][]string{"betaGroups": {"name"}},
		Limit:    200,
		MaxItems: 1000,
	})
	if err != nil {
		return nil, fmt.Errorf("list beta groups of app %s: %w", appID, err)
	}
	m := make(map[string]string, len(res.Data))
	for _, g := range res.Data {
		m[g.ID] = g.Str("name") // trusted: team-created beta group name, not tester-writable
	}
	return m, nil
}

// testerLinkage is a to-many linkage body of betaTesters ids.
func testerLinkage(ids []string) map[string]any {
	data := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]string{"type": "betaTesters", "id": id})
	}
	return map[string]any{"data": data}
}

// itemError classifies one failed item: outcome is "unknown" when Apple may
// have applied it, "error" otherwise; the body is the structured error.
func itemError(err error) (string, *tool.ErrorBody) {
	outcome, body := tool.ItemError(err)
	return outcome, &body
}

// relationshipTotals reads relationships.<rel>.meta.paging.total of every
// primary resource in a raw JSON:API body; the flattened Resource drops
// relationship meta. Missing totals are absent from the inner map.
func relationshipTotals(body []byte) (map[string]map[string]int, error) {
	var doc struct {
		Data []struct {
			ID            string `json:"id"`
			Relationships map[string]struct {
				Meta struct {
					Paging struct {
						Total *int `json:"total"`
					} `json:"paging"`
				} `json:"meta"`
			} `json:"relationships"`
		} `json:"data"`
	}
	if len(body) == 0 {
		return map[string]map[string]int{}, nil
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("decode relationship totals: %w", err)
	}
	out := make(map[string]map[string]int, len(doc.Data))
	for _, d := range doc.Data {
		m := map[string]int{}
		for name, rel := range d.Relationships {
			if rel.Meta.Paging.Total != nil {
				m[name] = *rel.Meta.Paging.Total
			}
		}
		out[d.ID] = m
	}
	return out, nil
}
