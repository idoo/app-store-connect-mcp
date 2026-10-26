package reports

import (
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestFunnelMathsExact(t *testing.T) {
	if got := Rate(big.NewRat(1, 1), big.NewRat(3, 1)); got != "33.33%" {
		t.Errorf("Rate = %q", got)
	}
	if got := Rate(big.NewRat(1, 1), new(big.Rat)); got != "" {
		t.Errorf("Rate zero den = %q", got)
	}
	if got := Change(big.NewRat(51, 1), big.NewRat(30, 1)); got != "+70.0%" {
		t.Errorf("Change = %q", got)
	}
	if got := Ratio(big.NewRat(7, 1), big.NewRat(2, 1)); got != "3.50" {
		t.Errorf("Ratio = %q", got)
	}
}

func testRows() *AnalyticsRows {
	return &AnalyticsRows{
		Report:  ReportDiscovery,
		Columns: []string{"Date", "Event", "Source Type", "Counts"},
		Rows: [][]string{
			{"2026-10-20", "Impression", "Search", "10"},
			{"2026-10-20", "Impression", "Browse", "5"},
			{"2026-10-22", "Page view", "Search", "2"},
			{"2026-10-30", "Impression", "Search", "99"},
		},
	}
}

func TestSumMetricAndFunnel(t *testing.T) {
	a := testRows()
	w := Window{From: "2026-10-19", To: "2026-10-25"}
	imp, err := SumMetric(a, ColCounts, map[string][]string{ColEvent: {"impression"}}, ColSourceType, w)
	if err != nil {
		t.Fatal(err)
	}
	if imp["Search"].RatString() != "10" || imp["Browse"].RatString() != "5" {
		t.Errorf("imp = %v", imp)
	}
	pv, _ := SumMetric(a, ColCounts, map[string][]string{ColEvent: {EventPageView}}, ColSourceType, w)
	total, lines := Funnel(FunnelSums{Impressions: imp, PageViews: pv, Downloads: Sums{}}, nil, true, 1)
	if total.Current.Impressions != "15" || total.Current.PageViewRate != "13.33%" || total.Previous != nil {
		t.Errorf("total = %+v", total)
	}
	if len(lines) != 1 || lines[0].Key != "Search" {
		t.Errorf("lines = %+v", lines)
	}
	if _, err := SumMetric(a, ColCounts, nil, ColTerritory, w); !errors.Is(err, ErrCorrupt) {
		t.Errorf("missing column err = %v", err)
	}
}

func TestCoverageOf(t *testing.T) {
	c := CoverageOf(Window{From: "2026-10-19", To: "2026-10-25"}, time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), testRows())
	if c.DaysExpected != 7 || c.DaysPresent != 2 || c.LatestDay != "2026-10-30" || c.LagDays != 1 || len(c.MissingDays) != 5 ||
		strings.Join(c.MissingDays[:2], ",") != "2026-10-19,2026-10-21" {
		t.Errorf("coverage = %+v", c)
	}
}
