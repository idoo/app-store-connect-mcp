package reports_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/reports"
)

const paymentsTestPath = "/WebObjects/iTunesConnect.woa/ra/paymentConsolidation/providers/4242424/sapVendorNumbers/81234567?year=2026&month=9"

// The month is requested as year and unpadded month; amounts parse exactly
// and an empty amount (no carried balance) is zero.
func TestPaymentsDecodesMonth(t *testing.T) {
	p := newDashPortal(t)
	p.HandleJSON("GET", paymentsTestPath, `{"data":{"updatedDate":"2026-10-09T00:00:00Z","hasConsolidatedReport":true,
		"reportSummaries":[{"status":"PAID","amount":"1,349.00","currency":"SEK","isPaymentExpected":true,"paidOrExpectingPaymentDate":"2026-11-05",
		"proceedsByRegion":[{"regionCurrency":{"regionCode":"EU","regionName":"Euro-Zone","currencyCode":"EUR"},"unitsSold":4,
		"beginBalance":"","earned":"7.10","taxesAndFees":"-0.20","adjustmentAmount":"0","totalOwed":"6.90","exchangeRate":"11.2","proceeds":"77.28"}]}]},
		"messages":{"warn":null,"error":null,"info":null},"statusCode":"SUCCESS"}`)
	m, err := reports.Payments(context.Background(), p, 4242424, "81234567", "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Published || m.Updated != "2026-10-09" || len(m.Payments) != 1 {
		t.Fatalf("month = %+v", m)
	}
	pay := m.Payments[0]
	r := pay.Regions[0]
	if pay.Status != "PAID" || pay.Amount.RatString() != "1349" || pay.Date != "2026-11-05" || !pay.Expected ||
		r.Balance.Sign() != 0 || r.TaxesAndFees.RatString() != "-1/5" || r.Bank.FloatString(2) != "77.28" || r.ExchangeRate != "11.2" {
		t.Errorf("payment = %+v region %+v", pay, r)
	}
	earned, units := m.Earned()
	if earned.String() != "EUR 7.10" || units != 4 {
		t.Errorf("earned = %v, units %d", earned, units)
	}
}

func TestPaymentsRejectsBadInput(t *testing.T) {
	p := newDashPortal(t)
	p.HandleJSON("GET", paymentsTestPath, `{"data":{"hasConsolidatedReport":true,"reportSummaries":[{"amount":"1,23","currency":"SEK"}]},"statusCode":"SUCCESS"}`)
	if _, err := reports.Payments(context.Background(), p, 4242424, "81234567", "2026-09"); !errors.Is(err, reports.ErrCorrupt) {
		t.Errorf("ambiguous amount: err = %v", err)
	}
	p.HandleJSON("GET", paymentsTestPath, `{"data":null,"messages":{"error":["no access"]},"statusCode":"ERROR"}`)
	if _, err := reports.Payments(context.Background(), p, 4242424, "81234567", "2026-09"); err == nil || !strings.Contains(err.Error(), "no access") {
		t.Errorf("Apple error: err = %v", err)
	}
	for _, c := range []struct{ vendor, month string }{{"8123/../1", "2026-09"}, {"81234567", "2026-9"}} {
		if _, err := reports.Payments(context.Background(), p, 4242424, c.vendor, c.month); err == nil {
			t.Errorf("%+v: no error", c)
		}
	}
	if n := len(p.Calls()); n != 2 {
		t.Errorf("portal calls = %d, want 2 (bad input never reaches Apple)", n)
	}
}
