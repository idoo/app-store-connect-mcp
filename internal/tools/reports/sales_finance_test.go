package reports

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/reports"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// fixedNow is Thursday 2026-10-22 12:00 Pacific: this_week = Mon 19 … Thu 22.
var fixedNow = time.Date(2026, 10, 22, 19, 0, 0, 0, time.UTC)

const notFound = `{"errors":[{"status":"404","code":"NOT_FOUND","title":"The specified resource does not exist","detail":"There were no sales for the date specified."}]}`

// notYet is Apple's 404 for a report that is not published yet.
const notYet = `{"errors":[{"status":"404","code":"NOT_FOUND","title":"The specified resource does not exist","detail":"The request expected results but none were found - Report is not available yet."}]}`

func reportsKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t)
	k.Clock.Set(fixedNow)
	k.Resolve.Apps = []tool.App{
		{ID: "1000000001", BundleID: "com.example.notes", Name: "Example Notes", SKU: "com.example.notes"},
		{ID: "2000000001", BundleID: "com.other.app", Name: "Other App", SKU: "com.other.app"},
	}
	return k
}

// gz returns the gzip bytes of a testdata/sales fixture.
func gz(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "sales", name))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func handleGz(t *testing.T, k *tooltest.Kit, pattern, file string) {
	t.Helper()
	k.ASC.HandleFixture(http.MethodGet, pattern, tooltest.Fixture{
		Header: http.Header{"Content-Type": {"application/a-gzip"}}, Body: gz(t, file),
	})
}

func mustTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	return tl
}

func thisWeekKit(t *testing.T) *tooltest.Kit {
	k := reportsKit(t)
	handleGz(t, k, "/v1/salesReports?filter[reportDate]=2026-10-19", "sales_day1.tsv")
	handleGz(t, k, "/v1/salesReports?filter[reportDate]=2026-10-20", "sales_day2.tsv")
	k.ASC.Handle(http.MethodGet, "/v1/salesReports?filter[reportDate]=2026-10-21", http.StatusNotFound, notYet)
	return k
}

