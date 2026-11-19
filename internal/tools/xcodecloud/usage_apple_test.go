package xcodecloud

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// The kit's clock is 2026-01-02T03:04:05Z, so today is 2026-01-02.
const (
	uaTeam     = "1a2b3c4d-0000-1111-2222-333344445555"
	uaProduct  = "5e6f7a8b-0000-1111-2222-333344445555"
	uaProduct2 = "6e6f7a8b-0000-1111-2222-333344445555"
	uaProduct3 = "7e6f7a8b-0000-1111-2222-333344445555"
	uaWF1      = "aa000000-0000-1111-2222-333344445555"
	uaWF2      = "bb000000-0000-1111-2222-333344445555"
	uaBase     = "/ci/api/teams/" + uaTeam
	uaSummary  = uaBase + "/usage/summary"
	uaFull     = "?start=2025-12-03&end=2026-01-02"
	uaPlanDoc  = `{"plan":{"name":"Plan","available":%A,"used":%U,"total":%T,"reset_date":"%D","reset_date_time":"%R"},"links":{"manage":"https://developer.apple.com/xcode-cloud/"}}`
)

// uaPlan fills uaPlanDoc.
func uaPlan(available, used, total, resetDate, resetDateTime string) string {
	return strings.NewReplacer("%A", available, "%U", used, "%T", total, "%D", resetDate, "%R", resetDateTime).Replace(uaPlanDoc)
}

// uaKit is a signed-in kit whose team has the given summary and, in the
// public API, no Xcode Cloud product.
func uaKit(t *testing.T, summary string) *tooltest.Kit {
	t.Helper()
	k := uaKitWith(t, summary)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, `{"data":[]}`)
	return k
}

// uaKitWith is uaKit without the public products, for a test that lists
// its own.
func uaKitWith(t *testing.T, summary string) *tooltest.Kit {
	t.Helper()
	k := kit(t).WebSession()
	k.Portal.HandleJSON(http.MethodGet, "/olympus/v1/session", `{"provider":{"providerId":4242424,"publicProviderId":"`+uaTeam+`"}}`)
	k.Portal.HandleJSON(http.MethodGet, uaSummary, summary)
	return k
}

// uaTeamDays answers the team's days call for exactly the window query.
func uaTeamDays(k *tooltest.Kit, query, body string) {
	k.Portal.HandleJSON(http.MethodGet, uaBase+"/usage/days"+query, body)
}

func uaCall(t *testing.T, k *tooltest.Kit, args string) usageOut {
	t.Helper()
	res := tooltest.MustOK(t, tooltest.Call(t, lookup(t, "xcc_usage_report"), k.Env, args))
	return res.Data.(usageOut)
}

func TestUsageApplePlan(t *testing.T) {
	cases := []struct {
		name                      string
		available, used, total    string
		resetDate, resetDateTime  string
		wantExhausted             bool
		wantReset                 string
		wantTotal, wantUsed, left int
	}{
		{"left", "300", "1200", "1500", "2026-02-01", "2026-02-01T00:00:00Z", false, "2026-02-01T00:00:00Z", 1500, 1200, 300},
		{"used up", "0", "1500", "1500", "2026-02-01", "2026-02-01T00:00:00Z", true, "2026-02-01T00:00:00Z", 1500, 1500, 0},
		{"overdrawn", "-20", "1520", "1500", "2026-02-01", "", true, "2026-02-01", 1500, 1520, -20},
		{"no plan", "0", "0", "0", "", "", false, "", 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := uaKit(t, uaPlan(c.available, c.used, c.total, c.resetDate, c.resetDateTime))
			// A reset date that is no timestamp gives the longest window.
			query := uaFull
			if c.resetDateTime != "" {
				query = "?start=2026-01-01&end=2026-01-02"
			}
			uaTeamDays(k, query, `{"usage":[]}`)
			out := uaCall(t, k, `{"source":"apple"}`)
			p := out.Plan
			if p == nil || p.Exhausted != c.wantExhausted || p.ResetDate != c.wantReset || p.TotalMinutes != c.wantTotal ||
				p.UsedMinutes != c.wantUsed || p.AvailableMinutes != c.left || p.Name != "Plan" {
				t.Fatalf("plan = %+v", p)
			}
			if out.Source != usageApple {
				t.Fatalf("source = %q", out.Source)
			}
		})
	}
}

