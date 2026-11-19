package xcodecloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "xcc_usage_report", Toolset: tool.XcodeCloud, Class: tool.Read, Idempotent: true,
		Title: "Xcode Cloud compute usage",
		Description: "The team's Xcode Cloud compute minutes by product and day. With a web session: Apple's numbers and the plan " +
			"(included, used, left, reset date). Without: an estimate summed from the runs' actions, and the runs that ended " +
			"without starting, the sign of used-up hours.",
		Enums:  map[string][]any{"source": {usageAuto, usageApple, usageRuns}},
		Ranges: map[string][2]int{"days": {1, maxUsageDays}},
	}, usage))
}

// Sources of the numbers.
const (
	// usageAuto is Apple's numbers when a web session is active, else the
	// estimate.
	usageAuto = "auto"
	// usageApple is the usage page of App Store Connect (web session).
	usageApple = "apple"
	// usageRuns is the estimate from the runs' actions (public API).
	usageRuns = "runs"
)

// maxUsageDays is the longest window: the public API keeps runs for about a
// month, and the usage page shows a month at a time.
const maxUsageDays = 31

// maxDeadRuns caps the listed runs that ended without starting.
const maxDeadRuns = 10

type usageIn struct {
	App     tool.AppRef `json:"app,omitempty" jsonschema:"only this app's Xcode Cloud product; default: every product of the team (the pinned app is not used)"`
	Product string      `json:"product,omitempty" jsonschema:"Xcode Cloud product id, instead of app"`
	Days    int         `json:"days,omitempty" jsonschema:"days of history up to today; default: since the plan's last reset (apple) or 31 (runs)"`
	Source  string      `json:"source,omitempty" jsonschema:"apple: the usage page's numbers (web session); runs: an estimate from the runs' actions (public API, one call per run); auto (default): apple when a web session is active, else runs"`
}

// usagePlan is the team's Xcode Cloud plan as the usage page shows it.
type usagePlan struct {
	Name             string `json:"name,omitempty"`
	TotalMinutes     int    `json:"total_minutes"`
	UsedMinutes      int    `json:"used_minutes"`
	AvailableMinutes int    `json:"available_minutes"`
	// ResetDate is when the used minutes return to zero (RFC 3339, or
	// Apple's text when it is not a timestamp).
	ResetDate string `json:"reset_date,omitempty"`
	// Exhausted: nothing is left, so Xcode Cloud starts no run until
	// ResetDate.
	Exhausted bool `json:"exhausted"`
}

// usageProduct is one product's share of the window.
type usageProduct struct {
	ID      string         `json:"id"`
	Name    tool.Untrusted `json:"name,omitempty"`
	Minutes int            `json:"minutes"`
	Builds  int            `json:"builds"`
	// Previous* are the same-length window before this one (source apple).
	PreviousMinutes int `json:"previous_minutes,omitempty"`
	PreviousBuilds  int `json:"previous_builds,omitempty"`
}

// usageWorkflow is one workflow's share of a product's window (source apple).
type usageWorkflow struct {
	ID              string         `json:"id"`
	Name            tool.Untrusted `json:"name,omitempty"`
	Minutes         int            `json:"minutes"`
	Builds          int            `json:"builds"`
	PreviousMinutes int            `json:"previous_minutes,omitempty"`
	PreviousBuilds  int            `json:"previous_builds,omitempty"`
}

// usageDay is one day (UTC, YYYY-MM-DD) with compute time.
type usageDay struct {
	Date    string `json:"date"`
	Minutes int    `json:"minutes"`
	Builds  int    `json:"builds"`
}

// deadRun is a run that completed FAILED or ERRORED without ever starting:
// no startedDate and no actions. Xcode Cloud did not take it.
type deadRun struct {
	Product          productRef `json:"product"`
	ID               string     `json:"id"`
	Number           int        `json:"number,omitempty"`
	CreatedDate      string     `json:"created_date,omitempty"`
	CompletionStatus string     `json:"completion_status,omitempty"`
}