func TestSalesReportThisWeek(t *testing.T) {
	k := thisWeekKit(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"this_week","group_by":["app"]}`))
	got := tooltest.Decode[map[string]any](t, res)

	if s := tooltest.JSON(t, got["covered_days"]); s != `["2026-10-19","2026-10-20"]` {
		t.Errorf("covered_days = %s", s)
	}
	if s := tooltest.JSON(t, got["pending_days"]); s != `["2026-10-21","2026-10-22"]` {
		t.Errorf("pending_days = %s (404 day and today must be pending)", s)
	}
	if s := tooltest.JSON(t, got["units"]); s != `{"download":120,"iap":8,"redownload":4,"subscription":7,"update":30}` {
		t.Errorf("units = %s", s)
	}
	// MA3: the refund row (units −1) is reported next to the net units.
	if s := tooltest.JSON(t, got["refunds"]); s != `{"subscription":1}` {
		t.Errorf("refunds = %s", s)
	}
	// Exact per-currency sums, sorted by currency code, never summed across currencies.
	if s := tooltest.JSON(t, got["proceeds"]); s != `[{"amount":"3.88","currency":"EUR"},{"amount":"420","currency":"JPY"},{"amount":"19.54","currency":"USD"}]` {
		t.Errorf("proceeds = %s", s)
	}
	if _, ok := got["proceeds_estimate_usd"]; ok {
		t.Error("USD estimate present")
	}
	groups := got["groups"].([]any)
	if len(groups) != 2 || tooltest.JSON(t, groups[0]) != `{"key":{"app":"com.other.app"},"proceeds":{"USD":"0.00"},"units":{"download":100}}` ||
		tooltest.JSON(t, groups[1].(map[string]any)["proceeds"]) != `{"EUR":"3.88","JPY":"420","USD":"19.54"}` {
		t.Errorf("groups = %s", tooltest.JSON(t, groups))
	}

	reqs := k.ASC.RequestsTo(http.MethodGet, "/v1/salesReports")
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want 3 (today is never fetched)", len(reqs))
	}
	for _, r := range reqs {
		q := r.Query
		if q.Get("filter[vendorNumber]") != "12345678" || q.Get("filter[reportType]") != "SALES" ||
			q.Get("filter[reportSubType]") != "SUMMARY" || q.Get("filter[frequency]") != "DAILY" ||
			q.Get("filter[version]") != "1_0" || r.Accept != "application/a-gzip" {
			t.Errorf("query = %s accept=%q", q.Encode(), r.Accept)
		}
	}
}

// RB2-4: only a 404 saying "no sales" is a published zero-sales day. Any
// other 404 stays pending whatever its age, with a hint that Apple has not
// published it yet.
func TestSalesReportNoSalesDays(t *testing.T) {
	k := reportsKit(t)
	handleGz(t, k, "/v1/salesReports?filter[reportDate]=2026-10-18", "sales_day1.tsv")
	k.ASC.Handle(http.MethodGet, "/v1/salesReports?filter[reportDate]=2026-10-19", http.StatusNotFound, notYet)
	k.ASC.Handle(http.MethodGet, "/v1/salesReports?filter[reportDate]=2026-10-20", http.StatusNotFound, notFound)
	k.ASC.Handle(http.MethodGet, "/v1/salesReports?filter[reportDate]=2026-10-21", http.StatusNotFound, notFound)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"from":"2026-10-18","to":"2026-10-21"}`))
	got := tooltest.Decode[map[string]any](t, res)
	if s := tooltest.JSON(t, got["covered_days"]); s != `["2026-10-18","2026-10-20","2026-10-21"]` {
		t.Errorf("covered_days = %s", s)
	}
	if s := tooltest.JSON(t, got["no_sales_days"]); s != `["2026-10-20","2026-10-21"]` {
		t.Errorf("no_sales_days = %s", s)
	}
	if s := tooltest.JSON(t, got["pending_days"]); s != `["2026-10-19"]` {
		t.Errorf("pending_days = %s (a 3-day-old 404 without \"no sales\" is pending)", s)
	}
	if w := strings.Join(res.Meta.Warnings, "\n"); !strings.Contains(w, "hasn't published the report for 2026-10-19") {
		t.Errorf("warnings = %v", res.Meta.Warnings)
	}
}

// RB2-3: when the latest days had no SUBSCRIPTION report, a warning says
// which day the active-subscription counts come from.
func TestSalesReportSubscriptionSnapshotDayWarning(t *testing.T) {
	k := reportsKit(t)
	handleGz(t, k, "/v1/salesReports?filter[reportDate]=2026-10-19", "subscription_day.tsv")
	k.ASC.Handle(http.MethodGet, "/v1/salesReports?filter[reportDate]=2026-10-20", http.StatusNotFound, notFound)
	k.ASC.Handle(http.MethodGet, "/v1/salesReports?filter[reportDate]=2026-10-21", http.StatusNotFound, notYet)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env,
		`{"report_type":"SUBSCRIPTION","from":"2026-10-19","to":"2026-10-21"}`))
	got := tooltest.Decode[map[string]any](t, res)
	if got["snapshot_day"] != "2026-10-19" || got["subscriptions"] == nil {
		t.Fatalf("result = %s", tooltest.JSON(t, got))
	}
	w := strings.Join(res.Meta.Warnings, "\n")
	if !strings.Contains(w, "active-subscription counts are from 2026-10-19") || !strings.Contains(w, "2026-10-20, 2026-10-21 had none") {
		t.Errorf("warnings = %v", res.Meta.Warnings)
	}
	// A snapshot on the last day of the window needs no warning.
	k2 := reportsKit(t)
	handleGz(t, k2, "/v1/salesReports?filter[reportDate]=2026-10-21", "subscription_day.tsv")
	res = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "sales_report"), k2.Env,
		`{"report_type":"SUBSCRIPTION","from":"2026-10-21","to":"2026-10-21"}`))
	if len(res.Meta.Warnings) != 0 {
		t.Errorf("warnings = %v", res.Meta.Warnings)
	}
}

