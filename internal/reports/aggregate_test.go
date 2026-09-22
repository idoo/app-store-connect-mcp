package reports

import (
	"encoding/json"
	"errors"
	"flag"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "golden", name+".json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("golden %s mismatch (-want +got):\n%s", name, diff)
	}
}

func TestParseAmount(t *testing.T) {
	tests := []struct {
		in, want string
		bad      bool
	}{
		{"", "0", false},
		{"0.70", "7/10", false},
		{"-2.79", "-279/100", false},
		{"(3.10)", "-31/10", false},
		{"1,234.56", "30864/25", false},
		{" 420 ", "420", false},
		{"1e3", "", true},
		{"1/3", "", true},
		{"1,234", "", true},
		{"abc", "", true},
		{"()", "", true},
	}
	for _, tt := range tests {
		got, err := ParseAmount(tt.in)
		if tt.bad {
			if !errors.Is(err, ErrCorrupt) {
				t.Errorf("ParseAmount(%q) err = %v, want ErrCorrupt", tt.in, err)
			}
			continue
		}
		if err != nil || got.RatString() != tt.want {
			t.Errorf("ParseAmount(%q) = %v, %v; want %s", tt.in, got, err, tt.want)
		}
	}
}

func TestMoneyExact(t *testing.T) {
	m := Money{}
	for _, s := range []string{"0.1", "0.2"} {
		v, err := ParseAmount(s)
		if err != nil {
			t.Fatal(err)
		}
		m.Add("usd", v)
	}
	if m["USD"].Cmp(big.NewRat(3, 10)) != 0 {
		t.Fatalf("0.1+0.2 = %s, want exactly 3/10", m["USD"].RatString())
	}
	// Ten thousand 0.01 additions must stay exact.
	cents := Money{}
	one := big.NewRat(1, 100)
	for range 10000 {
		cents.Add("EUR", one)
	}
	if cents["EUR"].Cmp(big.NewRat(100, 1)) != 0 {
		t.Fatalf("sum = %s", cents["EUR"].RatString())
	}
	m.Add("JPY", big.NewRat(4205, 10))
	m.Add("KWD", big.NewRat(12345, 10000))
	m.Add("", big.NewRat(1, 1))
	if got, want := m.String(), "JPY 421, KWD 1.235, UNKNOWN 1.00, USD 0.30"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	b, err := json.Marshal(m)
	if err != nil || !strings.Contains(string(b), `"USD":"0.30"`) {
		t.Fatalf("MarshalJSON = %s, %v", b, err)
	}
}

