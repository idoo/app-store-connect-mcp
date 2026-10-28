package insights

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const (
	dashSettings  = `{"measures":[],"configuration":{"dataStartDate":"2015-04-01T00:00:00Z","dataEndDate":"2026-10-24T00:00:00Z"}}`
	dashMeasures  = "/analytics/api/v1/data/app/detail/measures"
	dashSeries    = "/analytics/api/v1/data/timeseries"
	dashSetupPath = "/analytics/api/v1/settings/all"
	forbiddenBody = `{"errors":[{"status":"403","code":"FORBIDDEN_ERROR","title":"This request is forbidden for security reasons","detail":"The API key in use does not allow this request"}]}`
)

// dashKit is a funnel kit with a web session whose dashboard data ends
// 2026-10-24 (two days before the kit's Pacific today).
func dashKit(t *testing.T, requests string) *tooltest.Kit {
	t.Helper()
	k, _ := funnelKit(t, requests)
	k.WebSession()
	k.Portal.HandleJSON(http.MethodGet, dashSetupPath, dashSettings)
	return k
}

// forbiddenKit is a funnel kit whose key may not list analytics report
// requests (not Admin).
func forbiddenKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t).ReadOnly()
	k.Clock.Set(time.Date(2026, 10, 26, 20, 0, 0, 0, time.UTC))
	k.Resolve.Apps = []tool.App{{ID: funnelAppID, BundleID: "com.example.notes", Name: "Example Notes", SKU: "com.example.notes"}}
	k.ASC.Handle(http.MethodGet, "/v1/apps/"+funnelAppID+"/analyticsReportRequests", http.StatusForbidden, forbiddenBody)
	return k
}

// Not set up + web session: the funnel comes from the dashboard, its period
// ending at the dashboard's last data day, compared via previousTotal.
func TestFunnelDashboardWhenNotSetUp(t *testing.T) {
	k := dashKit(t, funnelDoc(t))
	k.Portal.HandleFile(http.MethodPost, dashMeasures, filepath.Join("testdata", "funnel", "dashboard_funnel.json"))
	r := tooltest.MustOK(t, tooltest.Call(t, funnelTool(t), k.Env, `{"period":"last_7_days"}`))
	got := tooltest.Decode[map[string]any](t, r)
	if got["status"] != "ok" || got["data_source"] != "web_dashboard" ||
		tooltest.JSON(t, got["current"]) != `{"from":"2026-10-18","to":"2026-10-24"}` ||
		tooltest.JSON(t, got["previous"]) != `{"from":"2026-10-11","to":"2026-10-17"}` {
		t.Fatalf("got = %s", tooltest.JSON(t, got))
	}
	funnel := tooltest.JSON(t, got["funnel"])
	for _, want := range []string{
		`"current":{"download_rate":"25.00%","downloads":"10","impressions":"200","page_view_rate":"20.00%","page_views":"40"}`,
		`"previous":{"download_rate":"12.50%","downloads":"5","impressions":"100","page_view_rate":"40.00%","page_views":"40"}`,
		`"downloads":"+100.0%"`, `"impressions":"+100.0%"`,
	} {
		if !strings.Contains(funnel, want) {
			t.Errorf("funnel %s lacks %s", funnel, want)
		}
	}
	usage := tooltest.JSON(t, got["usage"])
	for _, want := range []string{`"total_downloads":"14"`, `"redownloads":"4"`, `"unique_impressions":"150"`, `"conversion_rate_percent":"5"`, `"proceeds_usd":"12.5"`, `"sales_usd":"15"`, `"total_downloads":"+133.3%"`} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage %s lacks %s", usage, want)
		}
	}
	if s := tooltest.JSON(t, got["coverage"]); !strings.Contains(s, `"days_expected":7,"days_present":7`) || !strings.Contains(s, `"lag_days":2`) {
		t.Errorf("coverage = %s", s)
	}
	if body := k.Portal.Calls()[1].Body; !strings.Contains(body, `"adamId":["`+funnelAppID+`"]`) ||
		!strings.Contains(body, `"startTime":"2026-10-18T00:00:00Z"`) || !strings.Contains(body, `"endTime":"2026-10-24T00:00:00Z"`) {
		t.Errorf("measures body = %s", body)
	}
	if len(r.Next) != 0 {
		t.Errorf("next = %+v", r.Next)
	}
}

