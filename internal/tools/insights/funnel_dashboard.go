package insights

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/reports"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// webLoginAlt points at the web-session route when the Analytics Reports API
// cannot answer.
const webLoginAlt = "Or call web_login: with an App Store Connect web session this report reads the App Analytics dashboard right away."

// dashboardDimensions maps the by argument to App Analytics dimensions.
var dashboardDimensions = map[string]string{"source": "source", "territory": "storefront", "device": "platform"}

// dashboardFunnelMeasures are the funnel measures and the usage keys of the
// ones beyond impressions, page views and first-time downloads.
var (
	dashboardFunnelMeasures = []string{
		"impressionsTotal", "pageViewCount", "units",
		"impressionsTotalUnique", "pageViewUnique", "totalDownloads", "redownloads", "conversionRate", "proceeds", "sales",
	}
	dashboardFunnelExtras = map[string]string{
		"impressionsTotalUnique": "unique_impressions", "pageViewUnique": "unique_page_views",
		"totalDownloads": "total_downloads", "redownloads": "redownloads",
		"conversionRate": "conversion_rate_percent", "proceeds": "proceeds_usd", "sales": "sales_usd",
	}
	dashboardUsageNames = map[string]string{
		"sessions": "sessions", "activeDevices": "active_devices", "crashes": "crashes",
		"installs": "installs", "uninstalls": "deletions",
	}
)

func dashboardUsageMeasures(question string) []string {
	ms := []string{"sessions", "activeDevices"}
	switch question {
	case "crashes":
		ms = append(ms, "crashes")
	case "installs_deletions":
		ms = append(ms, "installs", "uninstalls")
	}
	return ms
}

// funnelFromDashboard answers the funnel question from the App Analytics
// dashboard through the web session, for when the Analytics Reports API is
// not set up or forbidden (why says which). Without a session it returns an
// error wrapping [tool.ErrWebSessionRequired].
func funnelFromDashboard(ctx context.Context, env *tool.Env, app tool.App, in funnelIn, why string) (*tool.Result, error) {
	end, err := reports.DataEnd(ctx, env.Portal)
	if err != nil {
		return nil, err
	}
	out := funnelOut{App: app, Status: "ok", DataSource: "web_dashboard", Question: in.Question, By: in.By}
	res := tool.OK(&out)
	from, to, note := dashboardWindow(in, end)
	out.Current = reports.Window{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly)}
	out.Explanations = append(out.Explanations,
		fmt.Sprintf("Read from the App Analytics dashboard with the web session (the Analytics Reports API is %s); figures match App Store Connect → Analytics.", why))
	if note != "" {
		out.Explanations = append(out.Explanations, note)
	}
	if from.After(to) {
		out.Status = "no_data"
		out.Message = fmt.Sprintf("App Analytics has data up to %s; the requested window starts later.", end.Format(time.DateOnly))
		return res, nil
	}
	compare := in.Compare != "none"
	if compare {
		n := out.Current.Days()
		out.Previous = &reports.Window{From: from.AddDate(0, 0, -n).Format(time.DateOnly), To: to.AddDate(0, 0, -n).Format(time.DateOnly)}
	}

	var names []string
	switch in.Question {
	case "funnel", "sources", "territories":
		names = dashboardFunnelMeasures
	default:
		names = dashboardUsageMeasures(in.Question)
	}
	ms, err := reports.Measures(ctx, env.Portal, app.ID, names, from, to, "day")
	if errors.Is(err, reports.ErrDashboardNoData) {
		out.Status, out.Previous = "no_data", nil
		out.Message = "App Analytics has no data for this app: it has never been on the App Store."
		return res, nil
	}
	if err != nil {
		return nil, err
	}

	switch in.Question {
	case "funnel", "sources", "territories":
		if err := dashboardFunnel(ctx, env, &out, in, app.ID, ms, from, to, compare); err != nil {
			return nil, err
		}
	default:
		vals := []map[string]*big.Rat{{}}
		if compare {
			vals = append(vals, map[string]*big.Rat{})
		}
		for _, k := range names {
			m := ms[k]
			vals[0][dashboardUsageNames[k]] = measureRat(m.Total)
			if compare {
				vals[1][dashboardUsageNames[k]] = measureRat(m.PreviousTotal)
			}
		}
		usageAnswer(&out, in.Question, vals)
	}

	days := map[string]bool{}
	below := false
	for _, m := range ms {
		below = below || !m.MeetsThreshold
		for _, p := range m.Points {
			if out.Current.Contains(p.Date) {
				days[p.Date] = true
			}
		}
	}
	cov := reports.Coverage{DaysExpected: out.Current.Days(), DaysPresent: len(days), LatestDay: end.Format(time.DateOnly)}
	today := applemeta.PacificDay(env.Clock.Now())
	cov.LagDays = int(time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC).Sub(end).Hours()/24 + 0.5)
	out.Coverage = &cov
	out.Explanations = append(out.Explanations, lagNote)
	if below {
		out.Explanations = append(out.Explanations, privacyNote)
	}
	return res, nil
}