// The default window starts on the UTC day a month before the next reset;
// a reset that does not parse, a last reset after today, or one more than
// maxUsageDays back gives the longest window.
func TestUsageAppleDefaultWindow(t *testing.T) {
	cases := []struct {
		name, reset, from string
	}{
		{"since last reset", "2026-01-20T08:00:00Z", "2025-12-20"},
		{"reset's UTC day", "2026-01-20T23:30:00-05:00", "2025-12-21"},
		{"exactly the longest back", "2026-01-02T00:00:00Z", "2025-12-02"},
		{"not a timestamp", "2026-01-20", "2025-12-03"},
		{"last reset after today", "2026-02-10T00:00:00Z", "2025-12-03"},
		{"last reset too far back", "2026-01-01T00:00:00Z", "2025-12-03"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := uaKit(t, uaPlan("10", "20", "30", "", c.reset))
			uaTeamDays(k, "?start="+c.from+"&end=2026-01-02", `{"usage":[{"date":"2026-01-01","minutes":5,"number_of_builds":1}]}`)
			out := uaCall(t, k, `{"source":"apple"}`)
			if out.From != c.from || out.To != "2026-01-02" || out.Minutes != 5 {
				t.Fatalf("out = %+v", out)
			}
		})
	}
}

func TestUsageAppleGivenDays(t *testing.T) {
	k := uaKit(t, uaPlan("10", "20", "30", "", "2026-01-20T08:00:00Z"))
	uaTeamDays(k, "?start=2025-12-27&end=2026-01-02", `{"usage":[]}`)
	out := uaCall(t, k, `{"source":"apple","days":7}`)
	if out.From != "2025-12-27" || out.To != "2026-01-02" {
		t.Fatalf("window = %s…%s", out.From, out.To)
	}
}

// The team: days sorted and summed, products by minutes then id, named by
// id in either letter case; the pinned app does not narrow the list.
func TestUsageAppleTeam(t *testing.T) {
	k := uaKitWith(t, uaPlan("100", "400", "500", "", "2026-01-20T08:00:00Z"))
	k.Env.Profile.DefaultApp = "Hjul"
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, `{"data":[
	 {"type":"ciProducts","id":"`+uaProduct+`","attributes":{"name":"Hjul"}},
	 {"type":"ciProducts","id":"`+uaProduct2+`","attributes":{"name":"Sikt"}}]}`)
	uaTeamDays(k, "?start=2025-12-20&end=2026-01-02", `{
	 "usage":[{"date":"2026-01-01","minutes":30,"number_of_builds":2},
	  {"date":"2025-12-21","minutes":0,"number_of_builds":0},
	  {"date":"2025-12-22","minutes":45,"number_of_builds":3},
	  {"date":"2025-12-30","minutes":0,"number_of_builds":1}],
	 "product_usage":[
	  {"product_id":"`+strings.ToUpper(uaProduct2)+`","usage_in_minutes":30,"usage_in_seconds":1800,"number_of_builds":3,"previous_usage_in_minutes":12,"previous_number_of_builds":2},
	  {"product_id":"`+strings.ToUpper(uaProduct3)+`","usage_in_minutes":30,"number_of_builds":1},
	  {"product_id":"`+uaProduct+`","usage_in_minutes":15,"number_of_builds":2}],
	 "info":{"can_view_all_products":true}}`)
	out := uaCall(t, k, `{"source":"apple"}`)

	if out.Minutes != 75 || out.Builds != 6 {
		t.Fatalf("sums = %d min, %d builds", out.Minutes, out.Builds)
	}
	want := []usageDay{{"2025-12-22", 45, 3}, {"2025-12-30", 0, 1}, {"2026-01-01", 30, 2}}
	if len(out.Days) != len(want) {
		t.Fatalf("days = %+v", out.Days)
	}
	for i, d := range want {
		if out.Days[i] != d {
			t.Fatalf("days = %+v", out.Days)
		}
	}
	p := out.Products
	if len(p) != 3 || p[0].ID != strings.ToUpper(uaProduct2) || p[1].ID != strings.ToUpper(uaProduct3) || p[2].ID != uaProduct {
		t.Fatalf("products = %+v", p)
	}
	if p[0].Name != "Sikt" || p[1].Name != "" || p[2].Name != "Hjul" {
		t.Fatalf("names = %q %q %q", p[0].Name, p[1].Name, p[2].Name)
	}
	if p[0].PreviousMinutes != 12 || p[0].PreviousBuilds != 2 || p[0].Minutes != 30 || p[0].Builds != 3 {
		t.Fatalf("product = %+v", p[0])
	}
	if len(out.Workflows) != 0 {
		t.Fatalf("workflows = %+v", out.Workflows)
	}
	wantLog(t, k, "GET /v1/ciProducts")
	if q := k.ASC.Requests()[0].Query; q.Get("filter[app]") != "" || q.Get("fields[ciProducts]") != "name" {
		t.Fatalf("products query = %v", q)
	}
}