// A sales 500 or finance 403 hint keeps the original cause and adds the
// wrong-vendor-number cause.
func TestReportErrorsMentionVendorNumber(t *testing.T) {
	for _, c := range []struct{ tool, path, args string }{
		{"sales_report", "/v1/salesReports", `{"frequency":"DAILY","report_date":"2026-10-20"}`},
		{"finance_report", "/v1/financeReports", `{"month":"2026-08"}`},
	} {
		k := reportsKit(t)
		status, body := http.StatusForbidden, `{"errors":[{"status":"403","code":"FORBIDDEN_ERROR","title":"forbidden","detail":"not allowed"}]}`
		if c.tool == "sales_report" {
			status, body = http.StatusInternalServerError, `{"errors":[{"status":"500","code":"UNEXPECTED_ERROR","title":"error","detail":"oops"}]}`
		}
		k.ASC.Handle(http.MethodGet, c.path, status, body)
		eb := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, c.tool), k.Env, c.args), "api_error")
		if eb.API == nil || eb.API.Status != status || !strings.Contains(eb.Hint, "vendor number 12345") {
			t.Errorf("%s: body = %+v", c.tool, eb)
		}
	}
	// An existing hint (e.g. system-status for a 500) is kept.
	err := vendorCause(fmt.Errorf("wrap: %w", &tool.APIError{Status: 500, Hint: "check status"}), "9")
	var api *tool.APIError
	if !errors.As(err, &api) || !strings.HasPrefix(api.Hint, "check status; or the vendor number 9") {
		t.Errorf("hint = %q", api.Hint)
	}
}

// A finance 403 with a vendor number Apple supplied (web session) is the
// key's role, never another account's number.
func TestFinanceReportForbiddenWithWebVendorNamesRole(t *testing.T) {
	k := webVendorKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/financeReports", http.StatusForbidden,
		`{"errors":[{"status":"403","code":"FORBIDDEN_ERROR","title":"forbidden","detail":"not allowed"}]}`)
	eb := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, `{"month":"2026-08"}`), "api_error")
	if eb.API == nil || eb.API.Status != http.StatusForbidden || strings.Contains(eb.Hint, "vendor number") ||
		!strings.Contains(eb.Hint, "Finance or Admin role") || !strings.Contains(eb.Hint, "Users and Access › Integrations") ||
		!strings.Contains(eb.Hint, "sales_report works") {
		t.Errorf("body = %+v", eb)
	}
}

func TestSalesReportAppFilter(t *testing.T) {
	k := thisWeekKit(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env,
		`{"period":"this_week","app":"Example Notes"}`))
	got := tooltest.Decode[map[string]any](t, res)
	if s := tooltest.JSON(t, got["units"]); s != `{"download":20,"iap":8,"redownload":4,"subscription":7,"update":30}` {
		t.Errorf("units = %s (other app must be filtered out, IAPs kept via parent SKU)", s)
	}
	if id := got["app"].(map[string]any)["id"]; id != "1000000001" {
		t.Errorf("app = %v", id)
	}
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/financeReports")); n != 0 {
		t.Errorf("sales_report fetched %d finance reports", n)
	}
	// The USD estimate (built on an exchange-rate column Apple's reports do
	// not have) is gone; the argument is rejected, not silently ignored.
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env,
		`{"period":"this_week","with_usd_estimate":true}`), "invalid_arguments")
}

