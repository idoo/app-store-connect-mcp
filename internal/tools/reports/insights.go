package reports

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/reports"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	insightsMaxReviews  = 200
	insightsMaxFeedback = 50
	insightsMaxVersions = 5
)

type insightsIn struct {
	App    tool.AppRef `json:"app,omitempty" jsonschema:"default pinned, else all (sales/ratings)"`
	Period string      `json:"period,omitempty" jsonschema:"Pacific; default last_7_days"`
}

func init() {
	periods := make([]any, 0, len(applemeta.PeriodPresets))
	for _, p := range applemeta.PeriodPresets {
		periods = append(periods, p)
	}
	tool.Register(tool.New(tool.Meta{
		Name: "insights_summary", Toolset: tool.Reports, Class: tool.Read, Idempotent: true,
		Title: "How is my app doing",
		Description: "Use first for \"how is my app doing\": units and proceeds vs previous period, ratings, versions, " +
			"TestFlight feedback, analytics; each section ok or unavailable+why.",
		Enums: map[string][]any{"period": periods},
	}, insightsSummary))
}

// window is an inclusive range of Pacific days.
type window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// section is the common status of one insights section.
type section struct {
	Status string `json:"status"` // ok | unavailable
	Reason string `json:"reason,omitempty"`
}

func unavailable(reason string) section { return section{Status: "unavailable", Reason: reason} }

var sectionOK = section{Status: "ok"}

type insightsOut struct {
	App        *tool.App        `json:"app,omitempty"`
	Period     string           `json:"period"`
	Current    window           `json:"current"`
	Previous   window           `json:"previous"`
	Sales      salesInsight     `json:"sales"`
	Ratings    ratingsInsight   `json:"ratings"`
	Release    releaseInsight   `json:"release"`
	TestFlight feedbackInsight  `json:"testflight"`
	Analytics  analyticsInsight `json:"analytics"`
}

type salesInsight struct {
	section
	// vendorMissing marks the profile has no vendor number (never serialized).
	vendorMissing bool
	ComparedDays  int                         `json:"compared_days,omitempty"`
	CurrentDays   int                         `json:"current_days,omitempty"`
	PreviousDays  int                         `json:"previous_days,omitempty"`
	PendingDays   []string                    `json:"pending_days,omitempty"`
	Units         reports.Units               `json:"units,omitempty"`
	PrevUnits     reports.Units               `json:"previous_units,omitempty"`
	UnitsChange   map[reports.Category]string `json:"units_change,omitempty"`
	Proceeds      []proceedsChange            `json:"proceeds,omitempty"`
}

type proceedsChange struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
	Previous string `json:"previous"`
	Change   string `json:"change,omitempty"`
}

type ratingsInsight struct {
	section
	NewReviews     int    `json:"new_reviews"`
	Average        string `json:"average,omitempty"`
	PrevNewReviews int    `json:"previous_new_reviews"`
	PrevAverage    string `json:"previous_average,omitempty"`
	Stars          [5]int `json:"stars"` // 1★..5★ counts in the current period
	Capped         bool   `json:"capped,omitempty"`
	// AppsScanned and AppsWithReviews are set when every app (from
	// /v1/apps, as reviews_list app:"all" counts them) was aggregated.
	AppsScanned     int `json:"apps_scanned,omitempty"`
	AppsWithReviews int `json:"apps_with_reviews,omitempty"`
}

type versionInsight struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
	State    string `json:"state"`
	Phased   string `json:"phased_release,omitempty"`
	Day      int    `json:"phased_day,omitempty"`
}

type releaseInsight struct {
	section
	Live     *versionInsight  `json:"live,omitempty"`
	InFlight []versionInsight `json:"in_flight,omitempty"`
	// Apps lists in-flight versions per app when no app was selected.
	Apps []portfolioApp `json:"apps,omitempty"`
}

type feedbackInsight struct {
	section
	Screenshots int  `json:"screenshot_feedback"`
	Crashes     int  `json:"crash_feedback"`
	Capped      bool `json:"capped,omitempty"`
}

type analyticsInsight struct {
	section
	AccessTypes []string `json:"access_types,omitempty"`
}

