package reports

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/reports"
	"github.com/idoo/hamn/internal/tool"
)

const (
	salesDefaultTopN = 10
	salesMaxTopN     = 100
	// salesMaxDays caps the DAILY fan-out (about one quarter of downloads).
	salesMaxDays = 93
	// salesMaxRawRows caps decoded rows returned for report types without a
	// typed aggregation.
	salesMaxRawRows = 50
)

type salesIn struct {
	Period     string      `json:"period,omitempty" jsonschema:"preset in Pacific time; default yesterday"`
	From       string      `json:"from,omitempty" jsonschema:"first day YYYY-MM-DD (with to)"`
	To         string      `json:"to,omitempty" jsonschema:"last day YYYY-MM-DD, inclusive"`
	Frequency  string      `json:"frequency,omitempty" jsonschema:"fetch one report of this frequency instead of a daily range (with report_date)"`
	ReportDate string      `json:"report_date,omitempty" jsonschema:"DAILY YYYY-MM-DD, WEEKLY the week's Sunday YYYY-MM-DD, MONTHLY YYYY-MM, YEARLY YYYY"`
	ReportType string      `json:"report_type,omitempty" jsonschema:"default SALES"`
	SubType    string      `json:"report_subtype,omitempty" jsonschema:"default: first subtype valid for report_type"`
	GroupBy    []string    `json:"group_by,omitempty" jsonschema:"SALES grouping dimensions"`
	TopN       int         `json:"top_n,omitempty" jsonschema:"groups kept, default 10, max 100"`
	App        tool.AppRef `json:"app,omitempty" jsonschema:"only rows of this app (id, bundle id, SKU or name); default: all apps"`
	WithUSD    bool        `json:"with_usd_estimate,omitempty" jsonschema:"add proceeds_estimate_usd from Apple's FX rates in the latest finance report"`
}

func init() {
	types := make([]any, 0, len(applemeta.SalesReports()))
	subTypes := []any{}
	for _, rs := range applemeta.SalesReports() {
		types = append(types, string(rs.Type))
		for _, s := range rs.SubTypes {
			if !slices.Contains(subTypes, any(s)) {
				subTypes = append(subTypes, s)
			}
		}
	}
	periods := make([]any, 0, len(applemeta.PeriodPresets))
	for _, p := range applemeta.PeriodPresets {
		periods = append(periods, p)
	}
	tool.Register(tool.New(tool.Meta{
		Name: "sales_report", Toolset: tool.Reports, Class: tool.Read, Idempotent: true, NeedsTeamKey: true,
		Title: "Sales and Trends report",
		Description: "Raw/decoded Sales & Trends rows and aggregates (units by category, proceeds per currency, top-N groups) " +
			"for a period, from/to days or one frequency+report_date. Prefer insights_summary for 'how is my app doing'. " +
			"Days not yet published come back as pending.",
		Enums: map[string][]any{
			"period":         periods,
			"frequency":      {"DAILY", "WEEKLY", "MONTHLY", "YEARLY"},
			"report_type":    types,
			"report_subtype": subTypes,
			"group_by":       {"app", "sku", "country", "date", "product_category", "currency"},
		},
	}, salesReport))
}

// salesCategory maps rows with applemeta's Product Type Identifier table.
func salesCategory(id string) reports.Category {
	return reports.Category(applemeta.ProductCategoryOf(id))
}

// salesWindow is the resolved set of report dates to fetch.
type salesWindow struct {
	Freq    applemeta.Frequency
	From    string
	To      string
	Fetch   []string // report dates to download
	Pending []string // dates that cannot exist yet (today or later, Pacific)
}