// Subscription event and subscriber rows match the app by App Apple ID, so a
// renamed app keeps its rows; the name is only a fallback without that column.
func TestSubscriptionRowsMatchByAppAppleID(t *testing.T) {
	app := &tool.App{ID: "1000000001", Name: "Example Notes"}
	cases := []struct {
		id, name string
		want     bool
	}{
		{"1000000001", "Old Name Before Rename", true},
		{"2000000001", "Example Notes", false},
		{"", "example notes", true},
		{"", "Other App", false},
	}
	for _, c := range cases {
		if got := subscriptionRowMatches(c.id, c.name, app); got != c.want {
			t.Errorf("match(%q, %q) = %v, want %v", c.id, c.name, got, c.want)
		}
	}
	tab, err := reports.Decode(strings.NewReader("Event Date\tEvent\tApp Name\tApp Apple ID\tQuantity\n"+
		"2026-10-19\tRefund\tOld Name\t1000000001\t2\n2026-10-19\tRefund\tExample Notes\t2000000001\t5\n"), reports.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var out salesOut
	if err := subscriptionEvents(&out, []dayTable{{Date: "2026-10-19", Table: tab}}, app); err != nil {
		t.Fatal(err)
	}
	if out.Rows != 1 || out.Events.Refunds != 2 {
		t.Errorf("rows = %d events = %+v", out.Rows, out.Events)
	}
}

func TestSalesReportVendorMissing(t *testing.T) {
	k := reportsKit(t)
	k.Env.Profile.VendorNumber = ""
	body := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"yesterday"}`), "vendor_number_missing")
	if !strings.Contains(body.Hint, "app-store-connect-mcp auth set test vendor_number=") || !strings.Contains(body.Hint, "web_login") ||
		!strings.Contains(body.Message, "Payments and Financial Reports") {
		t.Errorf("body = %+v", body)
	}
	// Env-configured users get the command that works for them.
	k.Env.Profile.Name = "env"
	body = tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"yesterday"}`), "vendor_number_missing")
	if !strings.Contains(body.Hint, "app-store-connect-mcp auth set env vendor_number=<number>") {
		t.Errorf("env hint = %q", body.Hint)
	}
	if n := len(k.ASC.Requests()); n != 0 {
		t.Errorf("requests = %d", n)
	}
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, ``), "vendor_number_missing")
}

const (
	olympusPath = "/olympus/v1/session"
	vendorsPath = "/WebObjects/iTunesConnect.woa/ra/paymentConsolidation/providers/4242424/sapVendorNumbers"
)

// webVendorKit is signed in to the web session, with no vendor number in
// the profile and Payments listing one (plus an Arcade) vendor.
func webVendorKit(t *testing.T) *tooltest.Kit {
	k := reportsKit(t).WebSession()
	k.Env.Profile.VendorNumber = ""
	k.Portal.HandleJSON(http.MethodGet, olympusPath, `{"provider":{"providerId":4242424}}`)
	k.Portal.HandleJSON(http.MethodGet, vendorsPath,
		`{"data":[{"sapVendorNumber":55500,"isArcadeVendor":true},{"sapVendorNumber":81234567,"isArcadeVendor":false}]}`)
	return k
}

