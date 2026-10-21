package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/idoo/app-store-connect-mcp/internal/app"
	"github.com/idoo/app-store-connect-mcp/internal/config"
	"github.com/idoo/app-store-connect-mcp/internal/mcpserver"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tools/insights"
)

const guardUsage = `usage: app-store-connect-mcp guard --app APP [--platform IOS] [--version V] [--pause-on-breach] [thresholds]
  evaluates release health and prints JSON; exit 0 healthy, 3 watch, 4 breach, 5 unknown (no diagnostics for the live build or no live version; never pauses), 1 error, 2 usage.
  --pause-on-breach only pauses an ACTIVE phased release on a breach with a pausing reason (diagnostics; never TestFlight or ratings)
  and needs writes enabled (--allow-writes, ASC_MCP_ALLOW_WRITES=1 or profile allow_writes); it never resumes, completes or starts one.
thresholds (0 or unset = default):
  --signature-watch-pct N   crash signature weight growth, percent, for watch (default 25)
  --signature-breach-pct N  crash signature weight growth, percent, for breach (default 100)
  --crash-watch N           TestFlight crash reports for watch (default 3)
  --crash-breach N          TestFlight crash reports for breach (default 10)
  --rating-drop-watch X     average rating drop for watch (default 0.3)
  --rating-drop-breach X    average rating drop for breach (default 0.8)
  --regressions-breach N    perf regressions for breach (default 2)
  --profile NAME, --allow-writes`

// Guard exit codes.
const (
	exitWatch  = 3
	exitBreach = 4
	// exitUnknown means no production data was measured or there is no
	// live version; the guard never pauses on it.
	exitUnknown = 5
)

// guardExit is an exit status other than 0 that is not an error.
type guardExit struct{ code int }

func (e *guardExit) Error() string { return fmt.Sprintf("guard verdict exit %d", e.code) }

// guardEnv builds the tool environment and reports whether writes are
// allowed; tests replace it.
var guardEnv = func(ctx context.Context, profile string, allowWrites bool) (*tool.Env, bool, error) {
	f := config.Flags{Profile: profile}
	if allowWrites {
		f.AllowWrites = &allowWrites
	}
	r, err := config.Resolve(config.Options{Flags: f})
	if err != nil {
		return nil, false, fmt.Errorf("config: %w", err)
	}
	env, err := app.NewEnv(ctx, r, buildVersion())
	if err != nil {
		return nil, false, err
	}
	return env, r.Profile.AllowWrites, nil
}

// guardOut is the guard's JSON output.
type guardOut struct {
	*insights.HealthReport
	Pause *pauseOutcome `json:"pause,omitempty"`
}

// pauseOutcome records what --pause-on-breach did.
type pauseOutcome struct {
	Action string `json:"action"` // paused | skipped | failed
	Reason string `json:"reason,omitempty"`
}

func runGuard(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("guard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "profile to use")
	appRef := fs.String("app", "", "app name, bundle id, SKU or id")
	platform := fs.String("platform", "IOS", "IOS, MAC_OS, TV_OS or VISION_OS")
	version := fs.String("version", "", "version string (default: live)")
	pause := fs.Bool("pause-on-breach", false, "pause an ACTIVE phased release on a pausing breach (needs writes)")
	allowWrites := fs.Bool("allow-writes", false, "enable writes (only used by --pause-on-breach)")
	var th insights.HealthThresholds
	fs.Float64Var(&th.SignatureWatchPct, "signature-watch-pct", 0, "signature weight growth % for watch (default 25)")
	fs.Float64Var(&th.SignatureBreachPct, "signature-breach-pct", 0, "signature weight growth % for breach (default 100)")
	fs.IntVar(&th.CrashWatch, "crash-watch", 0, "TestFlight crash reports for watch (default 3)")
	fs.IntVar(&th.CrashBreach, "crash-breach", 0, "TestFlight crash reports for breach (default 10)")
	fs.Float64Var(&th.RatingDropWatch, "rating-drop-watch", 0, "average rating drop for watch (default 0.3)")
	fs.Float64Var(&th.RatingDropBreach, "rating-drop-breach", 0, "average rating drop for breach (default 0.8)")
	fs.IntVar(&th.RegressionsBreach, "regressions-breach", 0, "perf regressions for breach (default 2)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usageErr(err.Error())
	}
	if *appRef == "" {
		return usageErr("guard: --app is required\n" + guardUsage)
	}
	env, writes, err := guardEnv(ctx, *profile, *allowWrites)
	if err != nil {
		return err
	}
	if env.Plans == nil {
		// release_phased's confirm gate needs a preview store, as a server
		// session has; it lives only for this run.
		env.Plans = mcpserver.NewPlanner(nil)
	}
	in := insights.HealthIn{App: tool.AppRef(*appRef), Platform: tool.Platform(*platform), Version: *version, Thresholds: th}
	rep, err := insights.RunHealth(ctx, env, in)
	if err != nil {
		return err
	}
	out := guardOut{HealthReport: rep}
	if *pause && rep.Verdict == insights.VerdictBreach {
		if insights.AnyPausing(rep.Reasons) {
			out.Pause = pausePhased(ctx, env, writes, in.Platform, rep, stderr)
		} else {
			out.Pause = logPause(stderr, &pauseOutcome{Action: "skipped", Reason: "breach has no pausing reason (TestFlight and ratings never pause)"})
		}
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	switch rep.Verdict {
	case insights.VerdictBreach:
		return &guardExit{exitBreach}
	case insights.VerdictWatch:
		return &guardExit{exitWatch}
	case insights.VerdictUnknown:
		return &guardExit{exitUnknown}
	}
	return nil
}

// guardPauseAction is the only release_phased action the guard ever sends.
const guardPauseAction = "pause"

// pausePhased pauses the report's phased release through release_phased
// (preview, then confirm) when it is ACTIVE and writes are allowed. It is the
// guard's only mutation and its action is fixed to pause.
func pausePhased(ctx context.Context, env *tool.Env, writes bool, platform tool.Platform, rep *insights.HealthReport, log io.Writer) *pauseOutcome {
	switch {
	case rep.Phased == nil || rep.Phased.State != "ACTIVE":
		return logPause(log, &pauseOutcome{Action: "skipped", Reason: "no ACTIVE phased release"})
	case !writes:
		return logPause(log, &pauseOutcome{Action: "skipped", Reason: "writes disabled; set ASC_MCP_ALLOW_WRITES=1, --allow-writes or profile allow_writes"})
	}
	t, ok := tool.Lookup("release_phased")
	if !ok {
		return logPause(log, &pauseOutcome{Action: "failed", Reason: "release_phased is not registered"})
	}
	args := map[string]any{"app": rep.App.ID, "platform": string(platform), "version": rep.Version, "action": guardPauseAction}
	for _, confirm := range []bool{false, true} {
		if confirm {
			args["confirm"] = true
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return logPause(log, &pauseOutcome{Action: "failed", Reason: err.Error()})
		}
		if res := t.Call(ctx, env, raw); res.IsError {
			b, _ := json.Marshal(res.Data)
			return logPause(log, &pauseOutcome{Action: "failed", Reason: string(b)})
		}
	}
	return logPause(log, &pauseOutcome{Action: "paused", Reason: fmt.Sprintf("breach: %d reasons; phased release %s paused on day %d", len(rep.Reasons), rep.Phased.ID, rep.Phased.Day)})
}

func logPause(w io.Writer, p *pauseOutcome) *pauseOutcome {
	fmt.Fprintf(w, "guard: pause %s: %s\n", p.Action, p.Reason)
	return p
}