func insightsSummary(ctx context.Context, env *tool.Env, in insightsIn) (*tool.Result, error) {
	preset := in.Period
	if preset == "" {
		preset = "last_7_days"
	}
	now := env.Clock.Now()
	from, to, _, err := applemeta.Period(preset, now)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", tool.ErrInvalidArgs, err)
	}
	pFrom, pTo := previousWindow(preset, from, to)
	out := insightsOut{
		Period:   preset,
		Current:  window{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly)},
		Previous: window{From: pFrom.Format(time.DateOnly), To: pTo.Format(time.DateOnly)},
	}
	res := tool.OK(&out)

	var app *tool.App
	noApp := ""
	if in.App.IsAll() {
		noApp = `needs one app for this section: app "all" covers sales, ratings and releases`
	} else if a, err := env.Resolve.App(ctx, in.App); err == nil {
		app = &a
		out.App = app
	} else if in.App != "" {
		return nil, err
	} else {
		noApp = "needs one app: pass app (" + tool.BodyFor(err).Message + ")"
	}

	today := applemeta.PacificDay(now)
	out.Sales = salesSection(ctx, env, app, from, to, pFrom, pTo, today)
	if out.Sales.vendorMissing {
		// The sales_report suggestion would fail the same way; the next step
		// is the one-time setup command and where to find the number.
		te := vendorNumberMissing(env.Profile.Name).(*tool.Error)
		res.Warn("next step for sales: " + te.Hint + "; " + te.Message)
	}
	if app == nil {
		out.Ratings = allAppsRatings(ctx, env, from, to, pFrom)
		out.Release = portfolioRelease(ctx, env)
		out.TestFlight.section = unavailable(noApp)
		out.Analytics.section = unavailable(noApp)
		return res, ctx.Err()
	}
	out.Ratings = ratingsSection(ctx, env, app.ID, from, to, pFrom)
	out.Release = releaseSection(ctx, env, app.ID)
	out.TestFlight = feedbackSection(ctx, env, app.ID, from)
	out.Analytics = analyticsSection(ctx, env, app.ID)
	switch {
	case out.Analytics.Status != "ok" && out.Analytics.Reason == analyticsMissingReason:
		res.Then(tool.NextStep{Tool: "analytics_setup", Args: appIDArgs(app.ID)})
	case out.Analytics.Status == "ok":
		res.Then(tool.NextStep{Tool: "analytics_funnel_report", Args: map[string]any{"app": app.ID}})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if out.Sales.Status == "ok" {
		res.Then(tool.NextStep{Tool: "sales_report", Args: map[string]any{"app": app.ID, "period": preset, "group_by": []string{"country"}}})
	}
	return res, nil
}

// reasonOf turns a section error into a short reason.
func reasonOf(err error) string { return tool.BodyFor(err).Message }

