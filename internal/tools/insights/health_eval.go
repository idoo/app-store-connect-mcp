package insights

import (
	"fmt"
	"sort"
)

// Health verdicts, from best to worst.
const (
	VerdictHealthy = "healthy"
	VerdictWatch   = "watch"
	VerdictBreach  = "breach"
	// VerdictUnknown means the live version has no production measurement
	// (diagnostic signatures of its build) or there is no live version.
	// TestFlight, ratings, perf and analytics alone never make a release
	// healthy.
	VerdictUnknown = "unknown"
)

// LevelInfo marks a reason that is reported but never raises the verdict.
const LevelInfo = "info"

// Reason sources.
const (
	SourceDiagnostics = "diagnostics"
	SourcePerf        = "perf_power_metrics"
	SourceAnalytics   = "analytics_crashes"
	SourceTestFlight  = "testflight_crash_feedback"
	SourceRatings     = "ratings"
	SourceVersion     = "version"
)

// MinReviewsPerWindow is the number of reviews each rating window needs
// before an average is compared.
const MinReviewsPerWindow = 20

// Reason explains one contribution to a verdict. Pausing is true only for a
// breach the guard may act on: today only diagnostics of the current build.
type Reason struct {
	Level   string `json:"level"` // info | watch | breach | unknown
	Source  string `json:"source"`
	Message string `json:"message"`
	Pausing bool   `json:"pausing"`
}

func (r Reason) String() string { return r.Level + ": " + r.Message }

// AnyPausing reports whether a breach reason allows pausing a rollout.
func AnyPausing(rs []Reason) bool {
	for _, r := range rs {
		if r.Pausing && r.Level == VerdictBreach {
			return true
		}
	}
	return false
}

// HealthThresholds are the watch/breach limits of a release health
// evaluation. Signature deltas are percent growth of the summed diagnostic
// weight of the live build over the previous build.
type HealthThresholds struct {
	SignatureWatchPct  float64 `json:"signature_watch_pct,omitempty" jsonschema:"weight growth % per diagnostic type for watch; default 25"`
	SignatureBreachPct float64 `json:"signature_breach_pct,omitempty" jsonschema:"weight growth % for breach; default 100"`
	CrashWatch         int     `json:"crash_feedback_watch,omitempty" jsonschema:"TestFlight crash reports for watch; default 3"`
	CrashBreach        int     `json:"crash_feedback_breach,omitempty" jsonschema:"TestFlight crash reports for breach; default 10"`
	RatingDropWatch    float64 `json:"rating_drop_watch,omitempty" jsonschema:"average rating drop for watch; default 0.3"`
	RatingDropBreach   float64 `json:"rating_drop_breach,omitempty" jsonschema:"average rating drop for breach; default 0.8"`
	RegressionsBreach  int     `json:"regressions_breach,omitempty" jsonschema:"perf regressions for breach (1+ is watch); default 2"`
}

// DefaultHealthThresholds returns the defaults used for zero fields.
func DefaultHealthThresholds() HealthThresholds {
	return HealthThresholds{
		SignatureWatchPct: 25, SignatureBreachPct: 100,
		CrashWatch: 3, CrashBreach: 10,
		RatingDropWatch: 0.3, RatingDropBreach: 0.8,
		RegressionsBreach: 2,
	}
}

// withDefaults fills zero fields from [DefaultHealthThresholds].
func (t HealthThresholds) withDefaults() HealthThresholds {
	d := DefaultHealthThresholds()
	if t.SignatureWatchPct <= 0 {
		t.SignatureWatchPct = d.SignatureWatchPct
	}
	if t.SignatureBreachPct <= 0 {
		t.SignatureBreachPct = d.SignatureBreachPct
	}
	if t.CrashWatch <= 0 {
		t.CrashWatch = d.CrashWatch
	}
	if t.CrashBreach <= 0 {
		t.CrashBreach = d.CrashBreach
	}
	if t.RatingDropWatch <= 0 {
		t.RatingDropWatch = d.RatingDropWatch
	}
	if t.RatingDropBreach <= 0 {
		t.RatingDropBreach = d.RatingDropBreach
	}
	if t.RegressionsBreach <= 0 {
		t.RegressionsBreach = d.RegressionsBreach
	}
	return t
}

