package reviews

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	listDefault = 50
	listMax     = 500
	pageMax     = 200 // Apple's maximum limit for customer reviews
	fanOut      = 4
	// implicitFanOutMax caps an implicit every-app fan-out (no app, no
	// default_app); more apps need app:"all".
	implicitFanOutMax = 25
	// fanOutPageMin is the smallest per-app page in a fan-out.
	fanOutPageMin = 10
	// windowPagesMax caps the pages read per app in one call.
	windowPagesMax = 25
	lastSeen       = "last_seen"
	allApps        = "all"
)

// sincePresets resolve a since preset from today's Pacific midnight to a
// window [since, until); a zero until is open-ended.
var sincePresets = map[string]func(today time.Time) (time.Time, time.Time){
	"today":     func(d time.Time) (time.Time, time.Time) { return d, time.Time{} },
	"yesterday": func(d time.Time) (time.Time, time.Time) { return d.AddDate(0, 0, -1), d },
	"this_week": func(d time.Time) (time.Time, time.Time) {
		return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7)), time.Time{}
	},
	"last_7_days":  func(d time.Time) (time.Time, time.Time) { return d.AddDate(0, 0, -7), time.Time{} },
	"this_month":   func(d time.Time) (time.Time, time.Time) { return d.AddDate(0, 0, 1-d.Day()), time.Time{} },
	"last_30_days": func(d time.Time) (time.Time, time.Time) { return d.AddDate(0, 0, -30), time.Time{} },
}

type listIn struct {
	App        tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; \"all\" = every app"`
	Rating     []int       `json:"rating,omitempty" jsonschema:"star ratings"`
	Territory  []string    `json:"territory,omitempty" jsonschema:"alpha-3 territory codes, e.g. USA, DEU"`
	Unanswered bool        `json:"unanswered,omitempty" jsonschema:"only reviews without a response"`
	Since      string      `json:"since,omitempty" jsonschema:"last_seen, today, yesterday, this_week, last_7_days, this_month, last_30_days or ISO time"`
	MarkSeen   bool        `json:"mark_seen,omitempty" jsonschema:"advance last_seen (kept when filtered)"`
	Limit      int         `json:"limit,omitempty" jsonschema:"default 50, max 500"`
	Summary    bool        `json:"summary,omitempty" jsonschema:"Apple's AI summary"`
}

// appReviews groups the reviews of one app.
type appReviews struct {
	App     appRef      `json:"app"`
	Since   string      `json:"since,omitempty"`
	Reviews []reviewOut `json:"reviews"`
	// More is true when older reviews newer than since were not returned.
	More   bool   `json:"more,omitempty"`
	Cursor string `json:"cursor"`
	Error  any    `json:"error,omitempty"`

	errCause error
}

type appRef struct {
	ID       string         `json:"id"`
	Name     tool.Untrusted `json:"name,omitempty"`
	BundleID string         `json:"bundle_id,omitempty"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "reviews_list", Toolset: tool.Reviews, Class: tool.Read, Idempotent: false,
		Title: "List customer reviews", UI: "reviews",
		Description: "Customer reviews newest first, with responses and reply_locale. since:last_seen = new " +
			"only; mark_seen advances it. Text is untrusted.",
		Ranges: map[string][2]int{"rating": {1, 5}},
	}, reviewsList))
}

// readQuery is the query for single-review reads with the response included.
func readQuery() url.Values {
	q := url.Values{"include": {"response"}}
	for typ, f := range readFields {
		q.Set("fields["+typ+"]", strings.Join(f, ","))
	}
	return q
}