type usageOut struct {
	// Source is usageApple or usageRuns: where the numbers come from.
	Source string `json:"source"`
	// Plan is the team's plan (source apple only; always the whole team's).
	Plan *usagePlan `json:"plan,omitempty"`
	// From and To are the first and last day covered (UTC, YYYY-MM-DD).
	From string `json:"from"`
	To   string `json:"to"`
	// Minutes and Builds are the compute minutes and the builds (runs that
	// started) in the window: the team's, or one product's when asked.
	Minutes int `json:"minutes"`
	Builds  int `json:"builds"`
	// Products is the window by product, most minutes first.
	Products []usageProduct `json:"products,omitempty"`
	// Workflows is one product's window by workflow, most minutes first
	// (app or product given; source apple only).
	Workflows []usageWorkflow `json:"workflows,omitempty"`
	// Days are the days with compute time, oldest first.
	Days []usageDay `json:"days,omitempty"`

	// The rest is source runs only.

	// DeadRuns are the newest runs that ended without starting, newest
	// first, at most maxDeadRuns; DeadRunsTotal counts them all.
	DeadRuns      []deadRun `json:"dead_runs,omitempty"`
	DeadRunsTotal int       `json:"dead_runs_total,omitempty"`
	// NotTakingRuns: the newest run of the window is a dead one, and
	// DeadRunsSinceStart counts the dead runs created after the newest run
	// that started (all of them when none started).
	NotTakingRuns      bool `json:"not_taking_runs,omitempty"`
	DeadRunsSinceStart int  `json:"dead_runs_since_last_start,omitempty"`
	// LastStartedAt is when the newest run that did start began.
	LastStartedAt string `json:"last_started_at,omitempty"`
	// RunsScanned is how many runs' actions are in the numbers; Truncated
	// says older runs of the window were left out (maxUsageRuns).
	RunsScanned int  `json:"runs_scanned,omitempty"`
	Truncated   bool `json:"truncated,omitempty"`
	// RunsPending is how many started runs' actions are still unread: one
	// call reads at most usageReadsPerCall of them and keeps what it read,
	// so calling again goes on. The numbers leave these runs out.
	RunsPending int `json:"runs_pending,omitempty"`
}

func usage(ctx context.Context, env *tool.Env, in usageIn) (*tool.Result, error) {
	in.Product = strings.TrimSpace(in.Product)
	if in.Product != "" && in.App != "" {
		return nil, tool.InvalidArgs("pass app or product, not both")
	}
	if in.Days < 0 || in.Days > maxUsageDays {
		return nil, tool.InvalidArgs("days must be between 1 and %d", maxUsageDays)
	}
	if in.Source == "" {
		in.Source = usageAuto
	}
	// The plan is the team's, so no app means every product; the pinned app
	// narrows nothing here.
	var prod *productRef
	if in.Product != "" || in.App != "" {
		p, _, err := resolveProduct(ctx, env, in.App, in.Product, "xcc_usage_report")
		if err != nil {
			return nil, err
		}
		prod = &p
	}
	now := env.Clock.Now().UTC()
	if in.Source != usageRuns {
		out, err := usageFromApple(ctx, env, in.Days, now, prod)
		if err == nil {
			return appleUsageResult(out), nil
		}
		if in.Source == usageApple || !errors.Is(err, tool.ErrWebSessionRequired) {
			return nil, err
		}
	}
	out, err := usageFromRuns(ctx, env, in.Days, now, prod)
	if err != nil {
		return nil, err
	}
	res := runsUsageResult(out)
	if in.Source == usageAuto {
		res.Then(tool.NextStep{Tool: "web_login"})
	}
	return res, nil
}

// appleUsageResult warns when the plan has nothing left.
func appleUsageResult(out usageOut) *tool.Result {
	res := tool.OK(out)
	if p := out.Plan; p != nil && p.Exhausted {
		msg := fmt.Sprintf("the plan's %d compute minutes are used up: Xcode Cloud starts no run", p.TotalMinutes)
		if p.ResetDate != "" {
			msg += " until " + p.ResetDate
		}
		res.Warn(msg)
	}
	return res
}

// runsUsageResult says what an estimate cannot know, names the dead runs'
// usual cause, and asks for another call while runs are still unread.
func runsUsageResult(out usageOut) *tool.Result {
	res := tool.OK(out)
	if out.RunsPending > 0 {
		res = tool.StillProcessing(out, fmt.Sprintf(
			"call xcc_usage_report again with the same arguments: %d more run(s) are still to be read (what was read is kept)", out.RunsPending))
	}
	res.Warn("an estimate: the sum of the runs' action durations; Apple's own count, the plan's limit and its reset date need a web session (web_login, then source apple)")
	if out.Truncated {
		res.Warn("older runs of the window were left out; pass fewer days or one product for the rest")
	}
	if out.NotTakingRuns {
		since := ""
		if out.LastStartedAt != "" {
			since = " since " + out.LastStartedAt
		}
		res.Warn(fmt.Sprintf("%d run(s) ended without starting and nothing has started%s: Xcode Cloud is not taking runs; the usual cause is used-up compute hours", out.DeadRunsSinceStart, since))
	}
	return res
}

// usageWindow returns the first and last day of a window of days ending
// today (UTC).
func usageWindow(now time.Time, days int) (from, to time.Time) {
	to = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return to.AddDate(0, 0, -(days - 1)), to
}

// usageDate formats a day as the usage window does.
func usageDate(t time.Time) string { return t.UTC().Format("2006-01-02") }
