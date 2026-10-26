package reports

import (
	"errors"
	"math/big"
	"testing"
)

// MA1: ISO 4217 minor units; HUF, TWD and IDR keep 2 decimals.
func TestFormatAmountMinorUnits(t *testing.T) {
	for _, tc := range []struct{ cur, in, want string }{
		{"JPY", "420", "420"},
		{"JPY", "204.5", "205"},
		{"KWD", "1.166", "1.166"},
		{"KWD", "0.5", "0.500"},
		{"HUF", "12.34", "12.34"},
		{"TWD", "20.95", "20.95"},
		{"IDR", "15000.50", "15000.50"},
		{"KRW", "1000", "1000"},
		{"USD", "0.7", "0.70"},
		{"usd", "-2.79", "-2.79"},
	} {
		v, err := ParseAmount(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := FormatAmount(tc.cur, v); got != tc.want {
			t.Errorf("FormatAmount(%s, %s) = %q, want %q", tc.cur, tc.in, got, tc.want)
		}
	}
}

// MA6: thousands separators parse; ambiguous commas stay errors.
func TestParseAmountThousands(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1,234.50", "2469/2"},
		{"1,234,567", "1234567"},
		{"-1,234,567.8", "-6172839/5"},
		{"(1,234.5)", "-2469/2"},
		{"999.99", "99999/100"},
		{"1234", "1234"},
	} {
		got, err := ParseAmount(tc.in)
		if err != nil || got.RatString() != tc.want {
			t.Errorf("ParseAmount(%q) = %v, %v; want %s", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"1,234", "1,23", "1,2345", "12,34.5", "1,234,56", ",123", "1234,567", "1.234,5", "1,,234", "-,123"} {
		if v, err := ParseAmount(in); !errors.Is(err, ErrCorrupt) {
			t.Errorf("ParseAmount(%q) = %v, %v; want ErrCorrupt", in, v, err)
		}
	}
}

// MA3: refunds (negative units) are reported per category and group next to
// net units; proceeds stay exact per currency (JPY, KWD, USD mixed).
func TestAggregateSalesRefunds(t *testing.T) {
	row := func(sku, country, cur, units, proceeds string) SalesRow {
		u, _ := new(big.Rat).SetString(units)
		p, _ := new(big.Rat).SetString(proceeds)
		return SalesRow{SKU: sku, ProductTypeID: "IA1", CountryCode: country, ProceedsCurrency: cur, Units: u, DeveloperProceeds: p}
	}
	rows := []SalesRow{
		row("pro", "JP", "JPY", "3", "102"),
		row("pro", "JP", "JPY", "-1", "102"),
		row("pro", "US", "USD", "1", "0.70"),
		row("pro", "US", "USD", "-1", "0.70"),
		row("pro", "US", "USD", "1", "0.70"),
		row("pro", "KW", "KWD", "2", "0.583"),
		{SKU: "app", ProductTypeID: "1F", CountryCode: "US", ProceedsCurrency: "USD", Units: big.NewRat(10, 1), DeveloperProceeds: new(big.Rat)},
	}
	rep := AggregateSales(rows, []Dim{DimCountry}, 0, nil)
	tot := rep.Totals
	cases := []struct{ name, got, want string }{
		{"net iap", FormatUnits(tot.Units.Get(CategoryIAP)), "5"},
		{"refund iap", FormatUnits(tot.Refunds.Get(CategoryIAP)), "2"},
		{"refund download", FormatUnits(tot.Refunds.Get(CategoryDownload)), "0"},
		{"proceeds", tot.Proceeds.String(), "JPY 204, KWD 1.166, USD 0.70"},
	}
	for _, g := range rep.Groups {
		cases = append(cases, struct{ name, got, want string }{
			"refunds " + g.Key[DimCountry], FormatUnits(g.Refunds.Get(CategoryIAP)),
			map[string]string{"JP": "1", "US": "1", "KW": "0"}[g.Key[DimCountry]],
		})
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %s, want %s", c.name, c.got, c.want)
		}
	}
	if len(rep.Groups) != 3 {
		t.Fatalf("groups = %d", len(rep.Groups))
	}
}
