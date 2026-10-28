package reports

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// paymentsMonthPath is the web Payments path of the fake account's vendor.
func paymentsMonthPath(vendor string, year, month int) string {
	return vendorsPath + "/" + vendor + "?year=" + strconv.Itoa(year) + "&month=" + strconv.Itoa(month)
}

const financeForbidden = `{"errors":[{"status":"403","code":"FORBIDDEN_ERROR","title":"forbidden","detail":"not allowed"}]}`

// forbiddenFinanceKit is signed in to the web session with a key Apple
// refuses finance reports.
func forbiddenFinanceKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := webVendorKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/financeReports", http.StatusForbidden, financeForbidden)
	return k
}

func paymentsFixture(name string) string { return filepath.Join("testdata", "payments", name) }

// A finance 403 (no Finance role) with a web session answers from the web
// Payments page: earned per region currency as proceeds, plus the payment in
// the bank currency per region.
func TestFinanceReportWebPaymentsAfterForbidden(t *testing.T) {
	k := forbiddenFinanceKit(t)
	k.Portal.HandleFile(http.MethodGet, paymentsMonthPath("81234567", 2026, 9), paymentsFixture("2026-09.json"))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, ``))
	got := tooltest.Decode[map[string]any](t, res)
	if got["status"] != "available" || got["data_source"] != "web_payments" || got["month"] != "2026-09" ||
		got["units"] != 3.0 || got["vendor_number_source"] != "web_session" || got["top_products"] != nil {
		t.Fatalf("got = %s", tooltest.JSON(t, got))
	}
	// GBP earned nothing this month (only a carried balance): not proceeds.
	if s := tooltest.JSON(t, got["proceeds"]); s != `[{"amount":"22.22","currency":"USD"}]` {
		t.Errorf("proceeds = %s", s)
	}
	// Keys are sorted: the result went through a map.
	if pay := tooltest.JSON(t, got["payments"]); pay != `[{"amount":"349.00","currency":"SEK","regions":[`+
		`{"balance_forward":"1.73","bank_amount":"23.24","currency":"GBP","earned":"0.00","name":"United Kingdom","owed":"1.73","region":"GB","units":0},`+
		`{"balance_forward":"8.40","bank_amount":"134.05","currency":"USD","earned":"4.20","name":"Americas","owed":"12.60","region":"US","units":2},`+
		`{"bank_amount":"191.71","currency":"USD","earned":"18.02","name":"Rest of World","owed":"18.02","region":"WW","units":1}],`+
		`"status":"PENDING_PAYMENT"}]` {
		t.Errorf("payments = %s", pay)
	}
	if !strings.Contains(got["note"].(string), "web Payments page") {
		t.Errorf("note = %v", got["note"])
	}
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/financeReports")); n != 1 {
		t.Errorf("finance API calls = %d, want 1 (the 403)", n)
	}
}

// The default month falls back once on the web too: an unpublished month
// (no consolidated report, balances only) is skipped with a warning.
func TestFinanceReportWebPaymentsDefaultMonthFallsBack(t *testing.T) {
	k := forbiddenFinanceKit(t)
	k.Portal.HandleFile(http.MethodGet, paymentsMonthPath("81234567", 2026, 9), paymentsFixture("2026-10.json"))
	k.Portal.HandleFile(http.MethodGet, paymentsMonthPath("81234567", 2026, 8), paymentsFixture("2026-08.json"))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, ``))
	got := tooltest.Decode[map[string]any](t, res)
	if got["status"] != "available" || got["month"] != "2026-08" || got["units"] != 2.0 ||
		tooltest.JSON(t, got["proceeds"]) != `[{"amount":"1.73","currency":"GBP"},{"amount":"2.10","currency":"USD"}]` {
		t.Fatalf("got = %s", tooltest.JSON(t, got))
	}
	if w := res.Meta.Warnings; len(w) != 1 || !strings.Contains(w[0], "2026-09 is not published yet") {
		t.Errorf("warnings = %v", w)
	}
}

// A month Apple has not published yet is pending, pointing at the month
// before.
func TestFinanceReportWebPaymentsPending(t *testing.T) {
	k := forbiddenFinanceKit(t)
	k.Portal.HandleFile(http.MethodGet, paymentsMonthPath("81234567", 2026, 10), paymentsFixture("2026-10.json"))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "finance_report"), k.Env, `{"month":"2026-10"}`))
	got := tooltest.Decode[map[string]any](t, res)
	if got["status"] != "pending" || got["data_source"] != "web_payments" || got["proceeds"] != nil || got["payments"] != nil ||
		!strings.Contains(got["explanation"].(string), "2026-10") {
		t.Fatalf("got = %s", tooltest.JSON(t, got))
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "finance_report" || res.Next[0].Args["month"] != "2026-09" {
		t.Errorf("next = %+v", res.Next)
	}
}

// Without a web session the 403 keeps its hint and names web_login.
func TestFinanceReportForbiddenWithoutWebSession(t *testing.T) {
	k := reportsKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/financeReports", http.StatusForbidden, financeForbidden)
	r := tooltest.Call(t, mustTool(t, "finance_report"), k.Env, `{"month":"2026-08"}`)
	eb := tooltest.MustFail(t, r, "api_error")
	if eb.API == nil || eb.API.Status != http.StatusForbidden || !strings.Contains(eb.Hint, "vendor number 12345") ||
		!strings.Contains(eb.Message, "web_login") {
		t.Errorf("body = %+v", eb)
	}
	if len(r.Next) != 1 || r.Next[0].Tool != "web_login" {
		t.Errorf("next = %+v", r.Next)
	}
}
