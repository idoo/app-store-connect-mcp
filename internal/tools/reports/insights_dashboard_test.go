package reports

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const (
	dashSettingsPath = "/analytics/api/v1/settings/all"
	dashMeasuresPath = "/analytics/api/v1/data/app/detail/measures"
)

func dashFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "dashboard", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// routeDashMeasures answers the per-app measures request by adamId.
func routeDashMeasures(t *testing.T, k *tooltest.Kit, byApp map[string]func() (string, error)) {
	t.Helper()
	k.Portal.Handle(http.MethodPost, dashMeasuresPath, func(c tooltest.PortalCall) (string, error) {
		var body struct {
			AdamID    []string `json:"adamId"`
			StartTime string   `json:"startTime"`
			EndTime   string   `json:"endTime"`
		}
		if err := json.Unmarshal([]byte(c.Body), &body); err != nil || len(body.AdamID) != 1 {
			t.Errorf("measures body = %s", c.Body)
			return "", &tool.PortalError{Status: 400, Messages: []string{"Adam id list size must be 1"}}
		}
		// last_7_days at fixedNow is 10-15..21; the dashboard ends 10-20.
		if body.StartTime != "2026-10-14T00:00:00Z" || body.EndTime != "2026-10-20T00:00:00Z" {
			t.Errorf("measures window = %s..%s", body.StartTime, body.EndTime)
		}
		fn := byApp[body.AdamID[0]]
		if fn == nil {
			t.Errorf("unexpected app %s", body.AdamID[0])
			return "", &tool.PortalError{Status: 404}
		}
		return fn()
	})
}

const dashApps = `{"data":[
 {"type":"apps","id":"1000000011","attributes":{"name":"Alpha"}},
 {"type":"apps","id":"1000000012","attributes":{"name":"Beta"}},
 {"type":"apps","id":"1000000013","attributes":{"name":"Gamma"}},
 {"type":"apps","id":"1000000014","attributes":{"name":"Delta"}}]}`

// Without an app, insights_summary reads every app from the dashboard:
// apps by downloads, never-released apps under no_data, one app's failure
// kept as its own entry, account totals summing only COUNT measures.
func TestInsightsSummaryDashboardPortfolio(t *testing.T) {
	k := insightsKit(t).WebSession()
	k.ASC.Handle(http.MethodGet, "/v1/apps", http.StatusOK, dashApps)
	k.Portal.HandleJSON(http.MethodGet, dashSettingsPath, dashFixture(t, "settings.json"))
	fixture := func(name string) func() (string, error) {
		body := dashFixture(t, name)
		return func() (string, error) { return body, nil }
	}
	routeDashMeasures(t, k, map[string]func() (string, error){
		"1000000011": fixture("app_alpha.json"),
		"1000000012": fixture("app_beta.json"),
		"1000000013": func() (string, error) {
			return "", &tool.PortalError{Status: 403, Messages: []string{"Invalid adam id: 1000000013"}}
		},
		"1000000014": func() (string, error) {
			return "", &tool.PortalError{Status: 500, Messages: []string{"Internal error"}}
		},
	})
	// The sales section's vendor discovery is not under test here.
	k.Env.Profile.VendorNumber = "12345"
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "insights_summary"), k.Env, `{"app":"all","period":"last_7_days"}`))
	an := tooltest.Decode[struct {
		Analytics map[string]any `json:"analytics"`
	}](t, r).Analytics
	if an["status"] != "ok" || an["data_source"] != "web_dashboard" ||
		tooltest.JSON(t, an["window"]) != `{"from":"2026-10-14","to":"2026-10-20"}` ||
		tooltest.JSON(t, an["previous_window"]) != `{"from":"2026-10-07","to":"2026-10-13"}` {
		t.Fatalf("analytics = %s", tooltest.JSON(t, an))
	}
	reason, _ := an["reason"].(string)
	for _, want := range []string{"data up to 2026-10-20", "1 app(s) could not be read"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q lacks %q", reason, want)
		}
	}
	apps := an["apps"].([]any)
	if len(apps) != 3 {
		t.Fatalf("apps = %s", tooltest.JSON(t, apps))
	}
	if s := tooltest.JSON(t, apps[0]); s != `{"current":{"conversion_rate_percent":"3","crashes":"0","downloads":"9","impressions":"300","page_views":"40","proceeds_usd":"0","sessions":"30","total_downloads":"12"},`+
		`"id":"1000000012","name":{"untrusted":"Beta"},`+
		`"previous":{"conversion_rate_percent":"3","crashes":"2","downloads":"6","impressions":"200","page_views":"40","proceeds_usd":"0","sessions":"30","total_downloads":"8"}}` {
		t.Errorf("apps[0] = %s", s)
	}
	if s := tooltest.JSON(t, apps[1]); !strings.Contains(s, `"id":"1000000011"`) || !strings.Contains(s, `"downloads":"3"`) || !strings.Contains(s, `"proceeds_usd":"1.5"`) {
		t.Errorf("apps[1] = %s", s)
	}
	if s := tooltest.JSON(t, apps[2]); !strings.Contains(s, `"id":"1000000014"`) || !strings.Contains(s, `"error":"App Store Connect web 500: Internal error"`) || strings.Contains(s, "current") {
		t.Errorf("apps[2] = %s", s)
	}
	if s := tooltest.JSON(t, an["no_data"]); s != `[{"id":"1000000013","name":{"untrusted":"Gamma"}}]` {
		t.Errorf("no_data = %s", s)
	}
	// conversion_rate_percent is an average: shown per app, never summed,
	// even when Apple labels it COUNT (app_beta.json does).
	if s := tooltest.JSON(t, an["totals"]); s != `{"change":{"crashes":"-50.0%","downloads":"+71.4%","impressions":"+60.0%","page_views":"+11.1%","sessions":"+25.0%","total_downloads":"+60.0%"},`+
		`"current":{"crashes":"1","downloads":"12","impressions":"400","page_views":"50","proceeds_usd":"1.5","sessions":"50","total_downloads":"16"},`+
		`"previous":{"crashes":"2","downloads":"7","impressions":"250","page_views":"45","proceeds_usd":"0","sessions":"40","total_downloads":"10"}}` {
		t.Errorf("totals = %s", s)
	}
	if m := k.ASC.Mutations(); len(m) != 0 {
		t.Errorf("mutations = %+v", m)
	}
}