// salesSection compares SALES units and proceeds over equally long windows:
// the first N published days of the current window against the first N days
// of the previous one, so a 31-day month never "grows" against a 28-day one.
func salesSection(ctx context.Context, env *tool.Env, app *tool.App, from, to, pFrom, pTo, today time.Time) salesInsight {
	if env.Profile.KeyKind == tool.KeyIndividual {
		return salesInsight{section: unavailable("sales reports need a Team API key; Individual keys cannot read them")}
	}
	vendor, err := requireVendor(env)
	if err != nil {
		var te *tool.Error
		if errors.As(err, &te) {
			return salesInsight{section: unavailable(te.Message + "; " + te.Hint), vendorMissing: true}
		}
		return salesInsight{section: unavailable(reasonOf(err))}
	}
	if to.After(today.AddDate(0, 0, -1)) {
		to = today.AddDate(0, 0, -1)
	}
	cur := dayList(from, to)
	prev := dayList(pFrom, pTo)
	if len(cur) == 0 {
		return salesInsight{section: unavailable("no day of this period is published yet (Apple publishes daily reports the next day, Pacific)")}
	}
	rs, _ := applemeta.SalesReport(applemeta.ReportSales)
	tables, err := fetchSalesDates(ctx, env, vendor, rs, rs.SubTypes[0], applemeta.Daily, slices.Concat(cur, prev))
	if err != nil {
		return salesInsight{section: unavailable(reasonOf(err))}
	}
	curT, prevT := tables[:len(cur)], tables[len(cur):]
	out := salesInsight{section: sectionOK}
	// Unpublished trailing current days drop the same number of previous days,
	// so the comparison stays like-for-like.
	n := len(curT)
	for n > 0 && curT[n-1].Pending {
		out.PendingDays = append([]string{curT[n-1].Date}, out.PendingDays...)
		n--
	}
	if n == 0 {
		return salesInsight{section: unavailable("no day of this period is published yet"), PendingDays: out.PendingDays}
	}
	out.CurrentDays, out.PreviousDays = n, len(prevT)
	n = min(n, len(prevT))
	prevT = prevT[:n]
	out.ComparedDays = n
	if n < out.CurrentDays {
		out.Reason = fmt.Sprintf("compared the first %d days of each window (current %d days, previous %d)", n, out.CurrentDays, out.PreviousDays)
	}
	curAgg, err := salesTotals(curT[:n], app, &out.PendingDays)
	if err != nil {
		return salesInsight{section: unavailable(reasonOf(err))}
	}
	var prevPending []string
	prevAgg, err := salesTotals(prevT, app, &prevPending)
	if err != nil {
		return salesInsight{section: unavailable(reasonOf(err))}
	}
	out.Units, out.PrevUnits = curAgg.Units, prevAgg.Units
	out.UnitsChange = map[reports.Category]string{}
	for _, c := range unionKeys(curAgg.Units, prevAgg.Units) {
		if ch := pctChange(curAgg.Units.Get(c), prevAgg.Units.Get(c)); ch != "" {
			out.UnitsChange[c] = ch
		}
	}
	// One row per currency of either period, sorted by currency code.
	all := reports.Money{}
	all.AddMoney(curAgg.Proceeds)
	all.AddMoney(prevAgg.Proceeds)
	for _, c := range all.Currencies() {
		cur, p := curAgg.Proceeds[c], prevAgg.Proceeds[c]
		ch := "-100.0%"
		if cur != nil {
			ch = pctChange(cur, p)
		}
		out.Proceeds = append(out.Proceeds, proceedsChange{
			Currency: c, Amount: reports.FormatExact(c, cur), Previous: reports.FormatExact(c, p), Change: ch,
		})
	}
	if len(prevPending) > 0 {
		note := fmt.Sprintf("previous period has %d unpublished day(s); its totals may be low", len(prevPending))
		if out.Reason != "" {
			note = out.Reason + "; " + note
		}
		out.Reason = note
	}
	return out
}

// salesTotals sums SALES rows of the app (all apps when nil); pending
// (unpublished) days are appended to pending. No-sales days count as covered
// days with zero units, so they never shrink the compared window.
func salesTotals(days []dayTable, app *tool.App, pending *[]string) (reports.SalesGroup, error) {
	var rows []reports.SalesRow
	for _, d := range days {
		if d.Pending {
			*pending = append(*pending, d.Date)
			continue
		}
		if d.NoSales {
			continue
		}
		rs, err := reports.SalesRows(d.Table)
		if err != nil {
			return reports.SalesGroup{}, fmt.Errorf("sales report %s: %w", d.Date, err)
		}
		for _, r := range rs {
			if appMatchesSales(r, app) {
				rows = append(rows, r)
			}
		}
	}
	return reports.AggregateSales(rows, nil, 0, reports.DefaultCategory).Totals, nil
}

