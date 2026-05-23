package reports

import (
	"fmt"
	"math/big"
	"strings"
)

// colSpec names one logical field and the header spellings Apple has used.
type colSpec struct {
	key      string
	names    []string
	required bool
}

// colMap is a resolved field → column index map; -1 means absent.
type colMap map[string]int

func mapCols(t *Table, kind string, specs []colSpec) (colMap, error) {
	m := make(colMap, len(specs))
	var missing []string
	for _, s := range specs {
		i := t.colAny(s.names...)
		if i < 0 && s.required {
			missing = append(missing, s.names[0])
		}
		m[s.key] = i
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("reports: %s report missing columns %s: %w", kind, strings.Join(missing, ", "), ErrCorrupt)
	}
	return m, nil
}

func (m colMap) str(row []string, key string) string {
	if i := m[key]; i >= 0 && i < len(row) {
		return row[i]
	}
	return ""
}

func (m colMap) has(key string) bool { return m[key] >= 0 }

func (m colMap) amount(row []string, key string, line int) (*big.Rat, error) {
	v, err := ParseAmount(m.str(row, key))
	if err != nil {
		return nil, fmt.Errorf("row %d %s: %w", line, key, err)
	}
	return v, nil
}

func (m colMap) count(row []string, key string, line int) (int64, error) {
	v, err := m.amount(row, key, line)
	if err != nil {
		return 0, err
	}
	if !v.IsInt() || !v.Num().IsInt64() {
		return 0, fmt.Errorf("reports: row %d %s: %q is not an integer: %w", line, key, m.str(row, key), ErrCorrupt)
	}
	return v.Num().Int64(), nil
}

// SalesRow is one line of a Sales SUMMARY report.
type SalesRow struct {
	Provider          string
	SKU               string
	Title             string
	Version           string
	ProductTypeID     string
	Units             int64
	DeveloperProceeds *big.Rat // per unit, in ProceedsCurrency
	BeginDate         string
	EndDate           string
	CustomerCurrency  string
	CountryCode       string
	ProceedsCurrency  string
	AppleID           string
	CustomerPrice     *big.Rat
	ParentID          string
	Subscription      string
	Period            string
	Device            string
}

// TotalProceeds returns Units × DeveloperProceeds.
func (r SalesRow) TotalProceeds() *big.Rat {
	return new(big.Rat).Mul(new(big.Rat).SetInt64(r.Units), r.DeveloperProceeds)
}

var salesSpecs = []colSpec{
	{"provider", []string{"Provider"}, false},
	{"sku", []string{"SKU"}, false},
	{"title", []string{"Title"}, false},
	{"version", []string{"Version"}, false},
	{"ptype", []string{"Product Type Identifier"}, true},
	{"units", []string{"Units"}, true},
	{"proceeds", []string{"Developer Proceeds"}, true},
	{"begin", []string{"Begin Date"}, false},
	{"end", []string{"End Date"}, false},
	{"ccur", []string{"Customer Currency"}, false},
	{"country", []string{"Country Code"}, false},
	{"pcur", []string{"Currency of Proceeds"}, true},
	{"appleid", []string{"Apple Identifier"}, false},
	{"price", []string{"Customer Price"}, false},
	{"parent", []string{"Parent Identifier"}, false},
	{"sub", []string{"Subscription"}, false},
	{"period", []string{"Period"}, false},
	{"device", []string{"Device"}, false},
}