func TestSalesReportVendorFromWebSession(t *testing.T) {
	k := webVendorKit(t)
	handleGz(t, k, "/v1/salesReports?filter[reportDate]=2026-10-21", "sales_day1.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"yesterday"}`))
	if got := tooltest.Decode[map[string]any](t, res); got["vendor_number_source"] != "web_session" {
		t.Errorf("vendor_number_source = %v", got["vendor_number_source"])
	}
	reqs := k.ASC.RequestsTo(http.MethodGet, "/v1/salesReports")
	if len(reqs) != 1 || reqs[0].Query.Get("filter[vendorNumber]") != "81234567" {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestFinanceReportVendorFromWebSession(t *testing.T) {
	k := webVendorKit(t)
	handleGz(t, k, "/v1/financeReports?filter[reportDate]=2026-08", "financial.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, `{"month":"2026-08"}`))
	if got := tooltest.Decode[map[string]any](t, res); got["vendor_number_source"] != "web_session" {
		t.Errorf("vendor_number_source = %v", got["vendor_number_source"])
	}
	reqs := k.ASC.RequestsTo(http.MethodGet, "/v1/financeReports")
	if len(reqs) != 1 || reqs[0].Query.Get("filter[vendorNumber]") != "81234567" {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestReportsProfileVendorHasNoSource(t *testing.T) {
	k := reportsKit(t)
	handleGz(t, k, "/v1/financeReports?filter[reportDate]=2026-08", "financial.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, `{"month":"2026-08"}`))
	if got := tooltest.Decode[map[string]any](t, res); got["vendor_number_source"] != nil {
		t.Errorf("vendor_number_source = %v, want omitted for the profile's number", got["vendor_number_source"])
	}
}

func TestReportsVendorAmbiguous(t *testing.T) {
	k := reportsKit(t).WebSession()
	k.Env.Profile.VendorNumber = ""
	k.Portal.HandleJSON(http.MethodGet, olympusPath, `{"provider":{"providerId":4242424}}`)
	k.Portal.HandleJSON(http.MethodGet, vendorsPath, `{"data":[{"sapVendorNumber":111},{"sapVendorNumber":222}]}`)
	body := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"yesterday"}`), "vendor_number_ambiguous")
	if !strings.Contains(body.Message, "111") || !strings.Contains(body.Message, "222") {
		t.Errorf("message = %q", body.Message)
	}
	if n := len(k.ASC.Requests()); n != 0 {
		t.Errorf("requests = %d", n)
	}
}

func TestReportsWithheldForIndividualKey(t *testing.T) {
	k := reportsKit(t).Individual()
	for _, name := range []string{"sales_report", "finance_report"} {
		tl := mustTool(t, name)
		if tl.ListedIn(k.Env.Profile) {
			t.Errorf("%s listed for an individual key", name)
		}
		tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, ``), "individual_key_unsupported")
	}
	if n := len(k.ASC.Requests()); n != 0 {
		t.Errorf("requests = %d", n)
	}
}

func TestSalesReportExplicitFrequency(t *testing.T) {
	k := reportsKit(t)
	handleGz(t, k, "/v1/salesReports?filter[frequency]=WEEKLY", "sales_day1.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"frequency":"WEEKLY","report_date":"2026-10-18"}`))
	got := tooltest.Decode[map[string]any](t, res)
	if s := tooltest.JSON(t, got["covered_days"]); s != `["2026-10-18"]` {
		t.Errorf("covered = %s", s)
	}

	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"frequency":"WEEKLY","report_date":"2026-10-21"}`), "invalid_arguments")
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"yesterday","from":"2026-10-01","to":"2026-10-02"}`), "invalid_arguments")
	// SUBSCRIPTION is DAILY-only; FIRST_ANNUAL has no DAILY report.
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"report_type":"FIRST_ANNUAL"}`), "invalid_arguments")
	if n := len(k.ASC.Requests()); n != 1 {
		t.Errorf("requests = %d, invalid calls must not reach Apple", n)
	}
}

func TestSalesReportNon404IsError(t *testing.T) {
	k := reportsKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/salesReports", http.StatusBadRequest,
		`{"errors":[{"status":"400","code":"PARAMETER_ERROR.INVALID","title":"bad vendor"}]}`)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"yesterday"}`), "api_error")
}

func TestFinanceReportPerCurrencyOnly(t *testing.T) {
	k := reportsKit(t)
	handleGz(t, k, "/v1/financeReports?filter[reportDate]=2026-09", "finance_detail.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, `{"report_type":"FINANCE_DETAIL"}`))
	got := tooltest.Decode[map[string]any](t, res)
	if got["status"] != "available" || got["region"] != "Z1" || got["month"] != "2026-09" || got["units"] != 13.0 {
		t.Errorf("got = %s", tooltest.JSON(t, got))
	}
	for _, k := range []string{"total_usd", "fx_rates_to_usd", "missing_rates", "total_usd_source"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s present: Apple's finance reports have no exchange rates", k)
		}
	}
	if note, _ := got["note"].(string); !strings.Contains(note, "Payments page") {
		t.Errorf("note = %q", note)
	}
	if s := tooltest.JSON(t, got["proceeds"]); s != `[{"amount":"1.38","currency":"EUR"},{"amount":"420","currency":"JPY"},{"amount":"27.90","currency":"USD"}]` {
		t.Errorf("proceeds = %s", s)
	}
	r := k.ASC.Requests()[0].Query
	if r.Get("filter[reportType]") != "FINANCE_DETAIL" || r.Get("filter[vendorNumber]") != "12345678" {
		t.Errorf("query = %s", r.Encode())
	}
}

