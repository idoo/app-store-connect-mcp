package reports

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

// insightsKit serves last_7_days at fixedNow (Thu 2026-10-22): current
// 2026-10-15..21 (21 not published yet), previous 2026-10-08..14.
func insightsKit(t *testing.T) *tooltest.Kit {
	k := reportsKit(t).ReadOnly()
	day1, day2 := gz(t, "sales_day1.tsv"), gz(t, "sales_day2.tsv")
	k.ASC.HandleFunc(http.MethodGet, "/v1/salesReports", func(req tool.Request) tooltest.Fixture {
		d := req.Query.Get("filter[reportDate]")
		switch {
		case d == "2026-10-21":
			return tooltest.Fixture{Status: http.StatusNotFound, Body: []byte(notFound)}
		case d >= "2026-10-15":
			return tooltest.Fixture{Header: http.Header{"Content-Type": {"application/a-gzip"}}, Body: day1}
		default:
			return tooltest.Fixture{Header: http.Header{"Content-Type": {"application/a-gzip"}}, Body: day2}
		}
	})
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/customerReviews", http.StatusOK, doc(t,
		res("customerReviews", "rv-1", map[string]any{"rating": 5, "createdDate": "2026-10-20T10:00:00-07:00"}),
		res("customerReviews", "rv-2", map[string]any{"rating": 2, "createdDate": "2026-10-16T09:00:00-07:00"}),
		res("customerReviews", "rv-3", map[string]any{"rating": 4, "createdDate": "2026-10-10T09:00:00-07:00"}),
		res("customerReviews", "rv-4", map[string]any{"rating": 1, "createdDate": "2026-10-01T09:00:00-07:00"}),
	))
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appStoreVersions", http.StatusOK, `{"data":[`+
		`{"type":"appStoreVersions","id":"v13","attributes":{"platform":"IOS","versionString":"1.3","appVersionState":"READY_FOR_DISTRIBUTION"},`+
		`"relationships":{"appStoreVersionPhasedRelease":{"data":{"type":"appStoreVersionPhasedReleases","id":"ph-1"}}}},`+
		`{"type":"appStoreVersions","id":"v14","attributes":{"platform":"IOS","versionString":"1.4","appVersionState":"WAITING_FOR_REVIEW"}},`+
		`{"type":"appStoreVersions","id":"v12","attributes":{"platform":"IOS","versionString":"1.2","appVersionState":"REPLACED_WITH_NEW_VERSION"}}],`+
		`"included":[{"type":"appStoreVersionPhasedReleases","id":"ph-1","attributes":{"phasedReleaseState":"ACTIVE","currentDayNumber":3}}],`+
		`"links":{"self":"https://api.appstoreconnect.apple.com/v1/x"}}`)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/betaFeedbackScreenshotSubmissions", http.StatusOK, doc(t,
		res("betaFeedbackScreenshotSubmissions", "fb-1", map[string]any{"createdDate": "2026-10-21T08:00:00Z"}),
		res("betaFeedbackScreenshotSubmissions", "fb-2", map[string]any{"createdDate": "2026-10-02T08:00:00Z"}),
	))
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/betaFeedbackCrashSubmissions", http.StatusOK, emptyList)
	k.ASC.Handle(http.MethodGet, requestsPath, http.StatusOK, emptyList)
	return k
}

