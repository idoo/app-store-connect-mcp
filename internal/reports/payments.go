package reports

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// paymentsPath is the Payments and Financial Reports page's endpoint for one
// vendor's fiscal month (?year=YYYY&month=M), reached through [tool.Portal]
// with the human's web session. It answers for any user who can see
// Payments, unlike /v1/financeReports, which needs a Finance or Admin key.
const paymentsPath = "/WebObjects/iTunesConnect.woa/ra/paymentConsolidation/providers/%d/sapVendorNumbers/%s?year=%d&month=%d"

// PaymentsMonth is one fiscal month of the web Payments page.
type PaymentsMonth struct {
	// Published is false until Apple publishes the month's financial
	// report; an unpublished month echoes the carried-forward balances with
	// nothing earned.
	Published bool
	// Updated is the day Apple last updated the month (YYYY-MM-DD), if any.
	Updated  string
	Payments []Payment
}

// Payment is one payment (one bank currency) of a month: the balance Apple
// owes across Regions, converted to Currency.
type Payment struct {
	Status   string // Apple's status, e.g. PAID or PENDING_PAYMENT
	Amount   *big.Rat
	Currency string
	// Date is the day paid or expected to be paid (YYYY-MM-DD), if any.
	Date     string
	Expected bool
	Regions  []PaymentRegion
}

// PaymentRegion is one region currency of a payment. Earned, Balance and
// Owed are in Currency; Bank is Owed converted to the payment's currency.
// ExchangeRate is Apple's rate as given ("" until Apple sets it).
type PaymentRegion struct {
	Code, Name, Currency string
	Units                int64
	Balance              *big.Rat // carried over from earlier months
	Earned               *big.Rat
	TaxesAndFees         *big.Rat
	Adjustments          *big.Rat
	Owed                 *big.Rat
	Bank                 *big.Rat
	ExchangeRate         string
}

type paymentsResponse struct {
	Data *struct {
		UpdatedDate           string `json:"updatedDate"`
		HasConsolidatedReport bool   `json:"hasConsolidatedReport"`
		ReportSummaries       []struct {
			Status                     string `json:"status"`
			Amount                     string `json:"amount"`
			Currency                   string `json:"currency"`
			IsPaymentExpected          bool   `json:"isPaymentExpected"`
			PaidOrExpectingPaymentDate string `json:"paidOrExpectingPaymentDate"`
			ProceedsByRegion           []struct {
				RegionCurrency struct {
					RegionCode   string `json:"regionCode"`
					RegionName   string `json:"regionName"`
					CurrencyCode string `json:"currencyCode"`
				} `json:"regionCurrency"`
				UnitsSold        int64  `json:"unitsSold"`
				BeginBalance     string `json:"beginBalance"`
				Earned           string `json:"earned"`
				TaxesAndFees     string `json:"taxesAndFees"`
				AdjustmentAmount string `json:"adjustmentAmount"`
				TotalOwed        string `json:"totalOwed"`
				ExchangeRate     string `json:"exchangeRate"`
				Proceeds         string `json:"proceeds"`
			} `json:"proceedsByRegion"`
		} `json:"reportSummaries"`
	} `json:"data"`
	StatusCode string `json:"statusCode"`
	Messages   struct {
		Error any `json:"error"`
	} `json:"messages"`
}

// Payments reads the fiscal month (YYYY-MM) of vendor (a numeric vendor
// number) under provider from the web Payments page.
func Payments(ctx context.Context, p tool.Portal, provider int64, vendor, month string) (*PaymentsMonth, error) {
	m, err := time.Parse("2006-01", month)
	if err != nil {
		return nil, fmt.Errorf("reports: payments month %q must be YYYY-MM", month)
	}
	if _, err := strconv.ParseUint(vendor, 10, 64); err != nil || provider <= 0 {
		return nil, fmt.Errorf("reports: payments need a numeric provider and vendor number, got %d and %q", provider, vendor)
	}
	var r paymentsResponse
	if err := p.JSON(ctx, "GET", fmt.Sprintf(paymentsPath, provider, vendor, m.Year(), int(m.Month())), nil, &r); err != nil {
		return nil, err
	}
	if r.StatusCode != "" && r.StatusCode != "SUCCESS" {
		return nil, fmt.Errorf("reports: payments %s: Apple answered %s: %s", month,
			tool.SanitizeText(r.StatusCode), tool.SanitizeText(fmt.Sprint(r.Messages.Error)))
	}
	if r.Data == nil {
		return nil, fmt.Errorf("reports: payments %s: no data: %w", month, ErrCorrupt)
	}
	out := &PaymentsMonth{Published: r.Data.HasConsolidatedReport, Updated: dayOf(r.Data.UpdatedDate)}
	for _, s := range r.Data.ReportSummaries {
		pay := Payment{
			Status: s.Status, Currency: s.Currency, Expected: s.IsPaymentExpected,
			Date: dayOf(s.PaidOrExpectingPaymentDate),
		}
		if pay.Amount, err = paymentAmount(month, "amount", s.Amount); err != nil {
			return nil, err
		}
		for _, g := range s.ProceedsByRegion {
			reg := PaymentRegion{
				Code: g.RegionCurrency.RegionCode, Name: g.RegionCurrency.RegionName, Currency: g.RegionCurrency.CurrencyCode,
				Units: g.UnitsSold, ExchangeRate: strings.TrimSpace(g.ExchangeRate),
			}
			for _, f := range []struct {
				dst  **big.Rat
				name string
				v    string
			}{
				{&reg.Balance, "beginBalance", g.BeginBalance},
				{&reg.Earned, "earned", g.Earned},
				{&reg.TaxesAndFees, "taxesAndFees", g.TaxesAndFees},
				{&reg.Adjustments, "adjustmentAmount", g.AdjustmentAmount},
				{&reg.Owed, "totalOwed", g.TotalOwed},
				{&reg.Bank, "proceeds", g.Proceeds},
			} {
				if *f.dst, err = paymentAmount(month, f.name, f.v); err != nil {
					return nil, err
				}
			}
			pay.Regions = append(pay.Regions, reg)
		}
		out.Payments = append(out.Payments, pay)
	}
	return out, nil
}

// Earned totals the month's earnings per region currency (the API finance
// report's proceeds) and units sold; regions that earned nothing are left
// out.
func (m *PaymentsMonth) Earned() (Money, int64) {
	earned := Money{}
	var units int64
	for _, p := range m.Payments {
		for _, r := range p.Regions {
			units += r.Units
			if r.Earned.Sign() != 0 {
				earned.Add(r.Currency, r.Earned)
			}
		}
	}
	return earned, units
}

func paymentAmount(month, field, s string) (*big.Rat, error) {
	v, err := ParseAmount(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("reports: payments %s: %s %q: %w", month, field, tool.SanitizeText(s), ErrCorrupt)
	}
	return v, nil
}

// dayOf returns the YYYY-MM-DD of an Apple timestamp or date ("" stays "").
func dayOf(s string) string {
	if len(s) >= len(time.DateOnly) {
		if _, err := time.Parse(time.DateOnly, s[:len(time.DateOnly)]); err == nil {
			return s[:len(time.DateOnly)]
		}
	}
	return ""
}
