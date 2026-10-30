package insights

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/reports"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

type funnelIn struct {
	App      tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Question string      `json:"question,omitempty" jsonschema:"default funnel"`
	Period   string      `json:"period,omitempty" jsonschema:"default last_28_days; ignored with from/to"`
	From     string      `json:"from,omitempty" jsonschema:"YYYY-MM-DD"`
	To       string      `json:"to,omitempty" jsonschema:"YYYY-MM-DD"`
	Compare  string      `json:"compare,omitempty" jsonschema:"default previous_period"`
	By       string      `json:"by,omitempty" jsonschema:"breakdown; sources/territories imply one"`
	Top      int         `json:"top,omitempty" jsonschema:"default 10, max 50"`
}

var funnelPeriods = map[string]int{"last_7_days": 7, "last_28_days": 28, "last_90_days": 90}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "analytics_funnel_report", Toolset: tool.Insights, Class: tool.Read, Idempotent: true,
		Title: "App Store funnel and usage",
		Description: "Impressions → page views → downloads with conversion by source/territory, sessions, crashes, " +
			"installs vs deletions; vs previous period with coverage and lag. Never creates requests: not_set_up points at analytics_setup.",
		UI: "funnel",
		Enums: map[string][]any{
			"question": {"funnel", "sources", "territories", "sessions", "crashes", "installs_deletions"},
			"period":   {"last_7_days", "last_28_days", "last_90_days"},
			"compare":  {"previous_period", "none"},
			"by":       {"none", "source", "territory", "device"},
		},
	}, funnelReport))
}

// numbers is a set of headline totals with their previous-period values.
type numbers struct {
	Current  map[string]string `json:"current"`
	Previous map[string]string `json:"previous,omitempty"`
	Change   map[string]string `json:"change,omitempty"`
}

// proxy is a labelled retention stand-in.
type proxy struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
	Note  string `json:"note"`
}

type funnelOut struct {
	App          tool.App             `json:"app"`
	Status       string               `json:"status"` // ok | not_set_up | unavailable | no_data
	DataSource   string               `json:"data_source,omitempty"`
	Question     string               `json:"question"`
	By           string               `json:"by,omitempty"`
	Current      reports.Window       `json:"current"`
	Previous     *reports.Window      `json:"previous,omitempty"`
	Message      string               `json:"message,omitempty"`
	Missing      string               `json:"missing_report,omitempty"`
	Available    []string             `json:"available_reports,omitempty"`
	Funnel       *reports.FunnelLine  `json:"funnel,omitempty"`
	Table        []reports.FunnelLine `json:"table,omitempty"`
	Usage        *numbers             `json:"usage,omitempty"`
	Retention    []proxy              `json:"retention_proxies,omitempty"`
	RetentionURL string               `json:"retention_url,omitempty"`
	Coverage     *reports.Coverage    `json:"coverage,omitempty"`
	Segments     int                  `json:"segments"`
	Partial      bool                 `json:"partial,omitempty"`
	MissingDates []string             `json:"unfetched_dates,omitempty"`
	Explanations []string             `json:"explanations,omitempty"`
}

const (
	lagNote     = "Apple publishes analytics 24–48 h after a day ends; recent days may be missing."
	privacyNote = "Apple drops rows below its privacy thresholds, so small apps and fine breakdowns show gaps or undercounts."
	setupNote   = "Apple only starts producing these reports 24–48 h after a request is created; history needs ONE_TIME_SNAPSHOT, which can take days."
)