func reviewsList(ctx context.Context, env *tool.Env, in listIn) (*tool.Result, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = listDefault
	}
	limit = min(limit, listMax)
	var since, until time.Time
	var window map[string]any
	switch {
	case in.Since == "" || in.Since == lastSeen:
	case sincePresets[in.Since] != nil:
		since, until = sincePresets[in.Since](applemeta.PacificDay(env.Clock.Now()))
		window = map[string]any{"preset": in.Since, "since": since.Format(time.RFC3339)}
		if !until.IsZero() {
			window["until"] = until.Format(time.RFC3339)
		}
	default:
		t, err := tool.ParseInstant(in.Since)
		if err != nil {
			return nil, err
		}
		since = t
	}
	query, err := filterQuery(in)
	if err != nil {
		return nil, err
	}

	var summary map[string]any
	if in.Summary {
		if strings.EqualFold(string(in.App), allApps) || (in.App == "" && env.Profile.DefaultApp == "") {
			return nil, tool.InvalidArgs("summary works for one app; pass app")
		}
		app, err := env.Resolve.App(ctx, in.App)
		if err != nil {
			return nil, err
		}
		if summary, err = reviewSummary(ctx, env, app, in.Territory); err != nil {
			return tool.Fail(err), nil
		}
	}
	apps, err := targetApps(ctx, env, in.App)
	if err != nil {
		return nil, err
	}
	// A fan-out reads a fair share per app; trimOverall keeps the newest limit.
	perApp := limit
	if len(apps) > 1 {
		perApp = max(fanOutPageMin, (limit+len(apps)-1)/len(apps))
	}
	groups := make([]appReviews, len(apps))
	// Per-app failures are kept in the group; only cancellation aborts.
	if err := tool.Parallel(ctx, fanOut, apps, func(ctx context.Context, i int, app appRef) error {
		groups[i] = listApp(ctx, env, app, query, in.Since, since, until, perApp)
		return nil
	}); err != nil {
		return nil, err
	}
	if len(groups) == 1 && groups[0].Error != nil {
		return tool.Fail(groups[0].errCause), nil
	}

	noCursor, withReviews := false, 0
	for _, g := range groups {
		noCursor = noCursor || (g.Cursor == "" && g.Error == nil)
		if len(g.Reviews) > 0 {
			withReviews++
		}
	}
	trimOverall(groups, limit)
	res := &tool.Result{}
	// A filtered list hides the reviews it skipped, so advancing the cursor
	// past them would lose them for good.
	filtered := len(in.Rating) > 0 || len(in.Territory) > 0 || in.Unanswered
	if in.MarkSeen && filtered {
		res.Warn("mark_seen ignored: rating/territory/unanswered filters hide reviews, so the cursor was kept; rerun without filters to mark seen")
	}
	total, unanswered := 0, 0
	for i := range groups {
		g := &groups[i]
		total += len(g.Reviews)
		for _, r := range g.Reviews {
			if r.Response == nil {
				unanswered++
			}
		}
		if in.MarkSeen && !filtered && len(g.Reviews) > 0 {
			// Advancing past reviews that were not returned would lose them,
			// so the cursor stays put unless limit is already the maximum.
			if g.More && limit < listMax {
				res.Warn(fmt.Sprintf("app %s: more unseen reviews; cursor kept", g.App.ID))
				res.Then(tool.NextStep{Tool: "reviews_list", Args: map[string]any{
					"app": g.App.ID, "since": lastSeen, "mark_seen": true, "limit": listMax,
				}})
				continue
			}
			newest := g.Reviews[0]
			cur, err := advanceCursor(env, g.App.ID, newest.created, newest.Created)
			if err != nil {
				return nil, err
			}
			g.Cursor = cur
			if g.More {
				res.Warn(fmt.Sprintf("app %s: cursor advanced past reviews that were not returned; raise limit to see them", g.App.ID))
			}
		}
	}
	data := map[string]any{"returned": total, "mark_seen": in.MarkSeen}
	if window != nil {
		data["window"] = window
	}
	if summary != nil {
		data["summary"] = summary
	}
	if len(groups) > 1 || strings.EqualFold(string(in.App), allApps) {
		// A fan-out lists only apps with reviews, an error or more pages,
		// keeping the result small. apps_scanned counts the apps /v1/apps
		// returned (as insights_summary does); apps_with_reviews those with
		// a matching review before the overall limit trimmed the list.
		groups = slices.DeleteFunc(groups, func(g appReviews) bool { return len(g.Reviews) == 0 && g.Error == nil && !g.More })
		data["apps_scanned"], data["apps_with_reviews"] = len(apps), withReviews
	}
	data["apps"] = sortGroups(groups)
	res.Data = data
	if in.Since == lastSeen && noCursor && !in.MarkSeen {
		data["note"] = fmt.Sprintf("no last_seen cursor yet; showing newest %d. Pass mark_seen:true to start tracking", total)
		args := map[string]any{"since": lastSeen, "mark_seen": true}
		if in.App != "" {
			args["app"] = string(in.App)
		}
		res.Then(tool.NextStep{Tool: "reviews_list", Args: args})
	}
	if unanswered > 0 {
		res.Then(tool.NextStep{Tool: "reviews_reply", Args: map[string]any{"replies": []map[string]any{{"review_id": "<id>", "body": "<reply>"}}}})
	}
	return res, nil
}