func unionKeys(a, b reports.Units) []reports.Category {
	var out []reports.Category
	for k := range a {
		out = append(out, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// ratingsSection averages ratings of reviews created in the current and
// previous windows (newest 200 reviews at most).
func ratingsSection(ctx context.Context, env *tool.Env, appID string, from, to, pFrom time.Time) ratingsInsight {
	out, cur, prev, err := ratingsTally(ctx, env, appID, from, to, pFrom)
	if err != nil {
		return ratingsInsight{section: unavailable(reasonOf(err))}
	}
	out.Average = average(cur, out.NewReviews)
	out.PrevAverage = average(prev, out.PrevNewReviews)
	return out
}

// allAppsRatings aggregates ratingsTally over every app /v1/apps returns.
func allAppsRatings(ctx context.Context, env *tool.Env, from, to, pFrom time.Time) ratingsInsight {
	apps, err := env.ASC.List(ctx, "/v1/apps", tool.ListOptions{
		Fields: map[string][]string{"apps": {"name"}}, Limit: 200, MaxItems: 5000,
	})
	if err != nil {
		return ratingsInsight{section: unavailable(reasonOf(err))}
	}
	type tally struct {
		r         ratingsInsight
		cur, prev int
		err       error
	}
	tallies := make([]tally, len(apps.Data))
	if err := tool.Parallel(ctx, 4, apps.Data, func(ctx context.Context, i int, a tool.Resource) error {
		t := &tallies[i]
		t.r, t.cur, t.prev, t.err = ratingsTally(ctx, env, a.ID, from, to, pFrom)
		return nil
	}); err != nil {
		return ratingsInsight{section: unavailable(reasonOf(err))}
	}
	out := ratingsInsight{section: sectionOK, AppsScanned: len(apps.Data)}
	var cur, prev, failed int
	for _, t := range tallies {
		if t.err != nil {
			failed++
			continue
		}
		if t.r.NewReviews > 0 {
			out.AppsWithReviews++
		}
		out.NewReviews += t.r.NewReviews
		out.PrevNewReviews += t.r.PrevNewReviews
		for i, n := range t.r.Stars {
			out.Stars[i] += n
		}
		out.Capped = out.Capped || t.r.Capped
		cur, prev = cur+t.cur, prev+t.prev
	}
	out.Average = average(cur, out.NewReviews)
	out.PrevAverage = average(prev, out.PrevNewReviews)
	switch {
	case failed == len(tallies) && failed > 0:
		return ratingsInsight{section: unavailable(reasonOf(tallies[0].err))}
	case failed > 0:
		out.Reason = fmt.Sprintf("%d app(s) could not be read; totals may be low", failed)
	case out.Capped:
		out.Reason = fmt.Sprintf("only the newest %d reviews per app were read; earlier counts may be low", insightsMaxReviews)
	}
	return out
}

// ratingsTally counts one app's reviews in the current and previous windows
// and returns the rating sums.
func ratingsTally(ctx context.Context, env *tool.Env, appID string, from, to, pFrom time.Time) (out ratingsInsight, cur, prev int, err error) {
	res, err := env.ASC.List(ctx, "/v1/apps/"+appID+"/customerReviews", tool.ListOptions{
		Query:    map[string][]string{"sort": {"-createdDate"}},
		Fields:   map[string][]string{"customerReviews": {"rating", "createdDate"}},
		Limit:    insightsMaxReviews,
		MaxItems: insightsMaxReviews,
	})
	if err != nil {
		return out, 0, 0, err
	}
	end := to.AddDate(0, 0, 1)
	out = ratingsInsight{section: sectionOK}
	reachedStart := false
	for _, r := range res.Data {
		t, err := time.Parse(time.RFC3339, r.Str("createdDate"))
		if err != nil {
			continue
		}
		rating, _ := r.Int("rating")
		switch {
		case !t.Before(end):
		case !t.Before(from):
			out.NewReviews++
			cur += rating
			if rating >= 1 && rating <= 5 {
				out.Stars[rating-1]++
			}
		case !t.Before(pFrom):
			out.PrevNewReviews++
			prev += rating
		default:
			reachedStart = true
		}
	}
	out.Capped = !reachedStart && res.NextCursor != ""
	if out.Capped {
		out.Reason = fmt.Sprintf("only the newest %d reviews were read; earlier counts may be low", insightsMaxReviews)
	}
	return out, cur, prev, nil
}

func average(sum, n int) string {
	if n == 0 {
		return ""
	}
	return big.NewRat(int64(sum), int64(n)).FloatString(2)
}

// releaseSection reports the live version and versions in flight with their
// phased release state.
func releaseSection(ctx context.Context, env *tool.Env, appID string) releaseInsight {
	res, err := env.ASC.List(ctx, "/v1/apps/"+appID+"/appStoreVersions", tool.ListOptions{
		Fields: map[string][]string{
			"appStoreVersions":              {"platform", "versionString", "appStoreState", "appVersionState", "appStoreVersionPhasedRelease"},
			"appStoreVersionPhasedReleases": {"phasedReleaseState", "currentDayNumber"},
		},
		Include:  []string{"appStoreVersionPhasedRelease"},
		Limit:    insightsMaxVersions,
		MaxItems: insightsMaxVersions,
	})
	if err != nil {
		return releaseInsight{section: unavailable(reasonOf(err))}
	}
	phased := map[string]tool.Resource{}
	for _, r := range res.Included {
		if r.Type == "appStoreVersionPhasedReleases" {
			phased[r.ID] = r
		}
	}
	out := releaseInsight{section: sectionOK}
	for _, v := range res.Data {
		state := v.Str("appVersionState")
		if state == "" {
			state = v.Str("appStoreState")
		}
		vi := versionInsight{ID: v.ID, Version: v.Str("versionString"), Platform: v.Str("platform"), State: state}
		if p, ok := phased[v.RelID("appStoreVersionPhasedRelease")]; ok {
			vi.Phased = p.Str("phasedReleaseState")
			vi.Day, _ = p.Int("currentDayNumber")
		}
		switch state {
		case "READY_FOR_SALE", "READY_FOR_DISTRIBUTION":
			if out.Live == nil {
				out.Live = &vi
			}
		case "REPLACED_WITH_NEW_VERSION", "DEVELOPER_REMOVED_FROM_SALE", "REMOVED_FROM_SALE":
		default:
			out.InFlight = append(out.InFlight, vi)
		}
	}
	return out
}

// feedbackSection counts TestFlight screenshot and crash feedback created
// since the start of the current window.
func feedbackSection(ctx context.Context, env *tool.Env, appID string, from time.Time) feedbackInsight {
	out := feedbackInsight{section: sectionOK}
	for _, kind := range []string{"betaFeedbackScreenshotSubmissions", "betaFeedbackCrashSubmissions"} {
		res, err := env.ASC.List(ctx, "/v1/apps/"+appID+"/"+kind, tool.ListOptions{
			Query:    map[string][]string{"sort": {"-createdDate"}},
			Fields:   map[string][]string{kind: {"createdDate"}},
			Limit:    insightsMaxFeedback,
			MaxItems: insightsMaxFeedback,
		})
		if err != nil {
			return feedbackInsight{section: unavailable(reasonOf(err))}
		}
		n, older := 0, false
		for _, r := range res.Data {
			t, err := time.Parse(time.RFC3339, r.Str("createdDate"))
			if err != nil {
				continue
			}
			if t.Before(from) {
				older = true
				continue
			}
			n++
		}
		if !older && res.NextCursor != "" {
			out.Capped = true
		}
		if kind == "betaFeedbackScreenshotSubmissions" {
			out.Screenshots = n
		} else {
			out.Crashes = n
		}
	}
	if out.Capped {
		out.Reason = fmt.Sprintf("more than %d feedback items; counts are a lower bound", insightsMaxFeedback)
	}
	return out
}

const analyticsMissingReason = "analytics not set up: no active analytics report request (call analytics_setup)"

// analyticsSection reports whether Apple is generating analytics reports.
func analyticsSection(ctx context.Context, env *tool.Env, appID string) analyticsInsight {
	reqs, err := analyticsRequests(ctx, env, appID)
	if err != nil {
		return analyticsInsight{section: unavailable(reasonOf(err))}
	}
	out := analyticsInsight{section: sectionOK}
	for _, access := range []string{accessOngoing, accessSnapshot} {
		if _, ok := activeRequest(reqs, access); ok {
			out.AccessTypes = append(out.AccessTypes, access)
		}
	}
	if len(out.AccessTypes) == 0 {
		return analyticsInsight{section: unavailable(analyticsMissingReason)}
	}
	out.Reason = "use analytics_funnel_report (toolset insights) for impressions → page views → downloads, sessions and crashes"
	return out
}