func funnelReport(ctx context.Context, env *tool.Env, in funnelIn) (*tool.Result, error) {
	if err := checkFunnelArgs(&in); err != nil {
		return nil, err
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	cur, prev, err := funnelWindows(in, env.Clock.Now())
	if err != nil {
		return nil, err
	}
	out := funnelOut{App: app, Status: "ok", DataSource: "analytics_reports", Question: in.Question, By: in.By, Current: cur, Previous: prev}
	res := tool.OK(&out)

	span := cur
	if prev != nil {
		span.From = prev.From
	}
	from, _ := time.Parse(time.DateOnly, span.From)
	to, _ := time.Parse(time.DateOnly, span.To)
	loaded := map[string]*reports.AnalyticsRows{}
	budget := reports.AnalyticsMaxSegments
	for _, name := range reportsFor(in.Question) {
		rows, err := reports.LoadAnalytics(ctx, env, app.ID, name, "DAILY", from, to, budget)
		var unavailable *reports.ReportUnavailableError
		var api *tool.APIError
		switch {
		case errors.Is(err, reports.ErrAnalyticsNotSetUp):
			dres, derr := funnelFromDashboard(ctx, env, app, in, "not set up for this app")
			if derr == nil {
				return dres, nil
			}
			out = funnelOut{App: app, Status: "not_set_up", Question: in.Question, Current: cur, Message: setupNote + " " + webLoginAlt}
			res = tool.OK(&out)
			if !errors.Is(derr, tool.ErrWebSessionRequired) {
				res.Warn("App Analytics dashboard: " + tool.BodyFor(derr).Message)
			}
			return res.Then(
				tool.NextStep{Tool: "web_login"},
				tool.NextStep{Tool: "analytics_setup", Args: map[string]any{"app": app.ID, "one_time_snapshot": true}},
			), nil
		case errors.As(err, &api) && api.Status == http.StatusForbidden:
			dres, derr := funnelFromDashboard(ctx, env, app, in, "forbidden for this API key")
			if derr == nil {
				return dres, nil
			}
			if !errors.Is(derr, tool.ErrWebSessionRequired) {
				return nil, fmt.Errorf("%w; the App Analytics dashboard fallback failed too: %s", err, tool.BodyFor(derr).Message)
			}
			return nil, tool.WithNext(fmt.Errorf("%w; %s", err, webLoginAlt), tool.NextStep{Tool: "web_login"})
		case errors.As(err, &unavailable):
			out.Status, out.Missing, out.Available = "unavailable", unavailable.Name, unavailable.Available
			out.Message = "Apple lists no report with this name for the app; it may not be generated yet or the name changed."
			return res, nil
		case err != nil:
			return nil, err
		}
		loaded[name] = rows
		budget -= rows.Segments
		out.Segments += rows.Segments
		out.Partial = out.Partial || rows.Partial
		out.MissingDates = append(out.MissingDates, rows.Missing...)
	}
	if out.Partial {
		res.Warn(fmt.Sprintf("segment cap %d reached; narrow the period for full data", reports.AnalyticsMaxSegments))
	}

	if err := fillAnswer(&out, in, loaded); err != nil {
		return nil, err
	}
	sets := make([]*reports.AnalyticsRows, 0, len(loaded))
	for _, r := range loaded {
		sets = append(sets, r)
	}
	cov := reports.CoverageOf(cur, applemeta.PacificDay(env.Clock.Now()), sets...)
	out.Coverage = &cov
	out.Explanations = append([]string{lagNote, privacyNote}, out.Explanations...)
	if cov.DaysPresent < cov.DaysExpected {
		out.Explanations = append(out.Explanations, fmt.Sprintf("%d of %d days have data; totals cover only those days.", cov.DaysPresent, cov.DaysExpected))
	}
	attachRows(ctx, env, res, app.ID, loaded)
	return res, nil
}

func checkFunnelArgs(in *funnelIn) error {
	if in.Question == "" {
		in.Question = "funnel"
	}
	if in.Period == "" {
		in.Period = "last_28_days"
	}
	if _, ok := funnelPeriods[in.Period]; !ok {
		return fmt.Errorf("%w: unknown period %q", tool.ErrInvalidArgs, in.Period)
	}
	if in.Compare == "" {
		in.Compare = "previous_period"
	}
	switch in.Question {
	case "sources":
		in.By = "source"
	case "territories":
		in.By = "territory"
	}
	if in.By == "none" {
		in.By = ""
	}
	if _, ok := reports.BreakdownColumns[in.By]; in.By != "" && !ok {
		return fmt.Errorf("%w: unknown breakdown %q", tool.ErrInvalidArgs, in.By)
	}
	if in.Top < 0 {
		return fmt.Errorf("%w: top must not be negative", tool.ErrInvalidArgs)
	}
	if in.Top == 0 {
		in.Top = 10
	}
	in.Top = min(in.Top, 50)
	if (in.From == "") != (in.To == "") {
		return fmt.Errorf("%w: pass both from and to", tool.ErrInvalidArgs)
	}
	return nil
}

// funnelWindows returns the current window (ending yesterday, Pacific, for
// presets) and the equally long previous one (nil with compare none).
func funnelWindows(in funnelIn, now time.Time) (reports.Window, *reports.Window, error) {
	var from, to time.Time
	if in.From != "" {
		var err1, err2 error
		from, err1 = time.Parse(time.DateOnly, in.From)
		to, err2 = time.Parse(time.DateOnly, in.To)
		if err1 != nil || err2 != nil {
			return reports.Window{}, nil, fmt.Errorf("%w: from/to must be YYYY-MM-DD", tool.ErrInvalidArgs)
		}
		if to.Before(from) {
			return reports.Window{}, nil, fmt.Errorf("%w: to is before from", tool.ErrInvalidArgs)
		}
		if to.Sub(from) > 366*24*time.Hour {
			return reports.Window{}, nil, fmt.Errorf("%w: window longer than a year", tool.ErrInvalidArgs)
		}
	} else {
		today := applemeta.PacificDay(now)
		to = time.Date(today.Year(), today.Month(), today.Day()-1, 0, 0, 0, 0, time.UTC)
		from = to.AddDate(0, 0, -(funnelPeriods[in.Period] - 1))
	}
	cur := reports.Window{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly)}
	if in.Compare == "none" {
		return cur, nil, nil
	}
	n := cur.Days()
	prev := reports.Window{From: from.AddDate(0, 0, -n).Format(time.DateOnly), To: to.AddDate(0, 0, -n).Format(time.DateOnly)}
	return cur, &prev, nil
}

