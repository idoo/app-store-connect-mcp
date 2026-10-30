package reports

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/reports"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

type financeIn struct {
	Month       string `json:"month,omitempty" jsonschema:"fiscal YYYY-MM; default newest closed"`
	ReportType  string `json:"report_type,omitempty" jsonschema:"default FINANCIAL"`
	Region      string `json:"region,omitempty" jsonschema:"default ZZ, or Z1 for detail"`
	TopN        int    `json:"top_n,omitempty" jsonschema:"products kept, default 10, max 100"`
	IncludeRows bool   `json:"include_rows,omitempty"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "finance_report", Toolset: tool.Reports, Class: tool.Read, Idempotent: true, NeedsTeamKey: true,
		Title: "Payments and financial report",
		Description: "Payments & Financial report for one fiscal month: proceeds per currency, top products; " +
			"include_rows: CSV link of all rows. No FX/USD totals (web Payments only). Unpublished months: pending.",
		Enums: map[string][]any{"report_type": {string(applemeta.ReportFinancial), string(applemeta.ReportFinanceDetail)}},
	}, financeReport))
}

// financeProduct is one product's totals in a finance report.
type financeProduct struct {
	SKU      string        `json:"sku"`
	Title    string        `json:"title,omitempty"`
	Units    int64         `json:"units"`
	Proceeds reports.Money `json:"proceeds"`
}

// financeOut is the finance_report result.
type financeOut struct {
	Month       string           `json:"month"`
	ReportType  string           `json:"report_type"`
	Region      string           `json:"region"`
	Status      string           `json:"status"`
	Explanation string           `json:"explanation,omitempty"`
	Rows        int              `json:"rows,omitempty"`
	Units       int64            `json:"units,omitempty"`
	Proceeds    []amountOut      `json:"proceeds,omitempty"`
	Products    []financeProduct `json:"top_products,omitempty"`
	Omitted     int              `json:"products_omitted,omitempty"`
	Note        string           `json:"note,omitempty"`
	// RowsResource is the URI of the CSV artifact with every decoded row.
	RowsResource string `json:"rows_resource,omitempty"`
	// VendorSource is "web_session" when the vendor number was read from
	// Payments through the web session (omitted for the profile's own).
	VendorSource string `json:"vendor_number_source,omitempty"`
}

// financeFXNote explains why proceeds are never converted.
const financeFXNote = "proceeds stay in each currency: Apple's finance reports have no exchange rates; the rates and " +
	"bank-currency totals are only on the web Payments page (App Store Connect → Payments and Financial Reports)"

func financeReport(ctx context.Context, env *tool.Env, in financeIn) (*tool.Result, error) {
	v, err := resolveVendor(ctx, env)
	if err != nil {
		return nil, err
	}
	vendor := v.Number
	rt := applemeta.ReportType(in.ReportType)
	if rt == "" {
		rt = applemeta.ReportFinancial
	}
	region := strings.ToUpper(strings.TrimSpace(in.Region))
	if region == "" {
		var ok bool
		if region, ok = applemeta.FinanceRegion(rt); !ok {
			return nil, fmt.Errorf("%w: unknown report_type %q", tool.ErrInvalidArgs, rt)
		}
	}
	// Without an explicit month, try the newest closed month first and fall
	// back once: Apple's fiscal months end on a Saturday and publish a few
	// days later, so the newest one is often still pending early in a month.
	months := []string{in.Month}
	if in.Month == "" {
		now := env.Clock.Now()
		months = []string{lastClosedMonth(now, 0), lastClosedMonth(now, 1)}
	}
	for _, month := range months {
		if _, err := time.Parse("2006-01", month); err != nil {
			return nil, fmt.Errorf("%w: month %q must be YYYY-MM", tool.ErrInvalidArgs, month)
		}
	}
	topN := in.TopN
	if topN <= 0 {
		topN = salesDefaultTopN
	}
	topN = min(topN, salesMaxTopN)

	var (
		t       *reports.Table
		month   string
		skipped []string
	)
	for _, month = range months {
		var nf *tool.APIError
		t, nf, err = download(ctx, env, "/v1/financeReports", financeQuery(vendor, rt, region, month))
		if err != nil {
			return nil, financeCause(err, v)
		}
		if nf == nil {
			break
		}
		skipped = append(skipped, month)
		t = nil
	}
	out := financeOut{Month: month, ReportType: string(rt), Region: region, VendorSource: webVendorSource(v)}
	if t == nil {
		m, _ := time.Parse("2006-01", month)
		prev := m.AddDate(0, -1, 0).Format("2006-01")
		out.Status = "pending"
		out.Explanation = "Apple has not published the report for " + strings.Join(skipped, " or ") + " in this region yet; " +
			"reports appear after the fiscal month closes, usually within its first week, and only for months with proceeds."
		return tool.OK(&out).Then(tool.NextStep{Tool: "finance_report", Args: map[string]any{
			"month": prev, "report_type": string(rt), "region": region,
		}}), nil
	}
	res := tool.OK(&out)
	if len(skipped) > 0 {
		res.Warn(fmt.Sprintf("%s is not published yet; showing %s, the newest published fiscal month", skipped[0], month))
	}
	rows, err := reports.FinanceRows(t)
	if err != nil {
		return nil, fmt.Errorf("finance report %s: %w", month, err)
	}
	sum := reports.FinanceSummary(rows)
	out.Status = "available"
	out.Rows = len(rows)
	out.Units = sum.Units
	out.Proceeds = currencyAmounts(sum.Proceeds)
	out.Note = financeFXNote
	out.Products, out.Omitted = financeProducts(rows, topN)
	// Rows behind omitted products are only reachable through the artifact.
	if in.IncludeRows || out.Omitted > 0 {
		linkRows(ctx, env, res, fmt.Sprintf("finance-%s-%s-%s.csv", strings.ToLower(string(rt)), region, month),
			salesCSV([]dayTable{{Date: month, Table: t}}, nil), func(uri string) { out.RowsResource = uri })
	}
	return res, nil
}

// financeRoleHint is the finance 403 hint when the vendor number came from
// Apple (Payments via the web session), so it cannot be the wrong account's.
const financeRoleHint = "finance reports need an API key with the Finance or Admin role (Users and Access › Integrations); " +
	"sales_report works with this key"

// financeCause explains a finance 403/5xx: a vendor number read from Apple
// is this account's, so a 403 means the key's role; a profile number may be
// another account's (vendorCause).
func financeCause(err error, v tool.VendorNumber) error {
	if v.Source != tool.VendorFromWebSession {
		return vendorCause(err, v.Number)
	}
	var api *tool.APIError
	if errors.As(err, &api) && api.Status == http.StatusForbidden {
		api.Hint = financeRoleHint
	}
	return err
}

// financeProducts totals units and proceeds per SKU (Vendor Identifier),
// ordered by units descending, then SKU.
func financeProducts(rows []reports.FinanceRow, topN int) ([]financeProduct, int) {
	by := map[string]*financeProduct{}
	for _, r := range rows {
		p, ok := by[r.VendorID]
		if !ok {
			p = &financeProduct{SKU: r.VendorID, Title: r.Title, Proceeds: reports.Money{}}
			by[r.VendorID] = p
		}
		p.Units += r.Quantity
		p.Proceeds.Add(r.PartnerShareCurrency, r.ExtendedPartnerShare)
	}
	list := make([]financeProduct, 0, len(by))
	for _, p := range by {
		list = append(list, *p)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Units != list[j].Units {
			return list[i].Units > list[j].Units
		}
		return list[i].SKU < list[j].SKU
	})
	if len(list) > topN {
		return list[:topN], len(list) - topN
	}
	return list, 0
}