func resolveSalesWindow(in salesIn, now time.Time) (salesWindow, error) {
	invalid := func(format string, a ...any) (salesWindow, error) {
		return salesWindow{}, fmt.Errorf("%w: "+format, append([]any{tool.ErrInvalidArgs}, a...)...)
	}
	modes := 0
	if in.Period != "" {
		modes++
	}
	if in.From != "" || in.To != "" {
		modes++
	}
	if in.Frequency != "" || in.ReportDate != "" {
		modes++
	}
	if modes > 1 {
		return invalid("use one of period, from/to, or frequency+report_date")
	}
	if in.Frequency != "" || in.ReportDate != "" {
		if in.Frequency == "" || in.ReportDate == "" {
			return invalid("frequency and report_date go together")
		}
		f := applemeta.Frequency(in.Frequency)
		if err := checkReportDate(f, in.ReportDate); err != nil {
			return salesWindow{}, err
		}
		return salesWindow{Freq: f, From: in.ReportDate, To: in.ReportDate, Fetch: []string{in.ReportDate}}, nil
	}
	today := applemeta.PacificDay(now)
	var from, to time.Time
	switch {
	case in.From != "" || in.To != "":
		if in.From == "" || in.To == "" {
			return invalid("from and to go together")
		}
		var err error
		if from, err = time.ParseInLocation(time.DateOnly, in.From, applemeta.Pacific()); err != nil {
			return invalid("from must be YYYY-MM-DD")
		}
		if to, err = time.ParseInLocation(time.DateOnly, in.To, applemeta.Pacific()); err != nil {
			return invalid("to must be YYYY-MM-DD")
		}
		if to.Before(from) {
			return invalid("to is before from")
		}
	default:
		p := in.Period
		if p == "" {
			p = "yesterday"
		}
		var err error
		if from, to, _, err = applemeta.Period(p, now); err != nil {
			return invalid("%v", err)
		}
	}
	w := salesWindow{Freq: applemeta.Daily, From: from.Format(time.DateOnly), To: to.Format(time.DateOnly)}
	n := 0
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if n++; n > salesMaxDays {
			return invalid("range longer than %d days; use frequency MONTHLY or YEARLY with report_date", salesMaxDays)
		}
		if d.Before(today) {
			w.Fetch = append(w.Fetch, d.Format(time.DateOnly))
		} else {
			w.Pending = append(w.Pending, d.Format(time.DateOnly))
		}
	}
	return w, nil
}

// checkReportDate validates the report date format per frequency.
func checkReportDate(f applemeta.Frequency, date string) error {
	layout := map[applemeta.Frequency]string{
		applemeta.Daily: time.DateOnly, applemeta.Weekly: time.DateOnly,
		applemeta.Monthly: "2006-01", applemeta.Yearly: "2006",
	}[f]
	if layout == "" {
		return fmt.Errorf("%w: unknown frequency %q", tool.ErrInvalidArgs, f)
	}
	t, err := time.Parse(layout, date)
	if err != nil {
		return fmt.Errorf("%w: report_date %q must look like %s for %s", tool.ErrInvalidArgs, date, layout, f)
	}
	if f == applemeta.Weekly && t.Weekday() != time.Sunday {
		return fmt.Errorf("%w: WEEKLY report_date must be the week's Sunday (%s)", tool.ErrInvalidArgs, applemeta.ReportDate(applemeta.Weekly, t))
	}
	return nil
}

// salesOut is the sales_report result.
type salesOut struct {
	ReportType  string                     `json:"report_type"`
	SubType     string                     `json:"report_subtype"`
	Frequency   string                     `json:"frequency"`
	From        string                     `json:"from"`
	To          string                     `json:"to"`
	App         *tool.App                  `json:"app,omitempty"`
	CoveredDays []string                   `json:"covered_days"`
	PendingDays []string                   `json:"pending_days,omitempty"`
	Rows        int                        `json:"rows"`
	Units       map[reports.Category]int64 `json:"units,omitempty"`
	Proceeds    []amountOut                `json:"proceeds,omitempty"`
	Groups      []reports.SalesGroup       `json:"groups,omitempty"`
	Omitted     int                        `json:"groups_omitted,omitempty"`
	USD         *usdEstimate               `json:"proceeds_estimate_usd,omitempty"`
	// Typed summaries for subscription report types.
	Subscriptions *reports.SubscriptionsSummary `json:"subscriptions,omitempty"`
	SnapshotDay   string                        `json:"snapshot_day,omitempty"`
	Events        *reports.EventsSummary        `json:"events,omitempty"`
	Subscribers   *subscriberSummary            `json:"subscribers,omitempty"`
	// Columns/Sample are decoded rows for report types without aggregation.
	Columns []string            `json:"columns,omitempty"`
	Sample  []map[string]string `json:"sample,omitempty"`

	proceedsMoney reports.Money // exact totals for the USD estimate
}

// usdEstimate converts proceeds with Apple's own FX rates; it is labelled as
// an estimate because rates come from a closed fiscal month.
type usdEstimate struct {
	Amount      string `json:"amount"`
	Label       string `json:"label"`
	FXSource    string `json:"fx_source"`
	SourceMonth string `json:"fx_source_month"`
}

// subscriberSummary totals a SUBSCRIBER report.
type subscriberSummary struct {
	Units       int64            `json:"units"`
	Refunds     int64            `json:"refunds"`
	Proceeds    []amountOut      `json:"proceeds,omitempty"`
	ByOfferType map[string]int64 `json:"by_offer_type,omitempty"`
}

