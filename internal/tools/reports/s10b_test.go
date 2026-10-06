package reports

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

// s10bNow is Thursday 2026-10-22 12:00 Pacific: this_week = Mon 19 … Thu 22.
var s10bNow = time.Date(2026, 10, 22, 19, 0, 0, 0, time.UTC)

const notFound = `{"errors":[{"status":"404","code":"NOT_FOUND","title":"The specified resource does not exist","detail":"There were no sales for the date specified."}]}`

func s10bKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t)
	k.Clock.Set(s10bNow)
	k.Resolve.Apps = []tool.App{
		{ID: "1000000001", BundleID: "com.example.notes", Name: "Example Notes", SKU: "com.example.notes"},
		{ID: "2000000001", BundleID: "com.other.app", Name: "Other App", SKU: "com.other.app"},
	}
	return k
}

// gz returns the gzip bytes of a testdata/s10b fixture.
func gz(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "s10b", name))
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
	k := s10bKit(t)
	handleGz(t, k, "/v1/salesReports?filter[reportDate]=2026-10-19", "sales_day1.tsv")
	handleGz(t, k, "/v1/salesReports?filter[reportDate]=2026-10-20", "sales_day2.tsv")
	k.ASC.Handle(http.MethodGet, "/v1/salesReports?filter[reportDate]=2026-10-21", http.StatusNotFound, notFound)
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
	// Exact per-currency sums, largest first, never summed across currencies.
	if s := tooltest.JSON(t, got["proceeds"]); s != `[{"amount":"420","currency":"JPY"},{"amount":"19.54","currency":"USD"},{"amount":"3.88","currency":"EUR"}]` {
		t.Errorf("proceeds = %s", s)
	}
	if _, ok := got["proceeds_estimate_usd"]; ok {
		t.Error("USD estimate without with_usd_estimate")
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

func TestSalesReportAppFilterAndUSDEstimate(t *testing.T) {
	k := thisWeekKit(t)
	handleGz(t, k, "/v1/financeReports?filter[reportDate]=2026-09", "finance_detail_fx.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env,
		`{"period":"this_week","app":"Example Notes","with_usd_estimate":true}`))
	got := tooltest.Decode[map[string]any](t, res)
	if s := tooltest.JSON(t, got["units"]); s != `{"download":20,"iap":8,"redownload":4,"subscription":7,"update":30}` {
		t.Errorf("units = %s (other app must be filtered out, IAPs kept via parent SKU)", s)
	}
	if id := got["app"].(map[string]any)["id"]; id != "1000000001" {
		t.Errorf("app = %v", id)
	}
	// 19.54 USD + 3.88 EUR × 1.10 + 420 JPY × 0.0067 = 26.622
	if s := tooltest.JSON(t, got["proceeds_estimate_usd"]); s != `{"amount":"26.62","fx_source":"Apple exchange rates, finance detail report","fx_source_month":"2026-09","label":"estimate"}` {
		t.Errorf("proceeds_estimate_usd = %s", s)
	}
	fin := k.ASC.RequestsTo(http.MethodGet, "/v1/financeReports")
	if len(fin) != 1 || fin[0].Query.Get("filter[reportType]") != "FINANCE_DETAIL" || fin[0].Query.Get("filter[regionCode]") != "Z1" {
		t.Errorf("finance requests = %+v", fin)
	}
}

func TestSalesReportUSDEstimateMissingRate(t *testing.T) {
	k := thisWeekKit(t)
	handleGz(t, k, "/v1/financeReports?filter[reportDate]=2026-09", "financial_no_fx.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"this_week","with_usd_estimate":true}`))
	got := tooltest.Decode[map[string]any](t, res)
	if _, ok := got["proceeds_estimate_usd"]; ok {
		t.Error("estimate must not be guessed without rates")
	}
	if w := strings.Join(res.Meta.Warnings, ";"); !strings.Contains(w, "EUR, JPY") {
		t.Errorf("warnings = %q", w)
	}
}

func TestSalesReportVendorMissing(t *testing.T) {
	k := s10bKit(t)
	k.Env.Profile.VendorNumber = ""
	body := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"yesterday"}`), "vendor_number_missing")
	if !strings.Contains(body.Hint, "hamn auth set test vendor_number=") || !strings.Contains(body.Message, "Payments and Financial Reports") {
		t.Errorf("body = %+v", body)
	}
	if n := len(k.ASC.Requests()); n != 0 {
		t.Errorf("requests = %d", n)
	}
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, ``), "vendor_number_missing")
}

func TestReportsWithheldForIndividualKey(t *testing.T) {
	k := s10bKit(t).Individual()
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
	k := s10bKit(t)
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
	k := s10bKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/salesReports", http.StatusBadRequest,
		`{"errors":[{"status":"400","code":"PARAMETER_ERROR.INVALID","title":"bad vendor"}]}`)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "sales_report"), k.Env, `{"period":"yesterday"}`), "api_error")
}

func TestFinanceReportWithFX(t *testing.T) {
	k := s10bKit(t)
	handleGz(t, k, "/v1/financeReports?filter[reportDate]=2026-09", "finance_detail_fx.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, `{"report_type":"FINANCE_DETAIL"}`))
	got := tooltest.Decode[map[string]any](t, res)
	// 1.38 × 1.10 + 420 × 0.0067 + 27.90 = 32.232
	if got["status"] != "available" || got["total_usd"] != "32.23" || got["region"] != "Z1" || got["month"] != "2026-09" {
		t.Errorf("got = %s", tooltest.JSON(t, got))
	}
	if s := tooltest.JSON(t, got["fx_rates_to_usd"]); s != `{"EUR":"1.100000","JPY":"0.006700","USD":"1.000000"}` {
		t.Errorf("fx = %s", s)
	}
	r := k.ASC.Requests()[0].Query
	if r.Get("filter[reportType]") != "FINANCE_DETAIL" || r.Get("filter[vendorNumber]") != "12345678" {
		t.Errorf("query = %s", r.Encode())
	}
}

func TestFinanceReportWithoutFX(t *testing.T) {
	k := s10bKit(t)
	handleGz(t, k, "/v1/financeReports?filter[reportDate]=2026-08", "financial_no_fx.tsv")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, `{"month":"2026-08"}`))
	got := tooltest.Decode[map[string]any](t, res)
	if _, ok := got["total_usd"]; ok {
		t.Errorf("total_usd guessed: %v", got["total_usd"])
	}
	if s := tooltest.JSON(t, got["missing_rates"]); s != `["EUR","JPY"]` {
		t.Errorf("missing_rates = %s", s)
	}
	if s := tooltest.JSON(t, got["proceeds"]); s != `[{"amount":"420","currency":"JPY"},{"amount":"27.21","currency":"USD"},{"amount":"1.38","currency":"EUR"}]` {
		t.Errorf("proceeds = %s", s)
	}
	if got["region"] != "ZZ" || got["report_type"] != "FINANCIAL" {
		t.Errorf("defaults = %s", tooltest.JSON(t, got))
	}
}

func TestFinanceReportPending(t *testing.T) {
	k := s10bKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/financeReports", http.StatusNotFound, notFound)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, ``))
	got := tooltest.Decode[map[string]any](t, res)
	if got["status"] != "pending" || got["explanation"] == "" {
		t.Errorf("got = %s", tooltest.JSON(t, got))
	}
	if len(res.Next) != 1 || res.Next[0].Args["month"] != "2026-08" {
		t.Errorf("next = %+v", res.Next)
	}
}