// SalesRows maps a Sales SUMMARY table to typed rows.
func SalesRows(t *Table) ([]SalesRow, error) {
	m, err := mapCols(t, "sales", salesSpecs)
	if err != nil {
		return nil, err
	}
	out := make([]SalesRow, 0, len(t.Rows))
	for i, row := range t.Rows {
		units, err := m.count(row, "units", i+1)
		if err != nil {
			return nil, err
		}
		proceeds, err := m.amount(row, "proceeds", i+1)
		if err != nil {
			return nil, err
		}
		price, err := m.amount(row, "price", i+1)
		if err != nil {
			return nil, err
		}
		out = append(out, SalesRow{
			Provider: m.str(row, "provider"), SKU: m.str(row, "sku"), Title: m.str(row, "title"),
			Version: m.str(row, "version"), ProductTypeID: m.str(row, "ptype"), Units: units,
			DeveloperProceeds: proceeds, BeginDate: m.str(row, "begin"), EndDate: m.str(row, "end"),
			CustomerCurrency: m.str(row, "ccur"), CountryCode: m.str(row, "country"),
			ProceedsCurrency: m.str(row, "pcur"), AppleID: m.str(row, "appleid"), CustomerPrice: price,
			ParentID: m.str(row, "parent"), Subscription: m.str(row, "sub"), Period: m.str(row, "period"),
			Device: m.str(row, "device"),
		})
	}
	return out, nil
}

// SubscriptionRow is one line of a SUBSCRIPTION report (active counts).
type SubscriptionRow struct {
	AppName          string
	AppAppleID       string
	SubscriptionName string
	SubscriptionID   string
	GroupID          string
	Duration         string
	Country          string
	ProceedsCurrency string
	DeveloperProceed *big.Rat
	// Paid sums standard-price, pay-up-front and pay-as-you-go actives.
	Paid int64
	// Trials sums free-trial actives across introductory, promotional and
	// offer-code offers.
	Trials       int64
	BillingRetry int64
	GracePeriod  int64
}

var subscriptionCountCols = map[string]bool{ // column → is trial
	"Active Standard Price Subscriptions":                   false,
	"Active Free Trial Introductory Offer Subscriptions":    true,
	"Active Pay Up Front Introductory Offer Subscriptions":  false,
	"Active Pay As You Go Introductory Offer Subscriptions": false,
	"Free Trial Promotional Offer Subscriptions":            true,
	"Pay Up Front Promotional Offer Subscriptions":          false,
	"Pay As You Go Promotional Offer Subscriptions":         false,
	"Free Trial Offer Code Subscriptions":                   true,
	"Pay Up Front Offer Code Subscriptions":                 false,
	"Pay As You Go Offer Code Subscriptions":                false,
	"Free Trial Win-back Offer Subscriptions":               true,
	"Pay Up Front Win-back Offer Subscriptions":             false,
	"Pay As You Go Win-back Offer Subscriptions":            false,
}