func salesReport(ctx context.Context, env *tool.Env, in salesIn) (*tool.Result, error) {
	vendor, err := requireVendor(env)
	if err != nil {
		return nil, err
	}
	rt := applemeta.ReportType(in.ReportType)
	if rt == "" {
		rt = applemeta.ReportSales
	}
	rs, ok := applemeta.SalesReport(rt)
	if !ok {
		return nil, fmt.Errorf("%w: unknown report_type %q", tool.ErrInvalidArgs, rt)
	}
	sub := in.SubType
	if sub == "" {
		sub = rs.SubTypes[0]
	}
	w, err := resolveSalesWindow(in, env.Clock.Now())
	if err != nil {
		return nil, err
	}
	if err := rs.Validate(sub, w.Freq); err != nil {
		return nil, fmt.Errorf("%w: %v; pass frequency and report_date", tool.ErrInvalidArgs, err)
	}
	topN := in.TopN
	if topN <= 0 {
		topN = salesDefaultTopN
	}
	topN = min(topN, salesMaxTopN)
	var app *tool.App
	if in.App != "" {
		a, err := env.Resolve.App(ctx, in.App)
		if err != nil {
			return nil, err
		}
		app = &a
	}

	days, err := fetchSalesDates(ctx, env, vendor, rs, sub, w.Freq, w.Fetch)
	if err != nil {
		return nil, err
	}
	out := salesOut{
		ReportType: string(rt), SubType: sub, Frequency: string(w.Freq), From: w.From, To: w.To,
		App: app, CoveredDays: []string{}, PendingDays: w.Pending,
	}
	var tables []dayTable
	for _, d := range days {
		if d.Pending {
			out.PendingDays = append(out.PendingDays, d.Date)
			continue
		}
		out.CoveredDays = append(out.CoveredDays, d.Date)
		tables = append(tables, d)
	}
	slices.Sort(out.PendingDays)

	res := tool.OK(&out)
	switch rt {
	case applemeta.ReportSales:
		if err := salesAggregate(&out, tables, app, in.GroupBy, topN); err != nil {
			return nil, err
		}
		if in.WithUSD {
			est, warn, err := salesUSDEstimate(ctx, env, vendor, out.proceedsMoney)
			if err != nil {
				return nil, err
			}
			out.USD = est
			if warn != "" {
				res.Warn(warn)
			}
		}
	case applemeta.ReportSubscription:
		if err := subscriptionSnapshot(&out, tables, app); err != nil {
			return nil, err
		}
	case applemeta.ReportSubscriptionEvent:
		if err := subscriptionEvents(&out, tables, app); err != nil {
			return nil, err
		}
	case applemeta.ReportSubscriber:
		if err := subscribers(&out, tables, app); err != nil {
			return nil, err
		}
	default:
		if warn := rawRows(&out, tables, app); warn != "" {
			res.Warn(warn)
		}
	}
	if in.WithUSD && rt != applemeta.ReportSales {
		res.Warn("with_usd_estimate applies to report_type SALES only")
	}
	if len(out.CoveredDays) == 0 && len(out.PendingDays) > 0 {
		res.Warn("no report published yet for this range; Apple publishes daily reports the next day (Pacific time)")
	}
	out.proceedsMoney = nil
	return res, nil
}

// appMatchesSales reports whether a SALES row belongs to app: the app row
// itself (Apple Identifier or SKU) or one of its in-app purchases (Parent
// Identifier = app SKU).
func appMatchesSales(r reports.SalesRow, app *tool.App) bool {
	if app == nil {
		return true
	}
	if r.AppleID != "" && r.AppleID == app.ID {
		return true
	}
	return app.SKU != "" && (r.SKU == app.SKU || r.ParentID == app.SKU)
}

func salesAggregate(out *salesOut, tables []dayTable, app *tool.App, groupBy []string, topN int) error {
	var rows []reports.SalesRow
	for _, d := range tables {
		rs, err := reports.SalesRows(d.Table)
		if err != nil {
			return fmt.Errorf("sales report %s: %w", d.Date, err)
		}
		for _, r := range rs {
			if appMatchesSales(r, app) {
				rows = append(rows, r)
			}
		}
	}
	dims := make([]reports.Dim, 0, len(groupBy))
	for _, g := range groupBy {
		dims = append(dims, reports.Dim(g))
	}
	agg := reports.AggregateSales(rows, dims, topN, salesCategory)
	out.Rows = len(rows)
	out.Units = agg.Totals.Units
	out.Proceeds = sortedAmounts(agg.Totals.Proceeds)
	out.proceedsMoney = agg.Totals.Proceeds
	out.Groups = agg.Groups
	out.Omitted = agg.Omitted
	return nil
}

