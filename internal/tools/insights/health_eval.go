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
	// VerdictUnknown means no production source (diagnostics, perf metrics,
	// analytics crashes) has data; TestFlight and ratings alone never make a
	// release healthy.
	VerdictUnknown = "unknown"
)

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
	// Weights maps diagnostic type to {current, previous} summed weight.
	Weights      map[string][2]float64
	Regressions  *int
	CrashReports *int
	RatingNow    *float64
	RatingBefore *float64
}

// EvaluateHealth turns signals into a verdict and the reasons behind it.
// Thresholds left at zero take their defaults.
func EvaluateHealth(s HealthSignals, th HealthThresholds) (string, []string) {
	th = th.withDefaults()
	verdict := VerdictHealthy
	var reasons []string
	raise := func(v, reason string) {
		if v == VerdictBreach || (v == VerdictWatch && verdict == VerdictHealthy) {
			verdict = v
		}
		reasons = append(reasons, v+": "+reason)
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
				raise(VerdictWatch, fmt.Sprintf("%s signatures appeared (weight %.2f, none before)", typ, w[0]))
			}
			continue
		}
		pct := (w[0] - w[1]) / w[1] * 100
		switch {
		case pct >= th.SignatureBreachPct:
			raise(VerdictBreach, fmt.Sprintf("%s weight +%.0f%% (≥ %.0f%%)", typ, pct, th.SignatureBreachPct))
		case pct >= th.SignatureWatchPct:
			raise(VerdictWatch, fmt.Sprintf("%s weight +%.0f%% (≥ %.0f%%)", typ, pct, th.SignatureWatchPct))
		}
	}
	if s.Regressions != nil {
		switch n := *s.Regressions; {
		case n >= th.RegressionsBreach:
			raise(VerdictBreach, fmt.Sprintf("%d performance regressions (≥ %d)", n, th.RegressionsBreach))
		case n > 0:
			raise(VerdictWatch, fmt.Sprintf("%d performance regression", n))
		}
	}
	if s.CrashReports != nil {
		switch n := *s.CrashReports; {
		case n >= th.CrashBreach:
			raise(VerdictBreach, fmt.Sprintf("%d TestFlight crash reports (≥ %d)", n, th.CrashBreach))
		case n >= th.CrashWatch:
			raise(VerdictWatch, fmt.Sprintf("%d TestFlight crash reports (≥ %d)", n, th.CrashWatch))
		}
	}
	if s.RatingNow != nil && s.RatingBefore != nil {
		switch drop := *s.RatingBefore - *s.RatingNow; {
		case drop >= th.RatingDropBreach:
			raise(VerdictBreach, fmt.Sprintf("average rating %.2f → %.2f (drop ≥ %.1f)", *s.RatingBefore, *s.RatingNow, th.RatingDropBreach))
		case drop >= th.RatingDropWatch:
			raise(VerdictWatch, fmt.Sprintf("average rating %.2f → %.2f (drop ≥ %.1f)", *s.RatingBefore, *s.RatingNow, th.RatingDropWatch))
		}
	}
	return verdict, reasons
}