func reportsFor(question string) []string {
	switch question {
	case "sessions":
		return []string{reports.ReportSessions}
	case "crashes":
		return []string{reports.ReportCrashes, reports.ReportSessions}
	case "installs_deletions":
		return []string{reports.ReportInstalls, reports.ReportSessions}
	default:
		return []string{reports.ReportDiscovery, reports.ReportDownloads}
	}
}

// fillAnswer aggregates the loaded reports for the question.
func fillAnswer(out *funnelOut, in funnelIn, loaded map[string]*reports.AnalyticsRows) error {
	windows := []reports.Window{out.Current}
	if out.Previous != nil {
		windows = append(windows, *out.Previous)
	}
	sum := func(report, metric string, match map[string][]string, dim string, w reports.Window) (reports.Sums, error) {
		return reports.SumMetric(loaded[report], metric, match, dim, w)
	}
	switch in.Question {
	case "funnel", "sources", "territories":
		dim := reports.BreakdownColumns[in.By]
		var fs []reports.FunnelSums
		for _, w := range windows {
			imp, err := sum(reports.ReportDiscovery, reports.ColCounts, map[string][]string{reports.ColEvent: {reports.EventImpression}}, dim, w)
			if err != nil {
				return err
			}
			pv, err := sum(reports.ReportDiscovery, reports.ColCounts, map[string][]string{reports.ColEvent: {reports.EventPageView}}, dim, w)
			if err != nil {
				return err
			}
			dl, err := sum(reports.ReportDownloads, reports.ColCounts, nil, dim, w)
			if err != nil {
				return err
			}
			fs = append(fs, reports.FunnelSums{Impressions: imp, PageViews: pv, Downloads: dl})
		}
		var prev *reports.FunnelSums
		if len(fs) > 1 {
			prev = &fs[1]
		}
		total, lines := reports.Funnel(fs[0], prev, dim != "", in.Top)
		out.Funnel, out.Table = &total, lines
		return nil
	}
	type metric struct {
		name, report, col string
		match             map[string][]string
	}
	ms := []metric{
		{"sessions", reports.ReportSessions, reports.ColSessions, nil},
		{"active_devices", reports.ReportSessions, reports.ColUniqueDevices, nil},
	}
	switch in.Question {
	case "crashes":
		ms = append(ms, metric{"crashes", reports.ReportCrashes, reports.ColCrashes, nil})
	case "installs_deletions":
		ms = append(ms,
			metric{"installs", reports.ReportInstalls, reports.ColCounts, map[string][]string{reports.ColEvent: {reports.EventInstall}}},
			metric{"deletions", reports.ReportInstalls, reports.ColCounts, map[string][]string{reports.ColEvent: {reports.EventDelete}}})
	}
	vals := make([]map[string]*big.Rat, len(windows))
	for i, w := range windows {
		vals[i] = map[string]*big.Rat{}
		for _, m := range ms {
			s, err := sum(m.report, m.col, m.match, "", w)
			if err != nil {
				return err
			}
			vals[i][m.name] = s.Total()
		}
	}
	usageAnswer(out, in.Question, vals)
	return nil
}

