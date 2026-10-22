package reports

import (
	"net/http"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// PO2: insights_summary without an app lists in-flight versions per app from
// ONE /v1/apps call with versions included; settled apps are left out.
func TestInsightsSummaryPortfolioRelease(t *testing.T) {
	k := insightsKit(t)
	k.Resolve.DefaultApp = "e" // ambiguous: no app selected
	k.ASC.Handle(http.MethodGet, "/v1/apps", http.StatusOK, `{"data":[
	 {"type":"apps","id":"1","attributes":{"name":"Live"},"relationships":{"appStoreVersions":{"data":[{"type":"appStoreVersions","id":"v1"}]}}},
	 {"type":"apps","id":"2","attributes":{"name":"Nord Saga"},"relationships":{"appStoreVersions":{"data":[{"type":"appStoreVersions","id":"v2"},{"type":"appStoreVersions","id":"v3"}]}}}],
	 "included":[
	 {"type":"appStoreVersions","id":"v1","attributes":{"appVersionState":"READY_FOR_DISTRIBUTION","platform":"IOS","versionString":"1.0","createdDate":"2026-10-01T00:00:00Z"}},
	 {"type":"appStoreVersions","id":"v2","attributes":{"appVersionState":"WAITING_FOR_REVIEW","platform":"IOS","versionString":"2.1","createdDate":"2026-10-20T00:00:00Z"}},
	 {"type":"appStoreVersions","id":"v3","attributes":{"appVersionState":"READY_FOR_DISTRIBUTION","platform":"IOS","versionString":"2.0","createdDate":"2026-09-01T00:00:00Z"}}]}`)
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "insights_summary"), k.Env, `{"app":"all"}`))
	got := tooltest.Decode[struct {
		Release map[string]any `json:"release"`
	}](t, r)
	want := `[{"id":"2","in_flight":[{"id":"v2","platform":"IOS","state":"WAITING_FOR_REVIEW","version":"2.1"}],"name":{"untrusted":"Nord Saga"}}]`
	if got.Release["status"] != "ok" || tooltest.JSON(t, got.Release["apps"]) != want {
		t.Fatalf("release = %s", tooltest.JSON(t, got.Release))
	}
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/apps/{id}/appStoreVersions")); n != 0 {
		t.Fatalf("per-app version reads = %d, want 0", n)
	}
}
