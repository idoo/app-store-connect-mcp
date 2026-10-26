package insights

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/reports"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// xcodeMetricsAccept is the media type Apple requires for perfPowerMetrics.
const xcodeMetricsAccept = "application/vnd.apple.xcode-metrics+json"

// maxInlineSignatures is the number of top signatures kept inline per build;
// the full list goes to an artifact link.
const maxInlineSignatures = 5

// HealthIn are the arguments of release_health_report and the guard CLI.
type HealthIn struct {
	App        tool.AppRef      `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform   tool.Platform    `json:"platform,omitempty"`
	Version    string           `json:"version,omitempty" jsonschema:"version string; default live"`
	Thresholds HealthThresholds `json:"thresholds,omitzero"`
}

// signatureView is one diagnostic signature.
type signatureView struct {
	Type      string  `json:"type"`
	Signature string  `json:"signature"`
	Weight    float64 `json:"weight"`
}

// buildDiagnostics summarises one build's diagnostic signatures.
type buildDiagnostics struct {
	Build   string             `json:"build_id,omitempty"`
	Weights map[string]float64 `json:"weights"`
	Top     []signatureView    `json:"top,omitempty"`
	Count   int                `json:"signatures"`
}

// section is the status of one optional data source.
type section struct {
	Status  string `json:"status"` // ok | not_set_up | unavailable | insufficient_data | error
	Message string `json:"message,omitempty"`
}

// HealthReport is the output of release_health_report.
type HealthReport struct {
	App         tool.App     `json:"app"`
	Version     string       `json:"version"`
	VersionID   string       `json:"version_id"`
	Previous    string       `json:"previous_version,omitempty"`
	Verdict     string       `json:"verdict"`
	Reasons     []string     `json:"reasons,omitempty"`
	Phased      *PhasedState `json:"phased_release,omitempty"`
	Diagnostics struct {
		Current  *buildDiagnostics   `json:"current,omitempty"`
		Previous *buildDiagnostics   `json:"previous,omitempty"`
		DeltaPct map[string]*float64 `json:"weight_delta_pct,omitempty"`
	} `json:"diagnostics"`
	Perf struct {
		section
		Regressions []map[string]any `json:"regressions,omitempty"`
	} `json:"perf_power_metrics"`
	CrashFeedback struct {
		section
		Count int `json:"count"`
	} `json:"testflight_crash_feedback"`
	Ratings struct {
		Since         string   `json:"since,omitempty"`
		ReviewsSince  int      `json:"reviews_since"`
		AverageSince  *float64 `json:"average_since,omitempty"`
		AverageBefore *float64 `json:"average_before,omitempty"`
	} `json:"ratings"`
	Analytics struct {
		section
		Crashes *float64 `json:"crashes,omitempty"`
	} `json:"analytics_crashes"`
	// Coverage maps each source to measured, no_data or not_set_up.
	Coverage   map[string]string `json:"coverage"`
	Thresholds HealthThresholds  `json:"thresholds"`
}

// PhasedState is the live version's phased release.
type PhasedState struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Day   int    `json:"current_day"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "release_health_report", Toolset: tool.Insights, Class: tool.Read, Idempotent: true,
		Title: "Release health vs previous version",
		Description: "Live (or given) version vs the previous one: hang/launch/disk-write signature deltas, perf regressions, " +
			"TestFlight crashes, rating delta, analytics crashes. Verdict healthy|watch|breach|unknown (no production data); breach with an active phased release suggests pause.",
		Enums: map[string][]any{"platform": {"IOS", "MAC_OS", "TV_OS", "VISION_OS"}},
	}, healthReport))
}

func healthReport(ctx context.Context, env *tool.Env, in HealthIn) (*tool.Result, error) {
	rep, err := RunHealth(ctx, env, in)
	if err != nil {
		return nil, err
	}
	res := tool.OK(rep)
	if rep.Analytics.Status == "not_set_up" {
		res.Then(tool.NextStep{Tool: "analytics_setup", Args: map[string]any{"app": rep.App.ID}})
	}
	if rep.Verdict == VerdictBreach && rep.Phased != nil && rep.Phased.State == "ACTIVE" {
		res.Then(tool.NextStep{Tool: "release_phased", Args: map[string]any{
			"app": rep.App.ID, "platform": string(platformOr(in.Platform)), "version": rep.Version, "action": "pause",
		}})
	}
	if err := linkSignatures(ctx, env, res, rep); err != nil {
		res.Warn("full signature list not stored: " + err.Error())
	}
	return res, nil
}

