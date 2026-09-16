package reports

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/reports"
	"github.com/idoo/hamn/internal/tool"
)

type financeIn struct {
	Month      string `json:"month,omitempty" jsonschema:"Apple fiscal month YYYY-MM; default: last closed month"`
	ReportType string `json:"report_type,omitempty" jsonschema:"default FINANCIAL"`
	Region     string `json:"region,omitempty" jsonschema:"default ZZ (FINANCIAL) or Z1 (FINANCE_DETAIL)"`
	TopN       int    `json:"top_n,omitempty" jsonschema:"products kept, default 10, max 100"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "finance_report", Toolset: tool.Reports, Class: tool.Read, Idempotent: true, NeedsTeamKey: true,
		Title: "Payments and financial report",
		Description: "Decoded Payments & Financial report for one fiscal month: proceeds per currency, FX rates, top " +
			"products, total_usd only if every currency has a rate. Unpublished months: pending.",
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
	Month       string            `json:"month"`
	ReportType  string            `json:"report_type"`
	Region      string            `json:"region"`
	Status      string            `json:"status"`
	Explanation string            `json:"explanation,omitempty"`
	Rows        int               `json:"rows,omitempty"`
	Units       int64             `json:"units,omitempty"`
	Proceeds    []amountOut       `json:"proceeds,omitempty"`
	FXRates     map[string]string `json:"fx_rates_to_usd,omitempty"`
	TotalUSD    *string           `json:"total_usd,omitempty"`
	USDSource   string            `json:"total_usd_source,omitempty"`
	MissingFX   []string          `json:"missing_rates,omitempty"`
	Products    []financeProduct  `json:"top_products,omitempty"`
	Omitted     int               `json:"products_omitted,omitempty"`
}

func financeReport(ctx context.Context, env *tool.Env, in financeIn) (*tool.Result, error) {
	vendor, err := requireVendor(env)
	if err != nil {
		return nil, err
	}
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
	month := in.Month
	if month == "" {
		month = lastClosedMonth(env.Clock.Now(), 0)
	}
	m, err := time.Parse("2006-01", month)
	if err != nil {
		return nil, fmt.Errorf("%w: month %q must be YYYY-MM", tool.ErrInvalidArgs, month)
	}
	topN := in.TopN
	if topN <= 0 {
		topN = salesDefaultTopN
	}
	topN = min(topN, salesMaxTopN)

	out := financeOut{Month: month, ReportType: string(rt), Region: region}
	t, pending, err := download(ctx, env, "/v1/financeReports", financeQuery(vendor, rt, region, month))
	if err != nil {
		return nil, err
	}
	if pending {
		prev := m.AddDate(0, -1, 0).Format("2006-01")
		out.Status = "pending"
		out.Explanation = "Apple has not published this fiscal month's report for this region yet; reports appear after the " +
			"fiscal month closes, usually within its first week, and only for months with proceeds."
		return tool.OK(&out).Then(tool.NextStep{Tool: "finance_report", Args: map[string]any{
			"month": prev, "report_type": string(rt), "region": region,
		}}), nil
	}
	rows, err := reports.FinanceRows(t)
	if err != nil {
		return nil, fmt.Errorf("finance report %s: %w", month, err)
	}
	sum := reports.FinanceSummary(rows)
	out.Status = "available"
	out.Rows = len(rows)
	out.Units = sum.Units
	out.Proceeds = sortedAmounts(sum.Proceeds)
	out.TotalUSD, out.USDSource, out.MissingFX = sum.TotalUSD, sum.TotalUSDSource, sum.MissingRates
	if rates := fxRates(rows); len(rates) > 1 {
		out.FXRates = make(map[string]string, len(rates))
		for c, r := range rates {
			out.FXRates[c] = r.FloatString(6)
		}
	}
	out.Products, out.Omitted = financeProducts(rows, topN)
	return tool.OK(&out), nil
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
