package reports

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

// Analytics report request access types (spec enum).
const (
	accessOngoing  = "ONGOING"
	accessSnapshot = "ONE_TIME_SNAPSHOT"
)

// analyticsLagNote explains when analytics data shows up.
const analyticsLagNote = "Apple generates the first reports 24–48 h after a request is created, then daily. " +
	"ONGOING requests never backfill; a ONE_TIME_SNAPSHOT request adds available history once."

// analyticsRequests lists the app's analytics report requests.
func analyticsRequests(ctx context.Context, env *tool.Env, appID string) ([]tool.Resource, error) {
	res, err := env.ASC.List(ctx, "/v1/apps/"+appID+"/analyticsReportRequests", tool.ListOptions{
		Fields:   map[string][]string{"analyticsReportRequests": {"accessType", "stoppedDueToInactivity"}},
		MaxItems: 50,
	})
	if err != nil {
		return nil, fmt.Errorf("list analytics report requests: %w", err)
	}
	return res.Data, nil
}

// activeRequest returns the first request of accessType that Apple has not
// stopped for inactivity.
func activeRequest(reqs []tool.Resource, accessType string) (tool.Resource, bool) {
	for _, r := range reqs {
		if r.Str("accessType") != accessType {
			continue
		}
		if stopped, _ := r.Bool("stoppedDueToInactivity"); stopped {
			continue
		}
		return r, true
	}
	return tool.Resource{}, false
}

// analyticsNotSetUp is the typed error for apps without an analytics report
// request; it points at analytics_setup.
func analyticsNotSetUp(env *tool.Env, appID string) error {
	hint := "call analytics_setup once; " + analyticsLagNote
	if !env.Profile.AllowWrites {
		hint = "analytics_setup needs writes: " + tool.EnableWritesHint + "; then call it once; " + analyticsLagNote
	}
	return tool.WithNext(&tool.Error{
		Code:    "analytics_not_set_up",
		Message: "this app has no active analytics report request, so Apple produces no analytics reports for it",
		Hint:    hint,
	}, tool.NextStep{Tool: "analytics_setup", Args: map[string]any{"app": appID}})
}

// pctChange returns the signed percentage change from prev to cur with one
// decimal ("+12.5%"), or "" when prev is zero (no meaningful ratio).
func pctChange(cur, prev *big.Rat) string {
	if prev == nil || prev.Sign() == 0 {
		return ""
	}
	if cur == nil {
		cur = new(big.Rat)
	}
	// Divide by |prev| so the sign follows the direction of change even when
	// the previous period was negative (−5 → +10 is +300%, not −300%).
	d := new(big.Rat).Sub(cur, prev)
	d.Quo(d, new(big.Rat).Abs(prev))
	d.Mul(d, big.NewRat(100, 1))
	s := d.FloatString(1)
	if d.Sign() > 0 {
		s = "+" + s
	}
	return s + "%"
}

// previousWindow returns the comparison window for a period preset: the same
// weekdays a week earlier for week presets, the same days of the previous
// month for month presets, otherwise the equally long window just before.
// Both bounds are inclusive Pacific days.
func previousWindow(preset string, from, to time.Time) (time.Time, time.Time) {
	switch preset {
	case "this_week", "last_week":
		return from.AddDate(0, 0, -7), to.AddDate(0, 0, -7)
	case "month_to_date", "last_month":
		pFrom := from.AddDate(0, -1, 0)
		lastOfPrev := from.AddDate(0, 0, -1)
		pTo := pFrom.AddDate(0, 0, to.Day()-1)
		if preset == "last_month" || pTo.After(lastOfPrev) {
			pTo = lastOfPrev
		}
		return pFrom, pTo
	default:
		n := int(to.Sub(from).Hours()/24+0.5) + 1
		return from.AddDate(0, 0, -n), to.AddDate(0, 0, -n)
	}
}

// dayList returns the inclusive days from..to as YYYY-MM-DD.
func dayList(from, to time.Time) []string {
	var out []string
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format(time.DateOnly))
	}
	return out
}

// appIDArgs is the Next-step argument map for an app.
func appIDArgs(appID string) map[string]any { return map[string]any{"app": appID} }