func platformOr(p tool.Platform) tool.Platform {
	if p == "" {
		return tool.IOS
	}
	return p
}

// RunHealth gathers every signal for the live (or given) version and the one
// before it, then evaluates the thresholds. It never mutates anything.
func RunHealth(ctx context.Context, env *tool.Env, in HealthIn) (*HealthReport, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := platformOr(in.Platform)
	cur, prev, inc, err := healthVersions(ctx, env, app.ID, platform, in.Version)
	if err != nil {
		return nil, err
	}
	rep := &HealthReport{App: app, Version: cur.Str("versionString"), VersionID: cur.ID, Thresholds: in.Thresholds.withDefaults()}
	if id := cur.RelID("appStoreVersionPhasedRelease"); id != "" {
		for _, r := range inc {
			if r.Type == "appStoreVersionPhasedReleases" && r.ID == id {
				day, _ := r.Int("currentDayNumber")
				rep.Phased = &PhasedState{ID: r.ID, State: r.Str("phasedReleaseState"), Day: day}
			}
		}
	}
	sig := HealthSignals{Weights: map[string][2]float64{}}
	since := cur.Str("createdDate")
	if prev != nil {
		rep.Previous = prev.Str("versionString")
	}

	// Diagnostic signatures per build.
	curD, err := diagnostics(ctx, env, cur.RelID("build"))
	if err != nil {
		return nil, err
	}
	rep.Diagnostics.Current = curD
	if prev != nil {
		prevD, err := diagnostics(ctx, env, prev.RelID("build"))
		if err != nil {
			return nil, err
		}
		rep.Diagnostics.Previous = prevD
		rep.Diagnostics.DeltaPct = map[string]*float64{}
		for _, typ := range []string{"HANGS", "LAUNCHES", "DISK_WRITES"} {
			c, p := curD.Weights[typ], prevD.Weights[typ]
			sig.Weights[typ] = [2]float64{c, p}
			if p > 0 {
				d := (c - p) / p * 100
				rep.Diagnostics.DeltaPct[typ] = &d
			} else {
				rep.Diagnostics.DeltaPct[typ] = nil
			}
		}
	}

	// App-level perfPowerMetrics regressions.
	perf(ctx, env, app.ID, rep, &sig)

	// TestFlight crash feedback for the live build.
	if b := cur.RelID("build"); b != "" {
		q := url.Values{"filter[build]": {b}}
		res, err := env.ASC.List(ctx, "/v1/apps/"+app.ID+"/betaFeedbackCrashSubmissions", tool.ListOptions{Query: q, MaxItems: 200})
		if err != nil {
			rep.CrashFeedback.section = section{Status: "error", Message: err.Error()}
		} else {
			n := max(res.Total, len(res.Data))
			rep.CrashFeedback.section, rep.CrashFeedback.Count = section{Status: "ok"}, n
			sig.CrashReports = &n
		}
	} else {
		rep.CrashFeedback.section = section{Status: "unavailable", Message: "no build attached"}
	}

	// Ratings since the live version vs the window before it.
	if err := ratings(ctx, env, app.ID, since, prev, rep, &sig); err != nil {
		return nil, err
	}

	// Production crashes from analytics.
	analytics(ctx, env, app.ID, since, rep)

	rep.Verdict, rep.Reasons = EvaluateHealth(sig, rep.Thresholds)
	rep.Coverage = coverageOf(rep)
	if !productionMeasured(rep.Coverage) {
		rep.Verdict = VerdictUnknown
		rep.Reasons = append([]string{"no production data yet"}, rep.Reasons...)
	}
	return rep, nil
}

// Coverage states.
const (
	covMeasured = "measured"
	covNoData   = "no_data"
	covNotSetUp = "not_set_up"
)

// productionSources are the sources that can make a release healthy.
var productionSources = []string{"diagnostics", "perf_power_metrics", "analytics_crashes"}