// Without a web session the every-app analytics section says how to get one.
func TestInsightsSummaryDashboardNoSession(t *testing.T) {
	k := insightsKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps", http.StatusOK, dashApps)
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "insights_summary"), k.Env, `{"app":"all"}`))
	an := tooltest.Decode[struct {
		Analytics map[string]any `json:"analytics"`
	}](t, r).Analytics
	if an["status"] != "unavailable" || !strings.Contains(an["reason"].(string), "web_login") || an["apps"] != nil {
		t.Errorf("analytics = %s", tooltest.JSON(t, an))
	}
	if n := len(k.Portal.Calls()); n != 0 {
		t.Errorf("portal calls = %d", n)
	}
}

// With an app whose analytics are not set up and no vendor number in the
// profile, the web session supplies both: sales via the discovered vendor
// number, analytics from the dashboard.
func TestInsightsSummaryWebSessionVendorAndAnalytics(t *testing.T) {
	k := insightsKit(t).WebSession()
	k.Env.Profile.VendorNumber = ""
	k.Portal.HandleJSON(http.MethodGet, olympusPath, `{"provider":{"providerId":4242424}}`)
	k.Portal.HandleJSON(http.MethodGet, vendorsPath, `{"data":[{"sapVendorNumber":7654321,"isArcadeVendor":false}]}`)
	k.Portal.HandleJSON(http.MethodGet, dashSettingsPath, dashFixture(t, "settings.json"))
	alpha := strings.ReplaceAll(dashFixture(t, "app_alpha.json"), "1000000011", "1000000001")
	routeDashMeasures(t, k, map[string]func() (string, error){"1000000001": func() (string, error) { return alpha, nil }})
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "insights_summary"), k.Env, ``))
	got := tooltest.Decode[map[string]any](t, r)
	sales := got["sales"].(map[string]any)
	if sales["status"] != "ok" || sales["vendor_number_source"] != "web_session" {
		t.Errorf("sales = %s", tooltest.JSON(t, sales))
	}
	for _, req := range k.ASC.RequestsTo(http.MethodGet, "/v1/salesReports") {
		if v := req.Query.Get("filter[vendorNumber]"); v != "7654321" {
			t.Fatalf("sales vendor = %q", v)
		}
	}
	an := got["analytics"].(map[string]any)
	if an["status"] != "ok" || an["data_source"] != "web_dashboard" || an["apps"] != nil ||
		!strings.Contains(tooltest.JSON(t, an["totals"]), `"current":{"conversion_rate_percent":"3","crashes":"1","downloads":"3"`) ||
		!strings.Contains(tooltest.JSON(t, an["totals"]), `"downloads":"+200.0%"`) {
		t.Errorf("analytics = %s", tooltest.JSON(t, an))
	}
	if len(r.Next) != 2 || r.Next[0].Tool != "analytics_funnel_report" || r.Next[1].Tool != "sales_report" {
		t.Errorf("next = %+v", r.Next)
	}
}

// A never-released app asked for directly: analytics unavailable with why.
func TestInsightsSummaryDashboardNeverReleasedApp(t *testing.T) {
	k := insightsKit(t).WebSession()
	k.Portal.HandleJSON(http.MethodGet, dashSettingsPath, dashFixture(t, "settings.json"))
	routeDashMeasures(t, k, map[string]func() (string, error){"1000000001": func() (string, error) {
		return "", &tool.PortalError{Status: 403, Messages: []string{"Invalid adam id: 1000000001"}}
	}})
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "insights_summary"), k.Env, ``))
	an := tooltest.Decode[map[string]any](t, r)["analytics"].(map[string]any)
	if an["status"] != "unavailable" || !strings.Contains(an["reason"].(string), "never been on the App Store") ||
		!strings.Contains(an["reason"].(string), "analytics_setup") {
		t.Errorf("analytics = %s", tooltest.JSON(t, an))
	}
}