func TestDefaultCategory(t *testing.T) {
	tests := map[string]Category{
		"1F": CategoryDownload, "7": CategoryUpdate, "3F": CategoryRedownload, "ia1": CategoryIAP,
		"IAY": CategorySubscription, " IAC ": CategorySubscription, "ZZ": CategoryOther, "": CategoryOther,
		"IA3": CategoryRestore, "ia3-m": CategoryRestore,
	}
	for in, want := range tests {
		if got := DefaultCategory(in); got != want {
			t.Errorf("DefaultCategory(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestAggregateSalesGolden(t *testing.T) {
	rows, err := SalesRows(openFixture(t, "sales_summary.tsv.gz"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		by   []Dim
		topN int
	}{
		{"sales_totals", nil, 0},
		{"sales_by_app_category", []Dim{DimApp, DimProductCategory}, 0},
		{"sales_by_country_top2", []Dim{DimCountry}, 2},
		{"sales_by_sku_currency", []Dim{DimSKU, DimCurrency}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			golden(t, tt.name, AggregateSales(rows, tt.by, tt.topN, nil))
		})
	}
}

func TestAggregateSalesTotals(t *testing.T) {
	rows, err := SalesRows(openFixture(t, "sales_summary.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	rep := AggregateSales(rows, []Dim{DimCountry}, 2, nil)
	if got, want := tooljson(t, rep.Totals.Units), `{"download":17,"iap":5,"redownload":4,"subscription":7,"update":30}`; got != want {
		t.Fatalf("units = %s, want %s", got, want)
	}
	// 3×0.70 + 7×2.79 − 1×2.79 = 18.84 USD; 2×0.69 EUR; 1×420 JPY.
	if got := rep.Totals.Proceeds.String(); got != "EUR 1.38, JPY 420, USD 18.84" {
		t.Fatalf("proceeds = %q", got)
	}
	if len(rep.Groups) != 2 || rep.Omitted != 1 || rep.Groups[0].Key[DimCountry] != "US" {
		t.Fatalf("topN groups = %+v omitted=%d", rep.Groups, rep.Omitted)
	}
	custom := AggregateSales(rows, nil, 0, func(string) Category { return CategoryOther })
	if custom.Totals.Units.Get(CategoryOther).Cmp(big.NewRat(63, 1)) != 0 {
		t.Fatalf("injected categorizer ignored: %v", custom.Totals.Units)
	}
}

func TestSalesRowsMissingColumn(t *testing.T) {
	tab := &Table{Columns: []string{"SKU", "Units"}}
	if _, err := SalesRows(tab); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

// Apple types Units as DECIMAL(18,2): fractional and negative values are
// valid and must be summed exactly, never rejected.
func TestSalesRowsDecimalUnits(t *testing.T) {
	tab := &Table{
		Columns: []string{"Product Type Identifier", "Units", "Developer Proceeds", "Currency of Proceeds"},
		Rows:    [][]string{{"1F", "1.5", "0", "USD"}, {"IA1", "-0.50", "2.00", "USD"}, {"IA1", "2", "2.00", "USD"}, {"IA3", "1", "0", "USD"}},
	}
	rows, err := SalesRows(tab)
	if err != nil {
		t.Fatalf("decimal units rejected: %v", err)
	}
	rep := AggregateSales(rows, nil, 0, nil)
	if got, want := tooljson(t, rep.Totals.Units), `{"download":1.5,"iap":1.5,"restore":1}`; got != want {
		t.Fatalf("units = %s, want %s", got, want)
	}
	if got := rep.Totals.Proceeds.String(); got != "USD 3.00" {
		t.Fatalf("proceeds = %q", got)
	}
}

func tooljson(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEventSummaryGolden(t *testing.T) {
	rows, err := SubscriptionEventRows(openFixture(t, "subscription_event.tsv.gz"))
	if err != nil {
		t.Fatal(err)
	}
	s := EventSummary(rows)
	if s.Conversions != 5 || s.Cancellations != 5 || s.Refunds != 1 || s.Starts != 11 {
		t.Fatalf("summary = %+v", s)
	}
	golden(t, "subscription_events", s)
}

func TestSubscriptionSummary(t *testing.T) {
	in := "App Name\tSubscription Name\tSubscription Apple ID\tCountry\tActive Standard Price Subscriptions\t" +
		"Active Free Trial Introductory Offer Subscriptions\tActive Pay Up Front Introductory Offer Subscriptions\t" +
		"Free Trial Offer Code Subscriptions\tBilling Retry\tGrace Period\n" +
		"Example Notes\tNotes Monthly\t1000000099\tUS\t40\t9\t2\t1\t3\t1\n" +
		"Example Notes\tNotes Monthly\t1000000099\tDE\t10\t0\t0\t0\t0\t0\n" +
		"Example Notes\tNotes Yearly\t1000000098\tUS\t70\t5\t0\t0\t1\t0\n"
	tab, err := Decode(strings.NewReader(in), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := SubscriptionRows(tab)
	if err != nil {
		t.Fatal(err)
	}
	got := SubscriptionSummary(rows)
	want := SubscriptionsSummary{
		Active: 137, Trials: 15, Paid: 122, BillingRetry: 4, GracePeriod: 1,
		ByProduct: []ProductSubscriptions{
			{SubscriptionName: "Notes Yearly", SubscriptionID: "1000000098", Active: 75, Trials: 5, Paid: 70},
			{SubscriptionName: "Notes Monthly", SubscriptionID: "1000000099", Active: 62, Trials: 10, Paid: 52},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestSubscriberRows(t *testing.T) {
	in := "Event Date\tSubscription Name\tCustomer Price\tCustomer Currency\tDeveloper Proceeds\tProceeds Currency\tRefund\tUnits\n" +
		"2026-10-24\tNotes Monthly\t3.99\tUSD\t2.79\tUSD\t\t1\n" +
		"2026-10-24\tNotes Monthly\t3.99\tUSD\t2.79\tUSD\tYes\t1\n"
	tab, err := Decode(strings.NewReader(in), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := SubscriberRows(tab)
	if err != nil || len(rows) != 2 || rows[0].Refund || !rows[1].Refund || rows[1].DeveloperProceeds.RatString() != "279/100" {
		t.Fatalf("rows = %+v, err = %v", rows, err)
	}
}

func TestFinanceSummary(t *testing.T) {
	plain, err := FinanceRows(openFixture(t, "finance_financial.tsv.gz"))
	if err != nil {
		t.Fatal(err)
	}
	sum := FinanceSummary(plain)
	if got := sum.Proceeds.String(); got != "EUR 1.38, JPY 420, USD 27.21" || sum.Units != 15 {
		t.Fatalf("proceeds = %q units = %d", got, sum.Units)
	}
	// Apple's finance reports have no exchange-rate column: no USD conversion.
	b, err := json.Marshal(sum)
	if err != nil || strings.Contains(string(b), "usd") || strings.Contains(string(b), "rate") {
		t.Fatalf("finance summary converts currencies: %s %v", b, err)
	}
	golden(t, "finance_summary", sum)
}
