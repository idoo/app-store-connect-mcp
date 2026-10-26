package applemeta

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Pacific day math must not depend on the host's zoneinfo.
)

// ReportType is a Sales and Trends (or Finance) report type enum value.
type ReportType string

// Sales and Trends report types (spec `filter[reportType]` enum) and Finance
// report types.
const (
	ReportSales               ReportType = "SALES"
	ReportPreOrder            ReportType = "PRE_ORDER"
	ReportNewsstand           ReportType = "NEWSSTAND"
	ReportSubscription        ReportType = "SUBSCRIPTION"
	ReportSubscriptionEvent   ReportType = "SUBSCRIPTION_EVENT"
	ReportSubscriber          ReportType = "SUBSCRIBER"
	ReportOfferCodeRedemption ReportType = "SUBSCRIPTION_OFFER_CODE_REDEMPTION"
	ReportInstalls            ReportType = "INSTALLS"
	ReportFirstAnnual         ReportType = "FIRST_ANNUAL"
	ReportWinBackEligibility  ReportType = "WIN_BACK_ELIGIBILITY"
	ReportFinancial           ReportType = "FINANCIAL"
	ReportFinanceDetail       ReportType = "FINANCE_DETAIL"
)

// Frequency is a report frequency enum value.
type Frequency string

// Report frequencies.
const (
	Daily   Frequency = "DAILY"
	Weekly  Frequency = "WEEKLY"
	Monthly Frequency = "MONTHLY"
	Yearly  Frequency = "YEARLY"
)

// ReportSpec is the valid parameter combination for one sales report type.
type ReportSpec struct {
	Type        ReportType  `json:"type"`
	Version     string      `json:"version"`
	SubTypes    []string    `json:"subTypes"`
	Frequencies []Frequency `json:"frequencies"`
}

var allFreq = []Frequency{Daily, Weekly, Monthly, Yearly}

// salesReports pins version, subtypes and frequencies per report type. The
// enums come from the spec; the pairing is not in the spec and follows
// Apple's "Download Sales and Trends Reports" documentation. asc-mcp always
// sends an explicit version, because omitting it returns older column sets.
var salesReports = []ReportSpec{
	{ReportSales, "1_0", []string{"SUMMARY"}, allFreq},
	{ReportPreOrder, "1_0", []string{"SUMMARY"}, allFreq},
	{ReportNewsstand, "1_0", []string{"DETAILED"}, []Frequency{Daily, Weekly}},
	{ReportSubscription, "1_4", []string{"SUMMARY"}, []Frequency{Daily}},
	{ReportSubscriptionEvent, "1_4", []string{"SUMMARY"}, []Frequency{Daily}},
	{ReportSubscriber, "1_4", []string{"DETAILED"}, []Frequency{Daily}},
	{ReportOfferCodeRedemption, "1_0", []string{"SUMMARY"}, []Frequency{Daily}},
	{ReportInstalls, "1_0", []string{"SUMMARY_INSTALL_TYPE", "SUMMARY_TERRITORY", "SUMMARY_CHANNEL"}, allFreq},
	{ReportFirstAnnual, "1_0", []string{"SUMMARY"}, []Frequency{Monthly, Yearly}},
	{ReportWinBackEligibility, "1_0", []string{"SUMMARY"}, []Frequency{Daily}},
}

// SalesReports returns every pinned sales report spec.
func SalesReports() []ReportSpec { return slices.Clone(salesReports) }

// SalesReport returns the spec for a sales report type.
func SalesReport(t ReportType) (ReportSpec, bool) {
	i := slices.IndexFunc(salesReports, func(s ReportSpec) bool { return s.Type == t })
	if i < 0 {
		return ReportSpec{}, false
	}
	return salesReports[i], true
}

// Validate checks a subtype and frequency against the spec.
func (s ReportSpec) Validate(subType string, freq Frequency) error {
	if !slices.Contains(s.SubTypes, subType) {
		return fmt.Errorf("report %s does not support subtype %q (valid: %s)", s.Type, subType, strings.Join(s.SubTypes, ", "))
	}
	if !slices.Contains(s.Frequencies, freq) {
		return fmt.Errorf("report %s does not support frequency %q (valid: %v)", s.Type, freq, s.Frequencies)
	}
	return nil
}

// FinanceRegion returns the default regionCode for a finance report type:
// ZZ (all regions) for FINANCIAL and Z1 for FINANCE_DETAIL.
func FinanceRegion(t ReportType) (string, bool) {
	switch t {
	case ReportFinancial:
		return "ZZ", true
	case ReportFinanceDetail:
		return "Z1", true
	default:
		return "", false
	}
}

// ReportDate formats a report date for freq: DAILY "2006-01-02", WEEKLY the
// Sunday ending t's Monday–Sunday week, MONTHLY "2006-01", YEARLY "2006".
// The calendar date of t in its own location is used; pass PacificDay(t).
// An unknown frequency yields "".
func ReportDate(freq Frequency, t time.Time) string {
	switch freq {
	case Daily:
		return t.Format(time.DateOnly)
	case Weekly:
		return t.AddDate(0, 0, (7-int(t.Weekday()))%7).Format(time.DateOnly)
	case Monthly:
		return t.Format("2006-01")
	case Yearly:
		return t.Format("2006")
	default:
		return ""
	}
}

var pacific = sync.OnceValue(func() *time.Location {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		// Only reachable with a broken ZONEINFO override; tzdata is embedded.
		return time.FixedZone("PST", -8*60*60)
	}
	return loc
})

// Pacific returns America/Los_Angeles, the time zone of Apple's report days.
func Pacific() *time.Location { return pacific() }

// PacificDay returns midnight, Pacific time, of the Pacific calendar day
// containing t.
func PacificDay(t time.Time) time.Time {
	p := t.In(Pacific())
	return time.Date(p.Year(), p.Month(), p.Day(), 0, 0, 0, 0, Pacific())
}

// PeriodPresets lists the presets accepted by Period.
var PeriodPresets = []string{"yesterday", "this_week", "last_week", "last_7_days", "month_to_date", "last_month"}

// Period resolves a preset to an inclusive range of Pacific days [from, to]
// (both at Pacific midnight). Weeks run Monday–Sunday, as Apple's weekly
// reports do. pendingDays lists days in the range whose daily report cannot
// exist yet (today or later, Pacific).
func Period(preset string, now time.Time) (from, to time.Time, pendingDays []string, err error) {
	today := PacificDay(now)
	monday := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	switch preset {
	case "yesterday":
		from, to = today.AddDate(0, 0, -1), today.AddDate(0, 0, -1)
	case "this_week":
		from, to = monday, today
	case "last_week":
		from, to = monday.AddDate(0, 0, -7), monday.AddDate(0, 0, -1)
	case "last_7_days":
		from, to = today.AddDate(0, 0, -7), today.AddDate(0, 0, -1)
	case "month_to_date":
		from, to = today.AddDate(0, 0, 1-today.Day()), today
	case "last_month":
		first := today.AddDate(0, 0, 1-today.Day())
		from, to = first.AddDate(0, -1, 0), first.AddDate(0, 0, -1)
	default:
		return time.Time{}, time.Time{}, nil, fmt.Errorf("unknown period %q (valid: %s)", preset, strings.Join(PeriodPresets, ", "))
	}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if !d.Before(today) {
			pendingDays = append(pendingDays, d.Format(time.DateOnly))
		}
	}
	return from, to, pendingDays, nil
}