// coverageOf reports which sources produced data.
func coverageOf(rep *HealthReport) map[string]string {
	state := func(ok bool) string {
		if ok {
			return covMeasured
		}
		return covNoData
	}
	d := rep.Diagnostics
	c := map[string]string{
		"diagnostics":               state((d.Current != nil && d.Current.Count > 0) || (d.Previous != nil && d.Previous.Count > 0)),
		"perf_power_metrics":        state(rep.Perf.Status == "ok"),
		"analytics_crashes":         state(rep.Analytics.Status == "ok"),
		"testflight_crash_feedback": state(rep.CrashFeedback.Status == "ok"),
		"ratings":                   state(rep.Ratings.AverageSince != nil || rep.Ratings.AverageBefore != nil),
	}
	if rep.Analytics.Status == "not_set_up" {
		c["analytics_crashes"] = covNotSetUp
	}
	return c
}

// productionMeasured reports whether any production source has data.
func productionMeasured(c map[string]string) bool {
	for _, s := range productionSources {
		if c[s] == covMeasured {
			return true
		}
	}
	return false
}

// healthVersions returns the chosen version, the one created before it on the
// same platform (if any) and the included resources.
func healthVersions(ctx context.Context, env *tool.Env, appID string, platform tool.Platform, version string) (tool.Resource, *tool.Resource, []tool.Resource, error) {
	q := url.Values{
		"filter[platform]":                      {string(platform)},
		"include":                               {"appStoreVersionPhasedRelease,build"},
		"fields[appStoreVersions]":              {"versionString,appVersionState,createdDate,appStoreVersionPhasedRelease,build"},
		"fields[appStoreVersionPhasedReleases]": {"phasedReleaseState,currentDayNumber"},
		"limit":                                 {"10"},
	}
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+appID+"/appStoreVersions", q)
	if err != nil {
		return tool.Resource{}, nil, nil, fmt.Errorf("list versions: %w", err)
	}
	vs := slices.Clone(resp.Data)
	slices.SortStableFunc(vs, func(a, b tool.Resource) int { return cmp.Compare(b.Str("createdDate"), a.Str("createdDate")) })
	idx := -1
	for i, v := range vs {
		if (version != "" && v.Str("versionString") == version) || (version == "" && v.Str("appVersionState") == "READY_FOR_DISTRIBUTION") {
			idx = i
			break
		}
	}
	if idx < 0 {
		what := "a live version"
		if version != "" {
			what = "version " + version
		}
		return tool.Resource{}, nil, nil, fmt.Errorf("%s not found for %s: %w", what, platform, tool.ErrInvalidArgs)
	}
	var prev *tool.Resource
	if idx+1 < len(vs) {
		p := vs[idx+1]
		prev = &p
	}
	return vs[idx], prev, resp.Included, nil
}

// diagnostics sums a build's diagnostic signature weights per type.
func diagnostics(ctx context.Context, env *tool.Env, buildID string) (*buildDiagnostics, error) {
	d := &buildDiagnostics{Build: buildID, Weights: map[string]float64{}}
	if buildID == "" {
		return d, nil
	}
	res, err := env.ASC.List(ctx, "/v1/builds/"+buildID+"/diagnosticSignatures", tool.ListOptions{MaxItems: 1000})
	if err != nil {
		return nil, fmt.Errorf("diagnostic signatures of build %s: %w", buildID, err)
	}
	all := make([]signatureView, 0, len(res.Data))
	for _, r := range res.Data {
		w := num(r.Attributes["weight"])
		s := signatureView{Type: r.Str("diagnosticType"), Signature: r.Str("signature"), Weight: w}
		d.Weights[s.Type] += w
		all = append(all, s)
	}
	slices.SortStableFunc(all, func(a, b signatureView) int { return cmp.Compare(b.Weight, a.Weight) })
	d.Count = len(all)
	d.Top = all
	return d, nil
}

// linkSignatures moves signature lists longer than the inline cap into one
// artifact and links it.
func linkSignatures(ctx context.Context, env *tool.Env, res *tool.Result, rep *HealthReport) error {
	builds := []*buildDiagnostics{rep.Diagnostics.Current, rep.Diagnostics.Previous}
	full := map[string][]signatureView{}
	for _, b := range builds {
		if b != nil && len(b.Top) > maxInlineSignatures {
			full[b.Build] = b.Top
			b.Top = b.Top[:maxInlineSignatures]
		}
	}
	if len(full) == 0 {
		return nil
	}
	body, err := json.Marshal(full)
	if err != nil {
		return err
	}
	l, err := env.Artifacts.Put(ctx, "diagnostic-signatures-"+rep.VersionID+".json", "application/json", body)
	if err != nil {
		return err
	}
	res.WithLink(l)
	return nil
}