// HealthSignals are the measured inputs of an evaluation. Nil pointers mean
// "not measured" and never raise the verdict.
type HealthSignals struct {
	// Weights maps diagnostic type to {current, previous} summed weight. The
	// caller sets it only when the current build has signatures.
	Weights map[string][2]float64
	// Regressions are app-level perf regressions; informational only.
	Regressions *int
	// CrashReports are TestFlight crash reports since the window start.
	CrashReports *int
	RatingNow    *float64
	RatingBefore *float64
	// RatingCountNow and RatingCountBefore are the reviews behind each
	// average; both need [MinReviewsPerWindow].
	RatingCountNow, RatingCountBefore int
	// ApproximateWindow makes rating changes informational: the window start
	// is not the release instant.
	ApproximateWindow bool
}

// EvaluateHealth turns signals into a verdict and the reasons behind it.
// Thresholds left at zero take their defaults. Only diagnostics breaches are
// pausing; TestFlight and rating breaches never are, and perf regressions
// are informational.
func EvaluateHealth(s HealthSignals, th HealthThresholds) (string, []Reason) {
	th = th.withDefaults()
	verdict := VerdictHealthy
	var reasons []Reason
	raise := func(level, src, msg string) {
		if level == VerdictBreach || (level == VerdictWatch && verdict == VerdictHealthy) {
			verdict = level
		}
		reasons = append(reasons, Reason{Level: level, Source: src, Message: msg, Pausing: level == VerdictBreach && src == SourceDiagnostics})
	}
	types := make([]string, 0, len(s.Weights))
	for k := range s.Weights {
		types = append(types, k)
	}
	sort.Strings(types)
	for _, typ := range types {
		w := s.Weights[typ]
		if w[1] <= 0 {
			if w[0] > 0 {
				raise(VerdictWatch, SourceDiagnostics, fmt.Sprintf("%s signatures appeared (weight %.2f, none before)", typ, w[0]))
			}
			continue
		}
		pct := (w[0] - w[1]) / w[1] * 100
		switch {
		case pct >= th.SignatureBreachPct:
			raise(VerdictBreach, SourceDiagnostics, fmt.Sprintf("%s weight +%.0f%% (≥ %.0f%%)", typ, pct, th.SignatureBreachPct))
		case pct >= th.SignatureWatchPct:
			raise(VerdictWatch, SourceDiagnostics, fmt.Sprintf("%s weight +%.0f%% (≥ %.0f%%)", typ, pct, th.SignatureWatchPct))
		}
	}
	if s.Regressions != nil && *s.Regressions > 0 {
		reasons = append(reasons, Reason{
			Level: LevelInfo, Source: SourcePerf,
			Message: fmt.Sprintf("%d app-level performance regressions (breach threshold %d; informational, not tied to this build)", *s.Regressions, th.RegressionsBreach),
		})
	}
	if s.CrashReports != nil {
		switch n := *s.CrashReports; {
		case n >= th.CrashBreach:
			raise(VerdictBreach, SourceTestFlight, fmt.Sprintf("%d TestFlight crash reports since the window start (≥ %d)", n, th.CrashBreach))
		case n >= th.CrashWatch:
			raise(VerdictWatch, SourceTestFlight, fmt.Sprintf("%d TestFlight crash reports since the window start (≥ %d)", n, th.CrashWatch))
		}
	}
	if s.RatingNow != nil && s.RatingBefore != nil &&
		s.RatingCountNow >= MinReviewsPerWindow && s.RatingCountBefore >= MinReviewsPerWindow {
		drop := *s.RatingBefore - *s.RatingNow
		level := ""
		switch {
		case drop >= th.RatingDropBreach:
			level = VerdictBreach
		case drop >= th.RatingDropWatch:
			level = VerdictWatch
		}
		if level != "" {
			msg := fmt.Sprintf("average rating %.2f → %.2f (%d vs %d reviews)", *s.RatingBefore, *s.RatingNow, s.RatingCountBefore, s.RatingCountNow)
			if s.ApproximateWindow {
				reasons = append(reasons, Reason{Level: LevelInfo, Source: SourceRatings, Message: msg + "; window start is approximate, informational"})
			} else {
				raise(level, SourceRatings, msg)
			}
		}
	}
	return verdict, reasons
}
