package insights

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// portfolioVersions is how many versions per app the portfolio call includes.
const portfolioVersions = 10

// settledStates are App Store version states with nothing in flight.
var settledStates = []string{
	"READY_FOR_DISTRIBUTION", "READY_FOR_SALE", "REPLACED_WITH_NEW_VERSION",
	"REMOVED_FROM_SALE", "DEVELOPER_REMOVED_FROM_SALE",
}

// PortfolioVersion is an in-flight App Store version (Apple enums, trusted).
type PortfolioVersion struct {
	ID       string `json:"id"`
	Platform string `json:"platform,omitempty"`
	Version  string `json:"version,omitempty"`
	State    string `json:"state"`
}

// PortfolioApp is one app of the account with its in-flight versions.
type PortfolioApp struct {
	App      tool.App           `json:"-"`
	InFlight []PortfolioVersion `json:"in_flight,omitempty"`
	// LastActivity is the newest version createdDate (RFC 3339), or "".
	LastActivity string `json:"last_activity,omitempty"`
}

// Portfolio lists every app of the account (up to 200) from ONE request with
// its App Store versions included, ranked: apps with an in-flight version
// (waiting, in review, rejected, pending release, being prepared…) first,
// then by latest version activity, then by name.
func Portfolio(ctx context.Context, env *tool.Env) ([]PortfolioApp, error) {
	list, err := env.ASC.List(ctx, "/v1/apps", tool.ListOptions{
		Fields: map[string][]string{
			"apps":             {"name", "bundleId", "sku", "appStoreVersions"},
			"appStoreVersions": {"platform", "versionString", "appVersionState", "appStoreState", "createdDate"},
		},
		Include:  []string{"appStoreVersions"},
		Query:    url.Values{"limit[appStoreVersions]": {fmt.Sprint(portfolioVersions)}},
		Limit:    200,
		MaxItems: 200,
	})
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	versions := make(map[string]tool.Resource, len(list.Included))
	for _, r := range list.Included {
		if r.Type == "appStoreVersions" {
			versions[r.ID] = r
		}
	}
	out := make([]PortfolioApp, 0, len(list.Data))
	for _, a := range list.Data {
		p := PortfolioApp{App: tool.App{ID: a.ID, Name: string(a.Text("name")), BundleID: a.Str("bundleId"), SKU: a.Str("sku")}}
		for _, id := range a.RelIDs("appStoreVersions") {
			v, ok := versions[id]
			if !ok {
				continue
			}
			p.LastActivity = max(p.LastActivity, v.Str("createdDate"))
			state := cmp.Or(v.Str("appVersionState"), v.Str("appStoreState"))
			if state == "" || slices.Contains(settledStates, state) {
				continue
			}
			p.InFlight = append(p.InFlight, PortfolioVersion{ID: v.ID, Platform: v.Str("platform"), Version: v.Str("versionString"), State: state})
		}
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b PortfolioApp) int {
		if fa, fb := len(a.InFlight) > 0, len(b.InFlight) > 0; fa != fb {
			if fa {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(b.LastActivity, a.LastActivity); c != 0 {
			return c
		}
		return cmp.Compare(a.App.Name, b.App.Name)
	})
	return out, nil
}
