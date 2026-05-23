package reports

import (
	"math/big"
	"sort"
	"strings"
)

// Dim is a sales grouping dimension.
type Dim string

// Sales grouping dimensions.
const (
	DimApp             Dim = "app"
	DimSKU             Dim = "sku"
	DimCountry         Dim = "country"
	DimDate            Dim = "date"
	DimProductCategory Dim = "product_category"
	DimCurrency        Dim = "currency"
)

// SalesGroup is the aggregate for one combination of dimension values.
type SalesGroup struct {
	Key      map[Dim]string     `json:"key,omitempty"`
	Units    map[Category]int64 `json:"units"`
	Proceeds Money              `json:"proceeds"`
}

func newSalesGroup(key map[Dim]string) *SalesGroup {
	return &SalesGroup{Key: key, Units: map[Category]int64{}, Proceeds: Money{}}
}

func (g *SalesGroup) add(r SalesRow, c Category) {
	g.Units[c] += r.Units
	g.Proceeds.Add(r.ProceedsCurrency, r.TotalProceeds())
}

func (g *SalesGroup) totalUnits() int64 {
	var n int64
	for _, u := range g.Units {
		n += u
	}
	return n
}

// SalesReport is the result of AggregateSales.
type SalesReport struct {
	Totals SalesGroup   `json:"totals"`
	Groups []SalesGroup `json:"groups,omitempty"`
	// Omitted counts groups dropped by the topN cut.
	Omitted int `json:"omitted,omitempty"`
}

// salesDim extracts a dimension value from a row. The app of an in-app
// purchase is its Parent Identifier (the parent app SKU).
func salesDim(d Dim, r SalesRow, c Category) string {
	switch d {
	case DimApp:
		if r.ParentID != "" {
			return r.ParentID
		}
		return r.SKU
	case DimSKU:
		return r.SKU
	case DimCountry:
		return r.CountryCode
	case DimDate:
		return r.BeginDate
	case DimProductCategory:
		return string(c)
	case DimCurrency:
		return strings.ToUpper(r.ProceedsCurrency)
	default:
		return ""
	}
}

// AggregateSales sums units by product category and proceeds per currency,
// grouped by the given dimensions. Groups are ordered by total units
// descending, then key; topN > 0 keeps only the first topN groups. A nil cat
// uses DefaultCategory.
func AggregateSales(rows []SalesRow, by []Dim, topN int, cat Categorizer) SalesReport {
	if cat == nil {
		cat = DefaultCategory
	}
	total := newSalesGroup(nil)
	groups := map[string]*SalesGroup{}
	for _, r := range rows {
		c := cat(r.ProductTypeID)
		total.add(r, c)
		if len(by) == 0 {
			continue
		}
		key := make(map[Dim]string, len(by))
		parts := make([]string, len(by))
		for i, d := range by {
			key[d] = salesDim(d, r, c)
			parts[i] = key[d]
		}
		id := strings.Join(parts, "\x00")
		g, ok := groups[id]
		if !ok {
			g = newSalesGroup(key)
			groups[id] = g
		}
		g.add(r, c)
	}
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		ui, uj := groups[ids[i]].totalUnits(), groups[ids[j]].totalUnits()
		if ui != uj {
			return ui > uj
		}
		return ids[i] < ids[j]
	})
	rep := SalesReport{Totals: *total}
	for i, id := range ids {
		if topN > 0 && i >= topN {
			rep.Omitted = len(ids) - topN
			break
		}
		rep.Groups = append(rep.Groups, *groups[id])
	}
	return rep
}

// ProductSubscriptions are the active counts for one subscription product.
type ProductSubscriptions struct {
	SubscriptionName string `json:"subscription_name"`
	SubscriptionID   string `json:"subscription_id,omitempty"`
	Active           int64  `json:"active"`
	Trials           int64  `json:"trials"`
	Paid             int64  `json:"paid"`
}

// SubscriptionsSummary totals a SUBSCRIPTION report.
type SubscriptionsSummary struct {
	Active       int64                  `json:"active"`
	Trials       int64                  `json:"trials"`
	Paid         int64                  `json:"paid"`
	BillingRetry int64                  `json:"billing_retry"`
	GracePeriod  int64                  `json:"grace_period"`
	ByProduct    []ProductSubscriptions `json:"by_product"`
}