// dashboardWindow returns the inclusive dashboard days for in: presets end
// at end (the last day with data); an explicit to later than end is clamped
// to it, with a note. from after the result means no data yet.
func dashboardWindow(in funnelIn, end time.Time) (from, to time.Time, note string) {
	if in.From == "" {
		return end.AddDate(0, 0, -(funnelPeriods[in.Period] - 1)), end, ""
	}
	// funnelWindows validated both dates already.
	from, _ = time.Parse(time.DateOnly, in.From)
	to, _ = time.Parse(time.DateOnly, in.To)
	if to.After(end) {
		to = end
		note = fmt.Sprintf("to was clamped to %s, the last day App Analytics has data for.", end.Format(time.DateOnly))
	}
	return from, to, note
}

// dashboardFunnel fills the funnel line, the extra headline numbers and,
// with a breakdown, the per-key table (current period only).
func dashboardFunnel(ctx context.Context, env *tool.Env, out *funnelOut, in funnelIn, appID string,
	ms map[string]reports.DashboardMeasure, from, to time.Time, compare bool,
) error {
	sums := func(prev bool) reports.FunnelSums {
		v := func(k string) reports.Sums {
			if prev {
				return reports.Sums{"": measureRat(ms[k].PreviousTotal)}
			}
			return reports.Sums{"": measureRat(ms[k].Total)}
		}
		return reports.FunnelSums{Impressions: v("impressionsTotal"), PageViews: v("pageViewCount"), Downloads: v("units")}
	}
	var prev *reports.FunnelSums
	if compare {
		p := sums(true)
		prev = &p
	}
	total, _ := reports.Funnel(sums(false), prev, false, in.Top)
	out.Funnel = &total

	cur, before := map[string]*big.Rat{}, map[string]*big.Rat{}
	for key, name := range dashboardFunnelExtras {
		if m, ok := ms[key]; ok {
			cur[name], before[name] = measureRat(m.Total), measureRat(m.PreviousTotal)
		}
	}
	n := &numbers{Current: format(cur)}
	if compare {
		n.Previous = format(before)
		n.Change = map[string]string{}
		for k, v := range cur {
			if c := reports.Change(v, before[k]); c != "" {
				n.Change[k] = c
			}
		}
	}
	out.Usage = n

	dim := dashboardDimensions[in.By]
	if dim == "" {
		return nil
	}
	split := reports.FunnelSums{Impressions: reports.Sums{}, PageViews: reports.Sums{}, Downloads: reports.Sums{}}
	for metric, into := range map[string]reports.Sums{"impressionsTotal": split.Impressions, "pageViewCount": split.PageViews, "units": split.Downloads} {
		groups, err := reports.Breakdown(ctx, env.Portal, appID, metric, dim, from, to, in.Top)
		if err != nil {
			return err
		}
		for _, g := range groups {
			key := g.Title
			if key == "" {
				key = g.Key
			}
			into[key] = measureRat(g.Total)
		}
	}
	_, out.Table = reports.Funnel(split, nil, true, in.Top)
	out.Explanations = append(out.Explanations, "Breakdown rows cover the current period only; keys with no activity are left out.")
	return nil
}

// measureRat converts a dashboard value to a rational at its display precision.
func measureRat(v float64) *big.Rat {
	r, ok := new(big.Rat).SetString(reports.FormatMeasure(v))
	if !ok {
		return new(big.Rat)
	}
	return r
}