// SubscriptionRows maps a SUBSCRIPTION table to typed rows.
func SubscriptionRows(t *Table) ([]SubscriptionRow, error) {
	specs := []colSpec{
		{"app", []string{"App Name"}, false},
		{"appid", []string{"App Apple ID"}, false},
		{"name", []string{"Subscription Name"}, true},
		{"id", []string{"Subscription Apple ID"}, false},
		{"group", []string{"Subscription Group ID"}, false},
		{"dur", []string{"Standard Subscription Duration"}, false},
		{"country", []string{"Country"}, false},
		{"pcur", []string{"Proceeds Currency"}, false},
		{"proceeds", []string{"Developer Proceeds"}, false},
		{"retry", []string{"Billing Retry"}, false},
		{"grace", []string{"Grace Period"}, false},
	}
	for col := range subscriptionCountCols {
		specs = append(specs, colSpec{col, []string{col}, false})
	}
	m, err := mapCols(t, "subscription", specs)
	if err != nil {
		return nil, err
	}
	if !m.has("Active Standard Price Subscriptions") {
		return nil, fmt.Errorf("reports: subscription report missing Active Standard Price Subscriptions: %w", ErrCorrupt)
	}
	out := make([]SubscriptionRow, 0, len(t.Rows))
	for i, row := range t.Rows {
		r := SubscriptionRow{
			AppName: m.str(row, "app"), AppAppleID: m.str(row, "appid"), SubscriptionName: m.str(row, "name"),
			SubscriptionID: m.str(row, "id"), GroupID: m.str(row, "group"), Duration: m.str(row, "dur"),
			Country: m.str(row, "country"), ProceedsCurrency: m.str(row, "pcur"),
		}
		if r.DeveloperProceed, err = m.amount(row, "proceeds", i+1); err != nil {
			return nil, err
		}
		for col, trial := range subscriptionCountCols {
			n, err := m.count(row, col, i+1)
			if err != nil {
				return nil, err
			}
			if trial {
				r.Trials += n
			} else {
				r.Paid += n
			}
		}
		if r.BillingRetry, err = m.count(row, "retry", i+1); err != nil {
			return nil, err
		}
		if r.GracePeriod, err = m.count(row, "grace", i+1); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// SubscriptionEventRow is one line of a SUBSCRIPTION_EVENT report.
type SubscriptionEventRow struct {
	EventDate          string
	Event              string
	AppName            string
	SubscriptionName   string
	SubscriptionID     string
	Country            string
	CancellationReason string
	Quantity           int64
}

// SubscriptionEventRows maps a SUBSCRIPTION_EVENT table to typed rows.
func SubscriptionEventRows(t *Table) ([]SubscriptionEventRow, error) {
	m, err := mapCols(t, "subscription event", []colSpec{
		{"date", []string{"Event Date"}, false},
		{"event", []string{"Event"}, true},
		{"app", []string{"App Name"}, false},
		{"name", []string{"Subscription Name"}, false},
		{"id", []string{"Subscription Apple ID"}, false},
		{"country", []string{"Country"}, false},
		{"reason", []string{"Cancellation Reason"}, false},
		{"qty", []string{"Quantity"}, true},
	})
	if err != nil {
		return nil, err
	}
	out := make([]SubscriptionEventRow, 0, len(t.Rows))
	for i, row := range t.Rows {
		q, err := m.count(row, "qty", i+1)
		if err != nil {
			return nil, err
		}
		out = append(out, SubscriptionEventRow{
			EventDate: m.str(row, "date"), Event: m.str(row, "event"), AppName: m.str(row, "app"),
			SubscriptionName: m.str(row, "name"), SubscriptionID: m.str(row, "id"),
			Country: m.str(row, "country"), CancellationReason: m.str(row, "reason"), Quantity: q,
		})
	}
	return out, nil
}

// SubscriberRow is one line of a SUBSCRIBER report.
type SubscriberRow struct {
	EventDate         string
	AppName           string
	SubscriptionName  string
	SubscriptionID    string
	OfferType         string
	Country           string
	CustomerPrice     *big.Rat
	CustomerCurrency  string
	DeveloperProceeds *big.Rat
	ProceedsCurrency  string
	SubscriberID      string
	Refund            bool
	Units             int64
}

// SubscriberRows maps a SUBSCRIBER table to typed rows.
func SubscriberRows(t *Table) ([]SubscriberRow, error) {
	m, err := mapCols(t, "subscriber", []colSpec{
		{"date", []string{"Event Date"}, false},
		{"app", []string{"App Name"}, false},
		{"name", []string{"Subscription Name"}, false},
		{"id", []string{"Subscription Apple ID"}, false},
		{"offer", []string{"Subscription Offer Type"}, false},
		{"country", []string{"Country"}, false},
		{"price", []string{"Customer Price"}, false},
		{"ccur", []string{"Customer Currency"}, false},
		{"proceeds", []string{"Developer Proceeds"}, true},
		{"pcur", []string{"Proceeds Currency"}, true},
		{"subscriber", []string{"Subscriber ID"}, false},
		{"refund", []string{"Refund"}, false},
		{"units", []string{"Units"}, false},
	})
	if err != nil {
		return nil, err
	}
	out := make([]SubscriberRow, 0, len(t.Rows))
	for i, row := range t.Rows {
		r := SubscriberRow{
			EventDate: m.str(row, "date"), AppName: m.str(row, "app"), SubscriptionName: m.str(row, "name"),
			SubscriptionID: m.str(row, "id"), OfferType: m.str(row, "offer"), Country: m.str(row, "country"),
			CustomerCurrency: m.str(row, "ccur"), ProceedsCurrency: m.str(row, "pcur"),
			SubscriberID: m.str(row, "subscriber"), Refund: strings.EqualFold(m.str(row, "refund"), "yes"),
		}
		if r.CustomerPrice, err = m.amount(row, "price", i+1); err != nil {
			return nil, err
		}
		if r.DeveloperProceeds, err = m.amount(row, "proceeds", i+1); err != nil {
			return nil, err
		}
		if r.Units, err = m.count(row, "units", i+1); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// FinanceRow is one line of a FINANCIAL or FINANCE_DETAIL report.
type FinanceRow struct {
	StartDate            string
	EndDate              string
	VendorID             string
	AppleID              string
	Title                string
	ProductTypeID        string
	CountryOfSale        string
	Quantity             int64
	PartnerShare         *big.Rat
	ExtendedPartnerShare *big.Rat
	PartnerShareCurrency string
	SalesOrReturn        string
	CustomerPrice        *big.Rat
	CustomerCurrency     string
	// ExchangeRate converts PartnerShareCurrency to USD (USD = amount × rate);
	// nil when the file has no rate for this row.
	ExchangeRate *big.Rat
}

// FinanceRows maps a FINANCIAL or FINANCE_DETAIL table to typed rows.
func FinanceRows(t *Table) ([]FinanceRow, error) {
	m, err := mapCols(t, "finance", []colSpec{
		{"start", []string{"Start Date", "Transaction Date"}, false},
		{"end", []string{"End Date", "Settlement Date"}, false},
		{"vendor", []string{"Vendor Identifier", "SKU"}, false},
		{"appleid", []string{"Apple Identifier"}, false},
		{"title", []string{"Title"}, false},
		{"ptype", []string{"Product Type Identifier"}, false},
		{"country", []string{"Country Of Sale"}, false},
		{"qty", []string{"Quantity"}, true},
		{"share", []string{"Partner Share"}, false},
		{"ext", []string{"Extended Partner Share"}, true},
		{"cur", []string{"Partner Share Currency"}, true},
		{"sor", []string{"Sales or Return", "Sale or Return"}, false},
		{"price", []string{"Customer Price"}, false},
		{"ccur", []string{"Customer Currency"}, false},
		{"fx", []string{"Exchange Rate", "FX Rate"}, false},
	})
	if err != nil {
		return nil, err
	}
	out := make([]FinanceRow, 0, len(t.Rows))
	for i, row := range t.Rows {
		r := FinanceRow{
			StartDate: m.str(row, "start"), EndDate: m.str(row, "end"), VendorID: m.str(row, "vendor"),
			AppleID: m.str(row, "appleid"), Title: m.str(row, "title"), ProductTypeID: m.str(row, "ptype"),
			CountryOfSale: m.str(row, "country"), PartnerShareCurrency: m.str(row, "cur"),
			SalesOrReturn: m.str(row, "sor"), CustomerCurrency: m.str(row, "ccur"),
		}
		if r.Quantity, err = m.count(row, "qty", i+1); err != nil {
			return nil, err
		}
		if r.PartnerShare, err = m.amount(row, "share", i+1); err != nil {
			return nil, err
		}
		if r.ExtendedPartnerShare, err = m.amount(row, "ext", i+1); err != nil {
			return nil, err
		}
		if r.CustomerPrice, err = m.amount(row, "price", i+1); err != nil {
			return nil, err
		}
		if s := m.str(row, "fx"); s != "" {
			if r.ExchangeRate, err = m.amount(row, "fx", i+1); err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, nil
}