func TestInsightsSummaryWithSales(t *testing.T) {
	k := insightsKit(t)
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "insights_summary"), k.Env, ``))
	got := tooltest.Decode[map[string]any](t, r)
	if tooltest.JSON(t, got["current"]) != `{"from":"2026-10-15","to":"2026-10-21"}` ||
		tooltest.JSON(t, got["previous"]) != `{"from":"2026-10-08","to":"2026-10-14"}` || got["period"] != "last_7_days" {
		t.Errorf("windows = %s / %s", tooltest.JSON(t, got["current"]), tooltest.JSON(t, got["previous"]))
	}
	sales := got["sales"].(map[string]any)
	// 6 published current days (21 pending) vs the first 6 previous days.
	if sales["status"] != "ok" || sales["compared_days"] != 6.0 || tooltest.JSON(t, sales["pending_days"]) != `["2026-10-21"]` {
		t.Errorf("sales = %s", tooltest.JSON(t, sales))
	}
	if s := tooltest.JSON(t, sales["units"]); s != `{"download":102,"iap":30,"redownload":24,"subscription":42,"update":180}` {
		t.Errorf("units = %s", s)
	}
	if s := tooltest.JSON(t, sales["previous_units"]); s != `{"download":18,"iap":18}` {
		t.Errorf("previous_units = %s", s)
	}
	if s := tooltest.JSON(t, sales["units_change"]); s != `{"download":"+466.7%","iap":"+66.7%"}` {
		t.Errorf("units_change = %s", s)
	}
	if s := tooltest.JSON(t, sales["proceeds"]); s != `[{"amount":"2520","currency":"JPY","previous":"0"},`+
		`{"amount":"113.04","change":"+2591.4%","currency":"USD","previous":"4.20"},`+
		`{"amount":"8.28","change":"-44.8%","currency":"EUR","previous":"15.00"}]` {
		t.Errorf("proceeds = %s", s)
	}
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/salesReports")); n != 14 {
		t.Errorf("sales requests = %d, want 14", n)
	}

	ratings := got["ratings"].(map[string]any)
	if ratings["status"] != "ok" || ratings["new_reviews"] != 2.0 || ratings["average"] != "3.50" ||
		ratings["previous_new_reviews"] != 1.0 || ratings["previous_average"] != "4.00" || tooltest.JSON(t, ratings["stars"]) != `[0,1,0,0,1]` {
		t.Errorf("ratings = %s", tooltest.JSON(t, ratings))
	}
	if s := tooltest.JSON(t, got["release"]); s != `{"in_flight":[{"id":"v14","platform":"IOS","state":"WAITING_FOR_REVIEW","version":"1.4"}],`+
		`"live":{"id":"v13","phased_day":3,"phased_release":"ACTIVE","platform":"IOS","state":"READY_FOR_DISTRIBUTION","version":"1.3"},"status":"ok"}` {
		t.Errorf("release = %s", s)
	}
	if s := tooltest.JSON(t, got["testflight"]); s != `{"crash_feedback":0,"screenshot_feedback":1,"status":"ok"}` {
		t.Errorf("testflight = %s", s)
	}
	an := got["analytics"].(map[string]any)
	if an["status"] != "unavailable" || !strings.Contains(an["reason"].(string), "analytics_setup") {
		t.Errorf("analytics = %s", tooltest.JSON(t, an))
	}
	if len(r.Next) == 0 || r.Next[0].Tool != "analytics_setup" {
		t.Errorf("next = %+v", r.Next)
	}
	rv := k.ASC.RequestsTo(http.MethodGet, "/v1/apps/1000000001/customerReviews")
	if len(rv) != 1 || rv[0].Query.Get("sort") != "-createdDate" {
		t.Errorf("review requests = %+v", rv)
	}
}

func TestInsightsSummaryVendorMissing(t *testing.T) {
	k := insightsKit(t)
	k.Env.Profile.VendorNumber = ""
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "insights_summary"), k.Env, `{"period":"last_week"}`))
	got := tooltest.Decode[map[string]any](t, r)
	sales := got["sales"].(map[string]any)
	if sales["status"] != "unavailable" || !strings.Contains(sales["reason"].(string), "vendor number") {
		t.Errorf("sales = %s", tooltest.JSON(t, sales))
	}
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/salesReports")); n != 0 {
		t.Errorf("sales requests = %d", n)
	}
	if got["ratings"].(map[string]any)["status"] != "ok" || got["release"].(map[string]any)["status"] != "ok" {
		t.Errorf("other sections must still work: %s", tooltest.JSON(t, got))
	}

	ind := insightsKit(t).Individual()
	r = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "insights_summary"), ind.Env, ``))
	if s := tooltest.Decode[map[string]any](t, r)["sales"].(map[string]any); !strings.Contains(s["reason"].(string), "Team API key") {
		t.Errorf("individual sales = %v", s)
	}
}

// last_month March (31 days) vs February (28 days) at a flat daily rate must
// not read as growth: both windows are cut to 28 days and both lengths shown.
func TestInsightsLastMonthEqualLengthWindows(t *testing.T) {
	k := reportsKit(t).ReadOnly()
	k.Clock.Set(time.Date(2026, 4, 10, 19, 0, 0, 0, time.UTC))
	day := gz(t, "sales_day1.tsv")
	k.ASC.HandleFunc(http.MethodGet, "/v1/salesReports", func(tool.Request) tooltest.Fixture {
		return tooltest.Fixture{Header: http.Header{"Content-Type": {"application/a-gzip"}}, Body: day}
	})
	for _, p := range []string{
		"/v1/apps/{id}/customerReviews", "/v1/apps/{id}/appStoreVersions",
		"/v1/apps/{id}/betaFeedbackScreenshotSubmissions", "/v1/apps/{id}/betaFeedbackCrashSubmissions", requestsPath,
	} {
		k.ASC.Handle(http.MethodGet, p, http.StatusOK, emptyList)
	}
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "insights_summary"), k.Env, `{"period":"last_month"}`))
	sales := tooltest.Decode[map[string]any](t, r)["sales"].(map[string]any)
	if sales["compared_days"] != 28.0 || sales["current_days"] != 31.0 || sales["previous_days"] != 28.0 {
		t.Fatalf("sales = %s", tooltest.JSON(t, sales))
	}
	if cur, prev := tooltest.JSON(t, sales["units"]), tooltest.JSON(t, sales["previous_units"]); cur != prev {
		t.Errorf("flat rate compared unequal windows: units %s vs previous %s", cur, prev)
	}
	for c, ch := range sales["units_change"].(map[string]any) {
		if ch != "+0.0%" && ch != "0.0%" {
			t.Errorf("units_change[%s] = %v, want no change", c, ch)
		}
	}
	if !strings.Contains(sales["reason"].(string), "first 28 days") {
		t.Errorf("reason = %v", sales["reason"])
	}
}