// usageAnswer fills the usage numbers and retention proxies from the
// per-window totals (current first, previous second when compared) keyed
// sessions, active_devices, crashes, installs, deletions.
func usageAnswer(out *funnelOut, question string, vals []map[string]*big.Rat) {
	n := &numbers{Current: format(vals[0])}
	if len(vals) > 1 {
		n.Previous = format(vals[1])
		n.Change = map[string]string{}
		for k, v := range vals[0] {
			if c := reports.Change(v, vals[1][k]); c != "" {
				n.Change[k] = c
			}
		}
	}
	c := vals[0]
	switch question {
	case "crashes":
		n.Current["crashes_per_1000_sessions"] = reports.Ratio(new(big.Rat).Mul(c["crashes"], big.NewRat(1000, 1)), c["sessions"])
	default:
		out.Retention = []proxy{{
			Name: "sessions_per_active_device", Value: reports.Ratio(c["sessions"], c["active_devices"]),
			Note: "proxy: engagement depth, not cohort retention",
		}}
		if question == "installs_deletions" {
			out.Retention = append(out.Retention, proxy{
				Name: "deletions_per_install", Value: reports.Ratio(c["deletions"], c["installs"]),
				Note: "proxy: deletions in the period over installs in the period, not the same users",
			})
		}
		out.RetentionURL = "https://appstoreconnect.apple.com/analytics"
		out.Explanations = append(out.Explanations, "The Analytics Reports API has no cohort D1/D7 retention; open retention_url, pick this app, then Retention.")
	}
	out.Usage = n
}

func format(m map[string]*big.Rat) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = reports.FormatRat(v)
	}
	return out
}

// attachRows stores the merged raw rows as a session artifact and links it;
// raw rows are never inlined.
func attachRows(ctx context.Context, env *tool.Env, res *tool.Result, appID string, loaded map[string]*reports.AnalyticsRows) {
	type raw struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}
	all := map[string]raw{}
	n := 0
	for name, r := range loaded {
		all[name] = raw{Columns: r.Columns, Rows: r.Rows}
		n += len(r.Rows)
	}
	if n == 0 {
		return
	}
	b, err := json.Marshal(all)
	if err != nil {
		res.Warn("raw rows not attached: " + err.Error())
		return
	}
	link, err := env.Artifacts.Put(ctx, "analytics-funnel-"+appID+".json", "application/json", b)
	if err != nil {
		res.Warn("raw rows not attached: " + tool.BodyFor(err).Message)
		return
	}
	res.WithLink(link)
}