// perf reads app-level perfPowerMetrics insights.
func perf(ctx context.Context, env *tool.Env, appID string, rep *HealthReport, sig *HealthSignals) {
	b, _, err := env.ASC.Download(ctx, tool.Request{Method: http.MethodGet, Path: "/v1/apps/" + appID + "/perfPowerMetrics", Accept: xcodeMetricsAccept})
	if err != nil {
		rep.Perf.section = section{Status: "error", Message: err.Error()}
		return
	}
	var doc struct {
		ProductData []json.RawMessage `json:"productData"`
		Insights    struct {
			Regressions []map[string]any `json:"regressions"`
		} `json:"insights"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		rep.Perf.section = section{Status: "error", Message: "undecodable metrics: " + err.Error()}
		return
	}
	if len(doc.ProductData) == 0 {
		rep.Perf.section = section{Status: "insufficient_data", Message: "Apple has no metrics yet (needs enough opted-in devices)."}
		return
	}
	n := len(doc.Insights.Regressions)
	rep.Perf.section, rep.Perf.Regressions = section{Status: "ok"}, doc.Insights.Regressions
	sig.Regressions = &n
}

// ratings averages reviews created since the live version against those
// created between the previous version and it.
func ratings(ctx context.Context, env *tool.Env, appID, since string, prev *tool.Resource, rep *HealthReport, sig *HealthSignals) error {
	rep.Ratings.Since = since
	q := url.Values{"sort": {"-createdDate"}}
	res, err := env.ASC.List(ctx, "/v1/apps/"+appID+"/customerReviews", tool.ListOptions{
		Query: q, Fields: map[string][]string{"customerReviews": {"rating", "createdDate"}}, MaxItems: 500,
	})
	if err != nil {
		return fmt.Errorf("customer reviews: %w", err)
	}
	from := ""
	if prev != nil {
		from = prev.Str("createdDate")
	}
	var sNow, sBefore float64
	var nNow, nBefore int
	for _, r := range res.Data {
		c := r.Str("createdDate")
		rating, _ := r.Int("rating")
		switch {
		case c >= since:
			sNow += float64(rating)
			nNow++
		case from != "" && c >= from:
			sBefore += float64(rating)
			nBefore++
		}
	}
	rep.Ratings.ReviewsSince = nNow
	if nNow > 0 {
		a := sNow / float64(nNow)
		rep.Ratings.AverageSince, sig.RatingNow = &a, &a
	}
	if nBefore > 0 {
		a := sBefore / float64(nBefore)
		rep.Ratings.AverageBefore, sig.RatingBefore = &a, &a
	}
	return nil
}

// analytics totals production crashes since the live version via the shared
// analytics loader; a missing setup is reported, never created.
func analytics(ctx context.Context, env *tool.Env, appID, since string, rep *HealthReport) {
	to := env.Clock.Now()
	from, err := time.Parse(time.RFC3339, since)
	if err != nil || from.After(to) {
		from = to.AddDate(0, 0, -7)
	}
	rows, err := reports.LoadAnalytics(ctx, env, appID, reports.ReportCrashes, "DAILY", from, to, 14)
	var unavailable *reports.ReportUnavailableError
	switch {
	case errors.Is(err, reports.ErrAnalyticsNotSetUp):
		rep.Analytics.section = section{Status: "not_set_up", Message: "analytics reports are not requested; run analytics_setup (data arrives 24–48 h later)"}
		return
	case errors.As(err, &unavailable):
		rep.Analytics.section = section{Status: "unavailable", Message: unavailable.Error()}
		return
	case err != nil:
		rep.Analytics.section = section{Status: "error", Message: err.Error()}
		return
	}
	col := rows.Col(reports.ColCrashes)
	if col < 0 {
		rep.Analytics.section = section{Status: "unavailable", Message: "crash report has no Crashes column"}
		return
	}
	var total float64
	for _, r := range rows.Rows {
		if col < len(r) {
			v, _ := strconv.ParseFloat(r[col], 64)
			total += v
		}
	}
	rep.Analytics.section, rep.Analytics.Crashes = section{Status: "ok"}, &total
}

// num converts a JSON number attribute to float64.
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	case int:
		return float64(x)
	}
	return 0
}