// A non-Admin key (403 on analytics report requests) with a web session
// answers usage questions from the dashboard.
func TestFunnelDashboardWhenForbidden(t *testing.T) {
	k := forbiddenKit(t).WebSession()
	k.Portal.HandleJSON(http.MethodGet, dashSetupPath, dashSettings)
	k.Portal.HandleJSON(http.MethodPost, dashMeasures, `{"results":[
	 {"measure":"sessions","type":"COUNT","total":50,"previousTotal":25,"meetsThreshold":true,"data":[]},
	 {"measure":"activeDevices","type":"AVERAGE","total":10,"previousTotal":10,"meetsThreshold":true,"data":[]},
	 {"measure":"crashes","type":"COUNT","total":2,"previousTotal":0,"meetsThreshold":false,"data":[]}]}`)
	r := tooltest.MustOK(t, tooltest.Call(t, funnelTool(t), k.Env, `{"question":"crashes","period":"last_28_days"}`))
	got := tooltest.Decode[map[string]any](t, r)
	if got["status"] != "ok" || got["data_source"] != "web_dashboard" || tooltest.JSON(t, got["current"]) != `{"from":"2026-09-27","to":"2026-10-24"}` {
		t.Fatalf("got = %s", tooltest.JSON(t, got))
	}
	if s := tooltest.JSON(t, got["usage"]); s != `{"change":{"active_devices":"0.0%","sessions":"+100.0%"},`+
		`"current":{"active_devices":"10","crashes":"2","crashes_per_1000_sessions":"40.00","sessions":"50"},`+
		`"previous":{"active_devices":"10","crashes":"0","sessions":"25"}}` {
		t.Errorf("usage = %s", s)
	}
	if !strings.Contains(tooltest.JSON(t, got["explanations"]), "forbidden for this API key") ||
		!strings.Contains(tooltest.JSON(t, got["explanations"]), "privacy thresholds") {
		t.Errorf("explanations = %s", tooltest.JSON(t, got["explanations"]))
	}
}

// Without a web session the 403 stays an error, now naming web_login.
func TestFunnelForbiddenWithoutSessionNamesWebLogin(t *testing.T) {
	k := forbiddenKit(t)
	r := tooltest.Call(t, funnelTool(t), k.Env, `{}`)
	body := tooltest.MustFail(t, r, "api_error")
	if body.API == nil || body.API.Status != http.StatusForbidden || !strings.Contains(body.Message, "web_login") {
		t.Errorf("body = %s", tooltest.JSON(t, body))
	}
	if len(body.Next) != 1 || body.Next[0].Tool != "web_login" {
		t.Errorf("next = %+v", body.Next)
	}
}

// A never-released app has no dashboard data: no_data, not an error.
func TestFunnelDashboardNeverReleased(t *testing.T) {
	k := dashKit(t, funnelDoc(t))
	k.Portal.HandleError(http.MethodPost, dashMeasures, http.StatusForbidden, "Invalid adam id: "+funnelAppID)
	r := tooltest.MustOK(t, tooltest.Call(t, funnelTool(t), k.Env, `{}`))
	got := tooltest.Decode[map[string]any](t, r)
	if got["status"] != "no_data" || !strings.Contains(got["message"].(string), "never been on the App Store") || got["funnel"] != nil {
		t.Errorf("got = %s", tooltest.JSON(t, got))
	}
}

// Sources from the dashboard: zero groups dropped, ordered by impressions,
// an explicit to past the data end clamped with a note.
func TestFunnelDashboardSources(t *testing.T) {
	k := dashKit(t, funnelDoc(t))
	k.Portal.HandleFile(http.MethodPost, dashMeasures, filepath.Join("testdata", "funnel", "dashboard_funnel.json"))
	series := map[string]string{
		"impressionsTotal": `[{"group":{"key":"AppRef","title":"App Referrer"},"totals":{"value":0}},{"group":{"key":"Other","title":"App Store Browse"},"totals":{"value":60}},{"group":{"key":"Search","title":"App Store Search"},"totals":{"value":100}}]`,
		"pageViewCount":    `[{"group":{"key":"Other","title":"App Store Browse"},"totals":{"value":10}},{"group":{"key":"Search","title":"App Store Search"},"totals":{"value":20}}]`,
		"units":            `[{"group":{"key":"Other","title":"App Store Browse"},"totals":{"value":0}},{"group":{"key":"Search","title":"App Store Search"},"totals":{"value":5}},{"group":{"key":"Web","title":"Web Referrer"},"totals":{"value":1}}]`,
	}
	k.Portal.Handle(http.MethodPost, dashSeries, func(c tooltest.PortalCall) (string, error) {
		if !strings.Contains(c.Body, `"dimension":"source"`) || !strings.Contains(c.Body, `"startTime":"2026-10-20T00:00:00Z"`) {
			t.Errorf("series body = %s", c.Body)
		}
		for metric, groups := range series {
			if strings.Contains(c.Body, `"metric":"`+metric+`"`) {
				return `{"results":` + groups + `}`, nil
			}
		}
		t.Errorf("unexpected series body %s", c.Body)
		return `{"results":[]}`, nil
	})
	r := tooltest.MustOK(t, tooltest.Call(t, funnelTool(t), k.Env, `{"question":"sources","from":"2026-10-20","to":"2026-10-30"}`))
	got := tooltest.Decode[map[string]any](t, r)
	if tooltest.JSON(t, got["current"]) != `{"from":"2026-10-20","to":"2026-10-24"}` || !strings.Contains(tooltest.JSON(t, got["explanations"]), "clamped to 2026-10-24") {
		t.Fatalf("got = %s", tooltest.JSON(t, got))
	}
	want := `[{"current":{"download_rate":"25.00%","downloads":"5","impressions":"100","page_view_rate":"20.00%","page_views":"20"},"key":"App Store Search"},` +
		`{"current":{"download_rate":"0.00%","downloads":"0","impressions":"60","page_view_rate":"16.67%","page_views":"10"},"key":"App Store Browse"},` +
		`{"current":{"downloads":"1","impressions":"0","page_views":"0"},"key":"Web Referrer"}]`
	if s := tooltest.JSON(t, got["table"]); s != want {
		t.Errorf("table = %s", s)
	}
}