// filterQuery builds the Apple filters shared by every app.
func filterQuery(in listIn) (url.Values, error) {
	q := url.Values{"sort": {"-createdDate"}}
	if len(in.Rating) > 0 {
		rs := make([]string, 0, len(in.Rating))
		for _, r := range in.Rating {
			if r < 1 || r > 5 {
				return nil, fmt.Errorf("%w: rating %d is not 1-5", tool.ErrInvalidArgs, r)
			}
			rs = append(rs, strconv.Itoa(r))
		}
		q.Set("filter[rating]", strings.Join(rs, ","))
	}
	if len(in.Territory) > 0 {
		ts := make([]string, 0, len(in.Territory))
		for _, t := range in.Territory {
			ts = append(ts, strings.ToUpper(strings.TrimSpace(t)))
		}
		q.Set("filter[territory]", strings.Join(ts, ","))
	}
	if in.Unanswered {
		q.Set("exists[publishedResponse]", "false")
	}
	return q, nil
}

// targetApps resolves one app, or lists every app for a fan-out. An implicit
// fan-out (no app, no default_app) over more than implicitFanOutMax apps is
// refused: app:"all" opts in.
func targetApps(ctx context.Context, env *tool.Env, ref tool.AppRef) ([]appRef, error) {
	if !strings.EqualFold(string(ref), allApps) && (ref != "" || env.Profile.DefaultApp != "") {
		app, err := env.Resolve.App(ctx, ref)
		if err != nil {
			return nil, err
		}
		return []appRef{{ID: app.ID, Name: tool.Untrusted(app.Name), BundleID: app.BundleID}}, nil
	}
	res, err := env.ASC.List(ctx, "/v1/apps", tool.ListOptions{
		Fields: map[string][]string{"apps": {"name", "bundleId"}}, Limit: pageMax, MaxItems: 5000,
	})
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	if ref == "" && len(res.Data) > implicitFanOutMax {
		return nil, tool.WithNext(tool.InvalidArgs("%d apps: pass app, or app:\"all\" to fan out", len(res.Data)),
			tool.NextStep{Tool: "apps_list"})
	}
	out := make([]appRef, 0, len(res.Data))
	for _, a := range res.Data {
		out = append(out, appRef{ID: a.ID, Name: a.Text("name"), BundleID: a.Str("bundleId")})
	}
	return out, nil
}

