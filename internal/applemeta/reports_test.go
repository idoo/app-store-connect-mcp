package applemeta

import (
	"slices"
	"testing"
	"time"
)

func pt(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, Pacific())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestReportDate(t *testing.T) {
	tests := []struct {
		freq Frequency
		day  string
		want string
	}{
		{Daily, "2026-10-21", "2026-10-21"},
		{Weekly, "2026-10-19", "2026-10-25"}, // Monday → that week's Sunday
		{Weekly, "2026-10-25", "2026-10-25"}, // Sunday is its own week end
		{Weekly, "2026-12-29", "2027-01-03"}, // across a year boundary
		{Monthly, "2026-02-28", "2026-02"},
		{Yearly, "2026-07-01", "2026"},
		{"HOURLY", "2026-07-01", ""},
	}
	for _, tc := range tests {
		if got := ReportDate(tc.freq, pt(t, tc.day+" 00:00")); got != tc.want {
			t.Errorf("ReportDate(%s, %s) = %q; want %q", tc.freq, tc.day, got, tc.want)
		}
	}
}

func TestPacificDay(t *testing.T) {
	tests := []struct {
		utc  string
		want string
	}{
		{"2026-10-25T06:59:00Z", "2026-10-24"}, // 23:59 PDT
		{"2026-10-25T07:00:00Z", "2026-10-25"}, // 00:00 PDT
		{"2026-01-10T07:59:00Z", "2026-01-09"}, // 23:59 PST
		{"2026-01-10T08:00:00Z", "2026-01-10"},
	}
	for _, tc := range tests {
		now, _ := time.Parse(time.RFC3339, tc.utc)
		got := PacificDay(now)
		if got.Format(time.DateOnly) != tc.want || got.Hour() != 0 {
			t.Errorf("PacificDay(%s) = %v; want %s 00:00", tc.utc, got, tc.want)
		}
	}
}

func TestPeriod(t *testing.T) {
	tests := []struct {
		name, preset, now string
		from, to          string
		pending           []string
	}{
		{"yesterday", "yesterday", "2026-10-25T18:00:00Z", "2026-10-24", "2026-10-24", nil},
		{"yesterday UTC already next day", "yesterday", "2026-10-26T03:00:00Z", "2026-10-24", "2026-10-24", nil},
		// Wednesday 2026-11-04, after DST ended on Sunday 2026-11-01.
		{"this_week", "this_week", "2026-11-04T20:00:00Z", "2026-11-02", "2026-11-04", []string{"2026-11-04"}},
		{"last_week across DST end", "last_week", "2026-11-04T20:00:00Z", "2026-10-26", "2026-11-01", nil},
		{"last_7_days across DST start", "last_7_days", "2026-03-10T20:00:00Z", "2026-03-03", "2026-03-09", nil},
		{"this_week on Monday", "this_week", "2026-03-09T20:00:00Z", "2026-03-09", "2026-03-09", []string{"2026-03-09"}},
		{"month_to_date first", "month_to_date", "2026-11-01T20:00:00Z", "2026-11-01", "2026-11-01", []string{"2026-11-01"}},
		{"month_to_date", "month_to_date", "2026-03-10T20:00:00Z", "2026-03-01", "2026-03-10", []string{"2026-03-10"}},
		{"last_month leap", "last_month", "2028-03-15T20:00:00Z", "2028-02-01", "2028-02-29", nil},
		{"last_month january", "last_month", "2027-01-01T20:00:00Z", "2026-12-01", "2026-12-31", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			from, to, pending, err := Period(tc.preset, now)
			if err != nil {
				t.Fatal(err)
			}
			if from.Format(time.DateOnly) != tc.from || to.Format(time.DateOnly) != tc.to {
				t.Errorf("range = %s..%s; want %s..%s", from.Format(time.DateOnly), to.Format(time.DateOnly), tc.from, tc.to)
			}
			if from.Hour() != 0 || to.Hour() != 0 || from.Location() != Pacific() {
				t.Errorf("bounds not Pacific midnight: %v %v", from, to)
			}
			if !slices.Equal(pending, tc.pending) {
				t.Errorf("pending = %v; want %v", pending, tc.pending)
			}
		})
	}
	if _, _, _, err := Period("fortnight", time.Now()); err == nil {
		t.Error("unknown preset accepted")
	}
}

func TestSalesReports(t *testing.T) {
	spec := map[ReportType]bool{
		ReportSales: true, ReportPreOrder: true, ReportNewsstand: true, ReportSubscription: true,
		ReportSubscriptionEvent: true, ReportSubscriber: true, ReportOfferCodeRedemption: true,
		ReportInstalls: true, ReportFirstAnnual: true, ReportWinBackEligibility: true,
	}
	subTypes := []string{"SUMMARY", "DETAILED", "SUMMARY_INSTALL_TYPE", "SUMMARY_TERRITORY", "SUMMARY_CHANNEL"}
	for _, s := range SalesReports() {
		if !spec[s.Type] {
			t.Errorf("unexpected report type %s", s.Type)
		}
		delete(spec, s.Type)
		for _, st := range s.SubTypes {
			if !slices.Contains(subTypes, st) {
				t.Errorf("%s subtype %s not in spec enum", s.Type, st)
			}
		}
	}
	if len(spec) != 0 {
		t.Errorf("report types missing from table: %v", spec)
	}
	tests := []struct {
		typ     ReportType
		sub     string
		freq    Frequency
		version string
		ok      bool
	}{
		{ReportSales, "SUMMARY", Weekly, "1_0", true},
		{ReportSubscription, "SUMMARY", Daily, "1_4", true},
		{ReportSubscriber, "DETAILED", Daily, "1_4", true},
		{ReportSubscriptionEvent, "SUMMARY", Weekly, "1_4", false},
		{ReportSales, "DETAILED", Daily, "1_0", false},
	}
	for _, tc := range tests {
		s, ok := SalesReport(tc.typ)
		if !ok || s.Version != tc.version {
			t.Fatalf("SalesReport(%s) = %+v,%v", tc.typ, s, ok)
		}
		if err := s.Validate(tc.sub, tc.freq); (err == nil) != tc.ok {
			t.Errorf("%s.Validate(%s,%s) = %v; want ok=%v", tc.typ, tc.sub, tc.freq, err, tc.ok)
		}
	}
	if _, ok := SalesReport(ReportFinancial); ok {
		t.Error("FINANCIAL is not a sales report")
	}
	for typ, want := range map[ReportType]string{ReportFinancial: "ZZ", ReportFinanceDetail: "Z1", ReportSales: ""} {
		if got, _ := FinanceRegion(typ); got != want {
			t.Errorf("FinanceRegion(%s) = %q; want %q", typ, got, want)
		}
	}
}

func TestProductCategoryOf(t *testing.T) {
	tests := map[string]ProductCategory{
		"1": CategoryDownload, "1F": CategoryDownload, "1T": CategoryDownload, "F1": CategoryDownload,
		"7": CategoryUpdate, "7F": CategoryUpdate, "7T": CategoryUpdate, "F7": CategoryUpdate,
		"3": CategoryRedownload, "3F": CategoryRedownload,
		"IA1": CategoryIAP, "IA9": CategoryIAP, "IAY": CategoryIAP, "IAC": CategoryIAP, "FI1": CategoryIAP,
		" iay-m ": CategoryIAP, "XYZ": CategoryUnknown, "": CategoryUnknown,
	}
	for id, want := range tests {
		if got := ProductCategoryOf(id); got != want {
			t.Errorf("ProductCategoryOf(%q) = %s; want %s", id, got, want)
		}
	}
}
