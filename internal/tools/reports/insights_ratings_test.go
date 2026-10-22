package reports

import (
	"net/http"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// R7 AO3: with no app, ratings aggregate over every app /v1/apps returns
// (as reviews_list app:"all" counts them); per-app sections say they need one.
func TestInsightsSummaryAllAppsRatings(t *testing.T) {
	k := insightsKit(t)
	k.Resolve.DefaultApp = "e" // ambiguous: no app selected
	k.ASC.Handle(http.MethodGet, "/v1/apps", http.StatusOK,
		`{"data":[{"type":"apps","id":"1000000001","attributes":{"name":"Example Notes"}},{"type":"apps","id":"2000000001","attributes":{"name":"Other App"}}]}`)
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "insights_summary"), k.Env, ``))
	got := tooltest.Decode[struct {
		App     any            `json:"app"`
		Ratings map[string]any `json:"ratings"`
		Release map[string]any `json:"release"`
	}](t, r)
	if got.App != nil {
		t.Fatalf("app = %v", got.App)
	}
	rt := got.Ratings
	if rt["status"] != "ok" || rt["new_reviews"] != 4.0 || rt["average"] != "3.50" || rt["previous_new_reviews"] != 2.0 ||
		rt["previous_average"] != "4.00" || rt["apps_scanned"] != 2.0 || rt["apps_with_reviews"] != 2.0 ||
		tooltest.JSON(t, rt["stars"]) != `[0,2,0,0,2]` {
		t.Errorf("ratings = %s", tooltest.JSON(t, rt))
	}
	// PO2: without an app the release section lists in-flight versions per
	// app from the one /v1/apps call (none in this fixture).
	if got.Release["status"] != "ok" || got.Release["apps"] != nil {
		t.Errorf("release = %v", got.Release)
	}
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/apps/{id}/customerReviews")); n != 2 {
		t.Errorf("review lists = %d, want 2", n)
	}
}