// SubscriptionSummary totals active, trial and paid subscriptions, overall
// and per product (ordered by active descending, then name).
func SubscriptionSummary(rows []SubscriptionRow) SubscriptionsSummary {
	var s SubscriptionsSummary
	by := map[string]*ProductSubscriptions{}
	for _, r := range rows {
		s.Trials += r.Trials
		s.Paid += r.Paid
		s.BillingRetry += r.BillingRetry
		s.GracePeriod += r.GracePeriod
		id := r.SubscriptionID + "\x00" + r.SubscriptionName
		p, ok := by[id]
		if !ok {
			p = &ProductSubscriptions{SubscriptionName: r.SubscriptionName, SubscriptionID: r.SubscriptionID}
			by[id] = p
		}
		p.Trials += r.Trials
		p.Paid += r.Paid
		p.Active += r.Trials + r.Paid
	}
	s.Active = s.Trials + s.Paid
	s.ByProduct = make([]ProductSubscriptions, 0, len(by))
	for _, p := range by {
		s.ByProduct = append(s.ByProduct, *p)
	}
	sort.Slice(s.ByProduct, func(i, j int) bool {
		a, b := s.ByProduct[i], s.ByProduct[j]
		if a.Active != b.Active {
			return a.Active > b.Active
		}
		return a.SubscriptionName+a.SubscriptionID < b.SubscriptionName+b.SubscriptionID
	})
	return s
}

// EventsSummary totals a SUBSCRIPTION_EVENT report.
type EventsSummary struct {
	ByEvent       map[string]int64 `json:"by_event"`
	Conversions   int64            `json:"conversions"`
	Cancellations int64            `json:"cancellations"`
	Refunds       int64            `json:"refunds"`
	Starts        int64            `json:"starts"`
}

// EventSummary sums event quantities by event name and classifies them:
// conversions (paid from an introductory offer or free trial), cancellations
// (cancel / opt-out of auto-renew), refunds and starts.
func EventSummary(rows []SubscriptionEventRow) EventsSummary {
	s := EventsSummary{ByEvent: map[string]int64{}}
	for _, r := range rows {
		s.ByEvent[r.Event] += r.Quantity
		e := strings.ToLower(r.Event)
		switch {
		case strings.Contains(e, "refund"):
			s.Refunds += r.Quantity
		case strings.HasPrefix(e, "paid subscription from") || strings.Contains(e, "converted"):
			s.Conversions += r.Quantity
		case strings.Contains(e, "cancel") || strings.Contains(e, "opt-out") ||
			strings.Contains(e, "turned off auto-renew"):
			s.Cancellations += r.Quantity
		case strings.HasPrefix(e, "start "), strings.HasPrefix(e, "subscribe"):
			s.Starts += r.Quantity
		}
	}
	return s
}

// FXSource labels a TotalUSD computed from exchange rates in the file.
const FXSource = "exchange rates in report file (estimate)"

// USDOnlySource labels a TotalUSD from a report whose proceeds are all USD.
const USDOnlySource = "all proceeds in USD"

// FinanceTotals summarizes a FINANCIAL / FINANCE_DETAIL report.
type FinanceTotals struct {
	Proceeds Money `json:"proceeds"`
	Units    int64 `json:"units"`
	// TotalUSD is set only when every non-USD row carries an exchange rate.
	TotalUSD       *string `json:"total_usd,omitempty"`
	TotalUSDSource string  `json:"total_usd_source,omitempty"`
	// MissingRates lists currencies without a rate (TotalUSD unavailable).
	MissingRates []string `json:"missing_rates,omitempty"`
}

// FinanceSummary sums extended partner share per currency. USD is never
// guessed: TotalUSD is computed exactly from per-row exchange rates, and only
// if every non-USD row has one.
func FinanceSummary(rows []FinanceRow) FinanceTotals {
	s := FinanceTotals{Proceeds: Money{}}
	usd := new(big.Rat)
	missing := map[string]bool{}
	usedFX := false
	for _, r := range rows {
		s.Units += r.Quantity
		s.Proceeds.Add(r.PartnerShareCurrency, r.ExtendedPartnerShare)
		cur := strings.ToUpper(strings.TrimSpace(r.PartnerShareCurrency))
		switch {
		case cur == "USD":
			usd.Add(usd, r.ExtendedPartnerShare)
		case r.ExchangeRate != nil:
			usedFX = true
			usd.Add(usd, new(big.Rat).Mul(r.ExtendedPartnerShare, r.ExchangeRate))
		default:
			missing[cur] = true
		}
	}
	if len(missing) > 0 {
		for c := range missing {
			s.MissingRates = append(s.MissingRates, c)
		}
		sort.Strings(s.MissingRates)
		return s
	}
	if len(rows) > 0 {
		v := FormatAmount("USD", usd)
		s.TotalUSD = &v
		s.TotalUSDSource = USDOnlySource
		if usedFX {
			s.TotalUSDSource = FXSource
		}
	}
	return s
}
