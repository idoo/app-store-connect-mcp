package reports

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/reports"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tools/insights"
)

// portfolioApp is one app's in-flight versions in an every-app release
// section; the app name is third-party text.
type portfolioApp struct {
	ID       string           `json:"id"`
	Name     tool.Untrusted   `json:"name,omitempty"`
	InFlight []versionInsight `json:"in_flight"`
}

// portfolioRelease is the release section without an app (PO2): in-flight
// versions per app from ONE apps call with versions included (state only,
// no per-app preflight), ranked as digest_summary app:"all" ranks them.
func portfolioRelease(ranked []insights.PortfolioApp, err error) releaseInsight {
	if err != nil {
		return releaseInsight{section: unavailable(reasonOf(err))}
	}
	out := releaseInsight{section: sectionOK, Apps: []portfolioApp{}}
	for _, p := range ranked {
		if len(p.InFlight) == 0 {
			continue
		}
		pa := portfolioApp{ID: p.App.ID, Name: tool.Untrusted(p.App.Name)}
		for _, v := range p.InFlight {
			pa.InFlight = append(pa.InFlight, versionInsight{ID: v.ID, Version: v.Version, Platform: v.Platform, State: v.State})
		}
		out.Apps = append(out.Apps, pa)
	}
	return out
}

// dashboardSummaryMeasures are the App Analytics measures insights_summary
// reads per app, with their output names. Averages (conversion rate) are
// shown per app but never summed into account totals.
var dashboardSummaryMeasures = []struct {
	key, name string
	average   bool
}{
	{"impressionsTotal", "impressions", false},
	{"pageViewCount", "page_views", false},
	{"units", "downloads", false},
	{"totalDownloads", "total_downloads", false},
	{"proceeds", "proceeds_usd", false},
	{"sessions", "sessions", false},
	{"crashes", "crashes", false},
	{"conversionRate", "conversion_rate_percent", true},
}

// dashboardNoSession names the web sign-in that makes the dashboard readable.
const dashboardNoSession = "call web_login (web toolset): with an App Store Connect web session App Analytics dashboard figures are read for every app, any key role"

// dashboardTotals are dashboard figures of the window and the equally long
// window before it.
type dashboardTotals struct {
	Current  map[string]string `json:"current"`
	Previous map[string]string `json:"previous,omitempty"`
	Change   map[string]string `json:"change,omitempty"`
}

// dashboardAppRef names an app; the name is third-party text.
type dashboardAppRef struct {
	ID   string         `json:"id"`
	Name tool.Untrusted `json:"name,omitempty"`
}

// dashboardAppStats is one app's dashboard figures, or why they are missing.
type dashboardAppStats struct {
	dashboardAppRef
	Current  map[string]string `json:"current,omitempty"`
	Previous map[string]string `json:"previous,omitempty"`
	Error    string            `json:"error,omitempty"`
}