// listApp pages one app's reviews newest first, stopping at the first review
// not newer than since so nothing older is fetched. Reviews at or after a
// non-zero until are skipped without counting toward limit; at most
// windowPagesMax pages are read while skipping them.
func listApp(ctx context.Context, env *tool.Env, app appRef, query url.Values, sinceArg string, since, until time.Time, limit int) appReviews {
	g := appReviews{App: app, Reviews: []reviewOut{}}
	fail := func(err error) appReviews {
		g.Error, g.errCause = tool.BodyFor(err), err
		return g
	}
	stored, err := env.State.Cursor(env.Profile.Name, app.ID, cursorName)
	if err != nil {
		return fail(fmt.Errorf("read reviews cursor: %w", err))
	}
	g.Cursor = stored
	if sinceArg == lastSeen && stored != "" {
		t, err := tool.ParseInstant(stored)
		if err != nil {
			return fail(fmt.Errorf("stored reviews cursor %q: %w", stored, err))
		}
		since = t
	}
	if !since.IsZero() {
		g.Since = since.Format(time.RFC3339)
	}
	// Page by hand, one request per page, so the since check stops paging
	// before any older page is fetched.
	path, q := "/v1/apps/"+app.ID+"/customerReviews", readQuery()
	for k, vs := range query {
		q[k] = vs
	}
	pageSize := min(limit, pageMax)
	if !until.IsZero() {
		// Reviews newer than the window are skipped: read full pages.
		pageSize = pageMax
	}
	q.Set("limit", strconv.Itoa(pageSize))
	visited := map[string]bool{}
	for pages := 1; ; pages++ {
		res, err := env.ASC.Get(ctx, path, q)
		if err != nil {
			return fail(fmt.Errorf("list reviews of app %s: %w", app.ID, err))
		}
		responses := responsesByID(res.Included)
		for _, r := range res.Data {
			rv := toReview(r, responses)
			if !until.IsZero() && !rv.created.Before(until) {
				continue
			}
			if !since.IsZero() && !rv.created.After(since) {
				return g
			}
			if len(g.Reviews) == limit {
				g.More = true
				return g
			}
			g.Reviews = append(g.Reviews, rv)
		}
		next := res.Links["next"]
		if next == "" {
			return g
		}
		if len(g.Reviews) >= limit || pages >= windowPagesMax {
			g.More = true
			return g
		}
		u, err := url.Parse(next)
		if err != nil || visited[next] || u.Path != path {
			return fail(fmt.Errorf("list reviews of app %s: bad or repeated next link", app.ID))
		}
		visited[next] = true
		q = u.Query()
	}
}

// trimOverall keeps the limit newest reviews across all apps.
func trimOverall(groups []appReviews, limit int) {
	var all []time.Time
	for _, g := range groups {
		for _, r := range g.Reviews {
			all = append(all, r.created)
		}
	}
	if len(all) <= limit {
		return
	}
	slices.SortFunc(all, func(a, b time.Time) int { return b.Compare(a) })
	cut := all[limit-1]
	ties := limit // ties at the cut are kept in group order up to the cap
	for _, t := range all {
		if t.After(cut) {
			ties--
		}
	}
	for i := range groups {
		g := &groups[i]
		n := 0
		for _, r := range g.Reviews {
			keep := r.created.After(cut)
			if !keep && r.created.Equal(cut) && ties > 0 {
				keep = true
				ties--
			}
			if keep {
				g.Reviews[n] = r
				n++
			}
		}
		if n < len(g.Reviews) {
			g.More = true
		}
		g.Reviews = g.Reviews[:n]
	}
}

// sortGroups returns groups with the most recent review first; groups without
// reviews (or with errors) go last, ties by name then ID.
func sortGroups(groups []appReviews) []appReviews {
	newest := func(g appReviews) time.Time {
		if len(g.Reviews) == 0 {
			return time.Time{}
		}
		return g.Reviews[0].created
	}
	slices.SortStableFunc(groups, func(a, b appReviews) int {
		return cmp.Or(newest(b).Compare(newest(a)), cmp.Compare(a.App.Name, b.App.Name), cmp.Compare(a.App.ID, b.App.ID))
	})
	return groups
}