func TestFinanceReportExplicitMonth(t *testing.T) {
	k := reportsKit(t)
	handleGz(t, k, "/v1/financeReports?filter[reportDate]=2026-08", "financial.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, `{"month":"2026-08"}`))
	got := tooltest.Decode[map[string]any](t, res)
	if s := tooltest.JSON(t, got["proceeds"]); s != `[{"amount":"1.38","currency":"EUR"},{"amount":"420","currency":"JPY"},{"amount":"27.21","currency":"USD"}]` {
		t.Errorf("proceeds = %s", s)
	}
	if got["region"] != "ZZ" || got["report_type"] != "FINANCIAL" {
		t.Errorf("defaults = %s", tooltest.JSON(t, got))
	}
	if n := len(k.ASC.Requests()); n != 1 {
		t.Errorf("requests = %d, an explicit month is fetched once", n)
	}
}

// Without a month, the newest closed month is tried first and, when Apple
// has not published it yet, the month before: at most two calls.
func TestFinanceReportDefaultMonthFallsBack(t *testing.T) {
	k := reportsKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/financeReports?filter[reportDate]=2026-09", http.StatusNotFound, notFound)
	handleGz(t, k, "/v1/financeReports?filter[reportDate]=2026-08", "financial.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, ``))
	got := tooltest.Decode[map[string]any](t, res)
	if got["status"] != "available" || got["month"] != "2026-08" {
		t.Errorf("got = %s", tooltest.JSON(t, got))
	}
	if w := strings.Join(res.Meta.Warnings, ";"); !strings.Contains(w, "2026-09 is not published yet") {
		t.Errorf("warnings = %q", w)
	}
	if n := len(k.ASC.Requests()); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestFinanceReportPending(t *testing.T) {
	k := reportsKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/financeReports", http.StatusNotFound, notFound)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, ``))
	got := tooltest.Decode[map[string]any](t, res)
	if got["status"] != "pending" || got["month"] != "2026-08" || !strings.Contains(got["explanation"].(string), "2026-09 or 2026-08") {
		t.Errorf("got = %s", tooltest.JSON(t, got))
	}
	if len(res.Next) != 1 || res.Next[0].Args["month"] != "2026-07" {
		t.Errorf("next = %+v", res.Next)
	}
	if n := len(k.ASC.Requests()); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestSubscribersRefundsEitherSign(t *testing.T) {
	cols := []string{"Subscription Offer Type", "Developer Proceeds", "Proceeds Currency", "Refund", "Units"}
	for _, units := range []string{"-1", "1"} {
		t.Run("refund units "+units, func(t *testing.T) {
			tab := &reports.Table{Columns: cols, Rows: [][]string{
				{"Free Trial", "0", "USD", "", "3"},
				{"", "4.00", "USD", "", "2"},
				{"Free Trial", "4.00", "USD", "Yes", units},
			}}
			var out salesOut
			if err := subscribers(&out, []dayTable{{Date: "2026-10-24", Table: tab}}, nil); err != nil {
				t.Fatal(err)
			}
			s := out.Subscribers
			if s.Units != 5 || s.Refunds != 1 || s.ByOfferType["Free Trial"] != 3 || len(s.ByOfferType) != 1 {
				t.Fatalf("summary = %+v", s)
			}
		})
	}
}