// dashboardAnalytics reads App Analytics dashboard totals through the web
// session: for one app (single) its totals, otherwise every app's figures
// (never-released apps listed under no_data, failures per app) sorted by
// downloads, with account totals of the summable measures.
func dashboardAnalytics(ctx context.Context, env *tool.Env, apps []tool.App, single bool, preset string, from, to time.Time) analyticsInsight {
	end, err := reports.DataEnd(ctx, env.Portal)
	if errors.Is(err, tool.ErrWebSessionRequired) {
		return analyticsInsight{section: unavailable(dashboardNoSession), noSession: true}
	}
	if err != nil {
		return analyticsInsight{section: unavailable(reasonOf(err))}
	}
	f, t, note := dashboardPeriod(preset, from, to, end)
	if f.After(t) {
		return analyticsInsight{section: unavailable(fmt.Sprintf("App Analytics has data up to %s, before this period starts", end.Format(time.DateOnly)))}
	}
	days := int(t.Sub(f).Hours()/24+0.5) + 1
	out := analyticsInsight{
		section:        sectionOK,
		DataSource:     "web_dashboard",
		Window:         &window{From: f.Format(time.DateOnly), To: t.Format(time.DateOnly)},
		PreviousWindow: &window{From: f.AddDate(0, 0, -days).Format(time.DateOnly), To: t.AddDate(0, 0, -days).Format(time.DateOnly)},
	}
	keys := make([]string, len(dashboardSummaryMeasures))
	for i, m := range dashboardSummaryMeasures {
		keys[i] = m.key
	}
	type read struct {
		ms  map[string]reports.DashboardMeasure
		err error
	}
	reads := make([]read, len(apps))
	if err := tool.Parallel(ctx, 4, apps, func(ctx context.Context, i int, a tool.App) error {
		reads[i].ms, reads[i].err = reports.Measures(ctx, env.Portal, a.ID, keys, f, t, "day")
		return nil
	}); err != nil {
		return analyticsInsight{section: unavailable(reasonOf(err))}
	}

	var notes []string
	if note != "" {
		notes = append(notes, note)
	}
	if single {
		r := reads[0]
		switch {
		case errors.Is(r.err, reports.ErrDashboardNoData):
			return analyticsInsight{section: unavailable("App Analytics has no data for this app: it has never been on the App Store")}
		case r.err != nil:
			return analyticsInsight{section: unavailable(reasonOf(r.err))}
		}
		cur, prev := dashboardValues(r.ms)
		out.Totals = totalsOf(cur, prev)
		out.Reason = strings.Join(notes, "; ")
		return out
	}

	sumCur, sumPrev := map[string]*big.Rat{}, map[string]*big.Rat{}
	type ranked struct {
		stats     dashboardAppStats
		downloads float64
		failed    bool
	}
	var rows []ranked
	failed := 0
	var firstErr error
	for i, a := range apps {
		ref := dashboardAppRef{ID: a.ID, Name: tool.Untrusted(a.Name)}
		r := reads[i]
		switch {
		case errors.Is(r.err, reports.ErrDashboardNoData):
			out.NoData = append(out.NoData, ref)
			continue
		case r.err != nil:
			failed++
			firstErr = cmp.Or(firstErr, r.err)
			rows = append(rows, ranked{stats: dashboardAppStats{dashboardAppRef: ref, Error: reasonOf(r.err)}, failed: true})
			continue
		}
		cur, prev := dashboardValues(r.ms)
		for _, m := range dashboardSummaryMeasures {
			if v, ok := r.ms[m.key]; ok && !m.average && v.Type != "AVERAGE" {
				addRat(sumCur, m.name, cur[m.name])
				addRat(sumPrev, m.name, prev[m.name])
			}
		}
		rows = append(rows, ranked{
			stats:     dashboardAppStats{dashboardAppRef: ref, Current: formatRats(cur), Previous: formatRats(prev)},
			downloads: r.ms["units"].Total,
		})
	}
	if failed > 0 && failed == len(rows) && len(out.NoData) == 0 {
		return analyticsInsight{section: unavailable(reasonOf(firstErr))}
	}
	slices.SortStableFunc(rows, func(a, b ranked) int {
		if a.failed != b.failed {
			if a.failed {
				return 1
			}
			return -1
		}
		if c := cmp.Compare(b.downloads, a.downloads); c != 0 {
			return c
		}
		return cmp.Compare(string(a.stats.Name), string(b.stats.Name))
	})
	out.Apps = make([]dashboardAppStats, len(rows))
	for i, r := range rows {
		out.Apps[i] = r.stats
	}
	out.Totals = totalsOf(sumCur, sumPrev)
	if failed > 0 {
		notes = append(notes, fmt.Sprintf("%d app(s) could not be read; account totals leave them out", failed))
	}
	if len(out.NoData) > 0 {
		notes = append(notes, "no_data lists apps App Analytics has no data for (never on the App Store)")
	}
	out.Reason = strings.Join(notes, "; ")
	return out
}

// dashboardPeriod maps an insights_summary period onto dashboard days.
// Periods ending after the dashboard's last data day: rolling ones
// (yesterday, last_7_days) move back to end on it, calendar ones are cut
// at it. The note says what changed ("" when nothing did).
func dashboardPeriod(preset string, from, to, end time.Time) (time.Time, time.Time, string) {
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
	from, to = day(from), day(to)
	if !to.After(end) {
		return from, to, ""
	}
	last := end.Format(time.DateOnly)
	switch preset {
	case "yesterday", "last_7_days":
		n := int(to.Sub(from).Hours()/24 + 0.5)
		f := end.AddDate(0, 0, -n)
		return f, end, fmt.Sprintf("App Analytics has data up to %s, so analytics covers %s..%s", last, f.Format(time.DateOnly), last)
	default:
		return from, end, fmt.Sprintf("App Analytics has data up to %s; later days of the period are not in it yet", last)
	}
}

// dashboardValues returns the current and previous values by output name.
func dashboardValues(ms map[string]reports.DashboardMeasure) (cur, prev map[string]*big.Rat) {
	cur, prev = map[string]*big.Rat{}, map[string]*big.Rat{}
	for _, m := range dashboardSummaryMeasures {
		v, ok := ms[m.key]
		if !ok {
			continue
		}
		cur[m.name], prev[m.name] = measureRat(v.Total), measureRat(v.PreviousTotal)
	}
	return cur, prev
}

func totalsOf(cur, prev map[string]*big.Rat) *dashboardTotals {
	t := &dashboardTotals{Current: formatRats(cur), Previous: formatRats(prev), Change: map[string]string{}}
	for k, v := range cur {
		if c := pctChange(v, prev[k]); c != "" {
			t.Change[k] = c
		}
	}
	if len(t.Change) == 0 {
		t.Change = nil
	}
	return t
}

func addRat(m map[string]*big.Rat, k string, v *big.Rat) {
	if v == nil {
		return
	}
	if m[k] == nil {
		m[k] = new(big.Rat)
	}
	m[k].Add(m[k], v)
}

func formatRats(m map[string]*big.Rat) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = reports.FormatRat(v)
	}
	return out
}

// measureRat converts a dashboard value to a rational at its display precision.
func measureRat(v float64) *big.Rat {
	r, ok := new(big.Rat).SetString(reports.FormatMeasure(v))
	if !ok {
		return new(big.Rat)
	}
	return r
}