// salesUSDEstimate converts proceeds with the FX rates of the latest closed
// fiscal month's FINANCE_DETAIL report. It returns no estimate (and a warning)
// when no such report is published or a currency has no rate; it never guesses.
func salesUSDEstimate(ctx context.Context, env *tool.Env, vendor string, proceeds reports.Money) (*usdEstimate, string, error) {
	if len(proceeds) == 0 {
		return nil, "", nil
	}
	region, _ := applemeta.FinanceRegion(applemeta.ReportFinanceDetail)
	now := env.Clock.Now()
	for back := range 2 {
		month := lastClosedMonth(now, back)
		t, pending, err := download(ctx, env, "/v1/financeReports", financeQuery(vendor, applemeta.ReportFinanceDetail, region, month))
		if err != nil {
			return nil, "", err
		}
		if pending {
			continue
		}
		rows, err := reports.FinanceRows(t)
		if err != nil {
			return nil, "", fmt.Errorf("finance report %s: %w", month, err)
		}
		rates := fxRates(rows)
		total := new(big.Rat)
		var missing []string
		for _, c := range proceeds.Currencies() {
			rate, ok := rates[c]
			if !ok {
				missing = append(missing, c)
				continue
			}
			total.Add(total, new(big.Rat).Mul(proceeds[c], rate))
		}
		if len(missing) > 0 {
			return nil, fmt.Sprintf("no USD estimate: finance report %s has no exchange rate for %s", month, strings.Join(missing, ", ")), nil
		}
		return &usdEstimate{
			Amount: reports.FormatAmount("USD", total), Label: "estimate",
			FXSource: "Apple exchange rates, finance detail report", SourceMonth: month,
		}, "", nil
	}
	return nil, "no USD estimate: no finance report for the last closed fiscal months is published yet", nil
}

func subscriptionSnapshot(out *salesOut, tables []dayTable, app *tool.App) error {
	if len(tables) == 0 {
		return nil
	}
	// Active counts are a daily snapshot; summing days would double count.
	last := tables[len(tables)-1]
	rows, err := reports.SubscriptionRows(last.Table)
	if err != nil {
		return fmt.Errorf("subscription report %s: %w", last.Date, err)
	}
	if app != nil {
		rows = slices.DeleteFunc(rows, func(r reports.SubscriptionRow) bool { return r.AppAppleID != app.ID })
	}
	s := reports.SubscriptionSummary(rows)
	out.Rows, out.Subscriptions, out.SnapshotDay = len(rows), &s, last.Date
	return nil
}

func subscriptionEvents(out *salesOut, tables []dayTable, app *tool.App) error {
	var rows []reports.SubscriptionEventRow
	for _, d := range tables {
		rs, err := reports.SubscriptionEventRows(d.Table)
		if err != nil {
			return fmt.Errorf("subscription event report %s: %w", d.Date, err)
		}
		for _, r := range rs {
			if app == nil || strings.EqualFold(r.AppName, app.Name) {
				rows = append(rows, r)
			}
		}
	}
	s := reports.EventSummary(rows)
	out.Rows, out.Events = len(rows), &s
	return nil
}

func subscribers(out *salesOut, tables []dayTable, app *tool.App) error {
	s := subscriberSummary{ByOfferType: map[string]int64{}}
	proceeds := reports.Money{}
	for _, d := range tables {
		rs, err := reports.SubscriberRows(d.Table)
		if err != nil {
			return fmt.Errorf("subscriber report %s: %w", d.Date, err)
		}
		for _, r := range rs {
			if app != nil && !strings.EqualFold(r.AppName, app.Name) {
				continue
			}
			out.Rows++
			s.Units += r.Units
			if r.Refund {
				s.Refunds += r.Units
			}
			if r.OfferType != "" {
				s.ByOfferType[r.OfferType] += r.Units
			}
			if r.DeveloperProceeds != nil {
				proceeds.Add(r.ProceedsCurrency, new(big.Rat).Mul(new(big.Rat).SetInt64(r.Units), r.DeveloperProceeds))
			}
		}
	}
	s.Proceeds = sortedAmounts(proceeds)
	out.Subscribers = &s
	return nil
}

// rawRows returns decoded rows for report types without a typed summary,
// filtered by app on an identifier column when the report has one.
func rawRows(out *salesOut, tables []dayTable, app *tool.App) string {
	warn := ""
	for _, d := range tables {
		t := d.Table
		rows := t.Rows
		if app != nil {
			cols := []int{}
			for _, name := range []string{"Apple Identifier", "App Apple ID", "SKU", "Parent Identifier"} {
				if i := t.Col(name); i >= 0 {
					cols = append(cols, i)
				}
			}
			if len(cols) == 0 {
				warn = "this report has no app identifier column; the app filter was not applied"
			} else {
				rows = slices.DeleteFunc(slices.Clone(rows), func(r []string) bool {
					for _, i := range cols {
						if v := r[i]; v != "" && (v == app.ID || v == app.SKU) {
							return false
						}
					}
					return true
				})
			}
		}
		if out.Columns == nil {
			out.Columns = t.Columns
		}
		out.Rows += len(rows)
		if room := salesMaxRawRows - len(out.Sample); room > 0 {
			out.Sample = append(out.Sample, tableRows(t, rows, room)...)
		}
	}
	return warn
}