// One product: its days, its workflows by minutes then id, named in either
// letter case, and the team's plan.
func TestUsageAppleProduct(t *testing.T) {
	k := uaKit(t, uaPlan("0", "500", "500", "", "2026-01-20T08:00:00Z"))
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/{id}", http.StatusOK,
		`{"data":{"type":"ciProducts","id":"`+uaProduct+`","attributes":{"name":"Hjul"}}}`)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/{id}/workflows", http.StatusOK, `{"data":[
	 {"type":"ciWorkflows","id":"`+uaWF1+`","attributes":{"name":"Release"}},
	 {"type":"ciWorkflows","id":"`+uaWF2+`","attributes":{"name":"Nightly"}}]}`)
	k.Portal.HandleJSON(http.MethodGet, uaBase+"/products/"+uaProduct+"/usage/days?start=2025-12-20&end=2026-01-02", `{
	 "usage":[{"date":"2026-01-02","minutes":10,"number_of_builds":1},{"date":"2025-12-28","minutes":20,"number_of_builds":2}],
	 "workflow_usage":[
	  {"workflow_id":"`+strings.ToUpper(uaWF2)+`","usage_in_minutes":10,"number_of_builds":2},
	  {"workflow_id":"`+strings.ToUpper(uaWF1)+`","usage_in_minutes":10,"number_of_builds":1,"previous_usage_in_minutes":40,"previous_number_of_builds":4},
	  {"workflow_id":"cc000000-0000-1111-2222-333344445555","usage_in_minutes":10,"number_of_builds":0}],
	 "info":{"current":{"builds":3,"used":30,"average_30_days":900},"previous":{"builds":4,"used":40,"average_30_days":1200}}}`)
	out := uaCall(t, k, `{"source":"apple","product":"`+uaProduct+`"}`)

	if out.Minutes != 30 || out.Builds != 3 || len(out.Days) != 2 || out.Days[0].Date != "2025-12-28" {
		t.Fatalf("out = %+v", out)
	}
	if len(out.Products) != 1 || out.Products[0] != (usageProduct{ID: uaProduct, Name: "Hjul", Minutes: 30, Builds: 3}) {
		t.Fatalf("products = %+v", out.Products)
	}
	w := out.Workflows
	if len(w) != 3 || w[0].ID != strings.ToUpper(uaWF1) || w[1].ID != strings.ToUpper(uaWF2) || w[2].Name != "" {
		t.Fatalf("workflows = %+v", w)
	}
	if w[0].Name != "Release" || w[1].Name != "Nightly" || w[0].PreviousMinutes != 40 || w[0].PreviousBuilds != 4 {
		t.Fatalf("workflows = %+v", w)
	}
	if out.Plan == nil || !out.Plan.Exhausted {
		t.Fatalf("plan = %+v", out.Plan)
	}
}

// A product outside the web session's team is not found.
func TestUsageAppleProductNotFound(t *testing.T) {
	k := uaKit(t, uaPlan("10", "20", "30", "", "2026-01-20T08:00:00Z"))
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/{id}", http.StatusOK,
		`{"data":{"type":"ciProducts","id":"`+uaProduct+`","attributes":{"name":"Hjul"}}}`)
	k.Portal.HandleError(http.MethodGet, uaBase+"/products/"+uaProduct+"/usage/days?start=2025-12-20&end=2026-01-02", http.StatusNotFound)
	b := tooltest.MustFail(t, tooltest.Call(t, lookup(t, "xcc_usage_report"), k.Env, `{"source":"apple","product":"`+uaProduct+`"}`), "not_found")
	if !strings.Contains(b.Message, uaProduct) {
		t.Fatalf("message = %s", b.Message)
	}
}

// Without a usable session the error stays ErrWebSessionRequired, which
// the dispatcher falls back on; source apple reports it.
func TestUsageAppleNoWebSession(t *testing.T) {
	k := kit(t)
	tooltest.MustFail(t, tooltest.Call(t, lookup(t, "xcc_usage_report"), k.Env, `{"source":"apple"}`), "web_session_required")

	// A session that expires at the summary.
	k = kit(t).WebSession()
	k.Portal.HandleJSON(http.MethodGet, "/olympus/v1/session", `{"provider":{"providerId":4242424,"publicProviderId":"`+uaTeam+`"}}`)
	k.Portal.Handle(http.MethodGet, uaSummary, func(tooltest.PortalCall) (string, error) {
		return "", tool.ErrWebSessionRequired
	})
	_, err := usageFromApple(context.Background(), k.Env, 0, k.Clock.Now().UTC(), nil)
	if !errors.Is(err, tool.ErrWebSessionRequired) {
		t.Fatalf("err = %v", err)
	}
	tooltest.MustFail(t, tooltest.Call(t, lookup(t, "xcc_usage_report"), k.Env, `{"source":"apple"}`), "web_session_required")
}
