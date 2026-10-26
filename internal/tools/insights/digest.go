package insights

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/reports"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	digestLimitDefault = 20
	digestLimitMax     = 100
	digestAllMax       = 10
	digestFanOut       = 4
	digestAllApps      = "all"
	digestSinceDefault = 7
	digestSinceMax     = 90
	digestSrcRev       = "reviews"
	digestSrcFb        = "feedback"
	digestSrcState     = "states"
	digestSrcBuild     = "builds"
	digestSrcSales     = "sales"
	// Source statuses.
	digestOK          = "ok"
	digestError       = "error"
	digestUnavailable = "unavailable"
	digestPending     = "pending"
)

// digestScanMax caps how many unseen items one source buffers while paging
// newest-first back to its watermark; digestCountMax caps how many more
// unseen items past that window are counted. Variables for tests.
var (
	digestScanMax  = 500
	digestCountMax = 5000
)

var digestAllSources = []string{digestSrcRev, digestSrcFb, digestSrcState, digestSrcBuild, digestSrcSales}

// digestFeedbackKinds are the TestFlight feedback collections; each keeps its
// own watermark under "feedback_<name>".
var digestFeedbackKinds = []struct{ name, resource string }{
	{"screenshots", "betaFeedbackScreenshotSubmissions"},
	{"crashes", "betaFeedbackCrashSubmissions"},
}

type digestIn struct {
	App            tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; \"all\" = up to 10 apps"`
	Sources        []string    `json:"sources,omitempty" jsonschema:"sources to read (default all)"`
	LimitPerSource int         `json:"limit_per_source,omitempty" jsonschema:"items per source (default 20, max 100)"`
	MarkSeen       bool        `json:"mark_seen,omitempty" jsonschema:"advance each source to the last item returned"`
	AcceptGap      bool        `json:"accept_gap,omitempty" jsonschema:"let mark_seen skip unseen items beyond the 500-item window"`
	SinceDays      int         `json:"since_days,omitempty" jsonschema:"days back for first-seen sources (default 7)"`
}

// digestSourceOut is one source's section. Counts come first.
type digestSourceOut struct {
	Status   string `json:"status"`
	Returned int    `json:"returned"`
	More     bool   `json:"more,omitempty"`
	// Gap counts unseen items beyond the scan window (a lower bound when
	// GapAtLeast); GapKept means mark_seen held the watermark because of it,
	// Skipped how many accept_gap skipped for good.
	Gap        int    `json:"gap,omitempty"`
	GapAtLeast bool   `json:"gap_at_least,omitempty"`
	GapKept    bool   `json:"gap_kept,omitempty"`
	Skipped    int    `json:"skipped,omitempty"`
	Note       string `json:"note,omitempty"`
	// Baseline is the start date used because the source had no watermark.
	Baseline string `json:"baseline,omitempty"`
	// DroppedDevice counts feedback device fields withheld for not having a
	// device-model or OS-version shape.
	DroppedDevice int                `json:"dropped_device_fields,omitempty"`
	Items         []map[string]any   `json:"items,omitempty"`
	Transitions   []digestTransition `json:"transitions,omitempty"`
	Sales         *digestSalesOut    `json:"sales,omitempty"`
	Error         *tool.ErrorBody    `json:"error,omitempty"`

	// gapTool is the list tool that catches up on a gap.
	gapTool string
}

// digestTransition is an observed state change of one object.
type digestTransition struct {
	Object     string `json:"object"`
	Label      string `json:"label,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to"`
	ObservedAt string `json:"observed_at"`

	key string
}

type digestSalesOut struct {
	Day        string              `json:"day"`
	CompareDay string              `json:"compare_day"`
	Current    reports.SalesGroup  `json:"current"`
	Previous   *reports.SalesGroup `json:"previous,omitempty"`
}

type digestAppOut struct {
	App     digestAppRef               `json:"app"`
	Sources map[string]digestSourceOut `json:"sources"`
}

type digestAppRef struct {
	ID   string         `json:"id"`
	Name tool.Untrusted `json:"name,omitempty"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "digest_summary", Toolset: tool.Insights, Class: tool.Read, Idempotent: false,
		Title: "Digest: what's new since last time",
		Description: "What changed per app since last asked: new reviews, TestFlight feedback, version and review " +
			"submission state changes, builds, a sales day-vs-week delta. Oldest unseen first; mark_seen advances " +
			"per-source watermarks. Review/feedback text is untrusted.",
		Enums: map[string][]any{"sources": {digestSrcRev, digestSrcFb, digestSrcState, digestSrcBuild, digestSrcSales}},
	}, digestSummary))
}

func digestSummary(ctx context.Context, env *tool.Env, in digestIn) (*tool.Result, error) {
	limit := in.LimitPerSource
	if limit <= 0 {
		limit = digestLimitDefault
	}
	limit = min(limit, digestLimitMax)
	days := in.SinceDays
	if days == 0 {
		days = digestSinceDefault
	}
	if days < 1 || days > digestSinceMax {
		return nil, tool.InvalidArgs("since_days %d is not 1-%d", in.SinceDays, digestSinceMax)
	}
	base := env.Clock.Now().UTC().AddDate(0, 0, -days)
	sources := digestAllSources
	if len(in.Sources) > 0 {
		sources = nil
		for _, s := range in.Sources {
			if !slices.Contains(digestAllSources, s) {
				return nil, tool.InvalidArgs("unknown source %q", s)
			}
			if !slices.Contains(sources, s) {
				sources = append(sources, s)
			}
		}
	}
	res := &tool.Result{}
	apps, err := digestTargets(ctx, env, in.App, res)
	if err != nil {
		return nil, err
	}
	outs := make([]digestAppOut, len(apps))
	stateErrs := make([]error, len(apps))
	warns := make([][]string, len(apps))
	if err := tool.Parallel(ctx, digestFanOut, apps, func(ctx context.Context, i int, app tool.App) error {
		outs[i], warns[i], stateErrs[i] = digestApp(ctx, env, app, sources, limit, in.MarkSeen, in.AcceptGap, base)
		return nil
	}); err != nil {
		return nil, err
	}
	stateDown := false
	var fresh []string
	for i := range apps {
		for _, w := range warns[i] {
			res.Warn(w)
		}
		if stateErrs[i] != nil && !stateDown {
			stateDown = true
			msg := "state store unavailable (" + stateErrs[i].Error() + "): showing the baseline window without watermarks"
			if in.MarkSeen {
				msg += "; mark_seen was not saved"
			}
			res.Warn(msg)
		}
		for _, s := range sources {
			if outs[i].Sources[s].Baseline != "" && !slices.Contains(fresh, s) {
				fresh = append(fresh, s)
			}
		}
	}
	if len(fresh) > 0 {
		res.Warn(fmt.Sprintf("no watermark yet for %s: started at baseline %s (since_days %d); older items are not shown",
			strings.Join(fresh, ", "), base.Format(time.DateOnly), days))
	}
	digestNext(res, outs, in, stateDown)
	res.Data = map[string]any{"mark_seen": in.MarkSeen && !stateDown, "limit_per_source": limit, "apps": outs}
	return res, nil
}

// digestTargets resolves one app, or up to digestAllMax apps for "all".
func digestTargets(ctx context.Context, env *tool.Env, ref tool.AppRef, res *tool.Result) ([]tool.App, error) {
	if !strings.EqualFold(string(ref), digestAllApps) {
		app, err := env.Resolve.App(ctx, ref)
		if err != nil {
			return nil, err
		}
		return []tool.App{app}, nil
	}
	list, err := env.ASC.List(ctx, "/v1/apps", tool.ListOptions{
		Fields: map[string][]string{"apps": {"name", "bundleId", "sku"}}, Limit: 200, MaxItems: 200,
	})
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	apps := make([]tool.App, 0, min(len(list.Data), digestAllMax))
	for _, a := range list.Data {
		apps = append(apps, tool.App{ID: a.ID, Name: a.Str("name"), BundleID: a.Str("bundleId"), SKU: a.Str("sku")}) // trusted: tool.App holds the raw name; digestAppRef wraps it on output
	}
	if len(apps) > digestAllMax {
		res.Warn(fmt.Sprintf("app \"all\" is capped at %d of %d apps; pass app for the others", digestAllMax, len(apps)))
		apps = apps[:digestAllMax]
	}
	return apps, nil
}

// digestApp reads every source of one app. A failing source keeps its
// watermark; the state is saved only with mark_seen and a working store.
func digestApp(ctx context.Context, env *tool.Env, app tool.App, sources []string, limit int, markSeen, acceptGap bool, base time.Time) (digestAppOut, []string, error) {
	out := digestAppOut{App: digestAppRef{ID: app.ID, Name: tool.Untrusted(app.Name)}, Sources: map[string]digestSourceOut{}}
	key := digestStateKey(env.Profile.Name, app.ID)
	unlock := digestLock(key)
	defer unlock()
	st, stateErr := digestLoad(env, key)
	next := digestState{Sources: maps.Clone(st.Sources), Snapshot: maps.Clone(st.Snapshot)}
	now := env.Clock.Now().UTC()
	var warns []string
	for _, s := range sources {
		var so digestSourceOut
		switch s {
		case digestSrcRev:
			so = digestReviews(ctx, env, app, st, &next, limit, acceptGap, base)
		case digestSrcFb:
			so = digestFeedback(ctx, env, app, st, &next, limit, acceptGap, base)
		case digestSrcState:
			so = digestStates(ctx, env, app, st, &next, limit, now, base)
		case digestSrcBuild:
			so = digestBuilds(ctx, env, app, st, &next, limit, now, acceptGap, base)
		case digestSrcSales:
			so = digestSales(ctx, env, app, now)
		}
		if so.Status == digestError {
			warns = append(warns, fmt.Sprintf("app %s: source %s failed; its watermark was kept", app.ID, s))
		}
		if so.Gap > 0 {
			size := strconv.Itoa(so.Gap)
			if so.GapAtLeast {
				size += "+"
			}
			switch {
			case !markSeen:
				warns = append(warns, fmt.Sprintf("app %s: source %s has %s unseen items beyond the %d-item window; catch up with %s", app.ID, s, size, digestScanMax, so.gapTool))
			case so.Skipped > 0:
				warns = append(warns, fmt.Sprintf("app %s: source %s: accept_gap skipped %s unseen items beyond the %d-item window", app.ID, s, size, digestScanMax))
			default:
				warns = append(warns, fmt.Sprintf("app %s: source %s has %s unseen items beyond the %d-item window; watermark kept. Catch up with %s, or pass accept_gap:true to skip them", app.ID, s, size, digestScanMax, so.gapTool))
			}
		}
		out.Sources[s] = so
	}
	if markSeen && stateErr == nil {
		// Another call (or process) may have saved since st was loaded:
		// apply only this call's changes onto the latest record.
		latest, err := digestLoad(env, key)
		if err != nil {
			return out, warns, err
		}
		digestMerge(&latest, st, next)
		latest.UpdatedAt = now.Format(time.RFC3339)
		if err := env.State.Save(digestStateKind, key, latest); err != nil {
			return out, warns, err
		}
	}
	return out, warns, stateErr
}

// digestLocks serialises load→compute→save of one profile+app record within
// the process.
var digestLocks sync.Map // key → *sync.Mutex

func digestLock(key string) func() {
	m, _ := digestLocks.LoadOrStore(key, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// digestLoad reads the record of key with non-nil maps and v0.5.0
// watermarks upgraded; on a store error the record is empty.
func digestLoad(env *tool.Env, key string) (digestState, error) {
	var st digestState
	if _, err := env.State.Load(digestStateKind, key, &st); err != nil {
		return digestState{Sources: map[string]digestWatermark{}, Snapshot: map[string]string{}}, err
	}
	if st.Sources == nil {
		st.Sources = map[string]digestWatermark{}
	}
	if st.Snapshot == nil {
		st.Snapshot = map[string]string{}
	}
	digestUpgrade(&st)
	return st, nil
}

// digestMerge applies the changes from st to next (the sources this call
// touched) onto latest, leaving every other source as latest has it.
func digestMerge(latest *digestState, st, next digestState) {
	for k, w := range next.Sources {
		if old, ok := st.Sources[k]; !ok || !reflect.DeepEqual(old, w) {
			latest.Sources[k] = w
		}
	}
	for k, v := range next.Snapshot {
		if old, ok := st.Snapshot[k]; !ok || old != v {
			latest.Snapshot[k] = v
		}
	}
	for k := range st.Snapshot {
		if _, ok := next.Snapshot[k]; !ok {
			delete(latest.Snapshot, k)
		}
	}
}

func digestFail(err error) digestSourceOut {
	b := tool.BodyFor(err)
	return digestSourceOut{Status: digestError, Error: &b}
}

// digestScanResult is a newest-first scan back to a watermark: the buffered
// items and how many more unseen items lie beyond the buffer (Beyond, a
// lower bound when BeyondCapped).
type digestScanResult struct {
	Items        []tool.Resource
	Beyond       int
	BeyondCapped bool
}

// digestScan pages path newest-first down to w's floor (TS − 72h, or the
// fresh baseline), skipping ids already seen, buffering at most
// digestScanMax unseen items and counting (up to digestCountMax) the unseen
// items past the buffer.
func digestScan(ctx context.Context, env *tool.Env, path string, q url.Values, tsField string, w digestWatermark) (digestScanResult, error) {
	floor := w.floor()
	var out digestScanResult
	visited := map[string]bool{}
	for {
		res, err := env.ASC.Get(ctx, path, q)
		if err != nil {
			return digestScanResult{}, err
		}
		for _, r := range res.Data {
			if digestParseTime(r.Str(tsField)).Before(floor) {
				return out, nil
			}
			switch {
			case digestSeen(w, r.ID):
			case len(out.Items) < digestScanMax:
				out.Items = append(out.Items, r)
			case out.Beyond >= digestCountMax:
				out.BeyondCapped = true
				return out, nil
			default:
				out.Beyond++
			}
		}
		next := res.Links["next"]
		if next == "" {
			return out, nil
		}
		u, err := url.Parse(next)
		if err != nil || visited[next] || u.Path != path {
			return digestScanResult{}, fmt.Errorf("%s: bad or repeated next link", path)
		}
		visited[next] = true
		q = u.Query()
	}
}

// digestPicked is what one watermarked collection contributed.
type digestPicked struct {
	Items []tool.Resource
	More  bool
	// Gap counts unseen items beyond the scan window; Kept means the
	// watermark was held because of it, Skipped how many accept_gap dropped.
	Gap     int
	Kept    bool
	Skipped int
}

// digestPick selects the resources unseen by w oldest first and advances the
// next-state watermark of name to the last one returned. With unseen items
// beyond the scan window the watermark is held unless acceptGap, since
// advancing would skip them for good.
func digestPick(scan digestScanResult, tsField, name string, w digestWatermark, next *digestState, limit int, acceptGap bool) digestPicked {
	rs := scan.Items
	keyed := make([]digestKeyed, len(rs))
	for i, r := range rs {
		keyed[i] = digestKeyed{ID: r.ID, TS: digestParseTime(r.Str(tsField)), Index: i}
	}
	sel, more := digestSelect(keyed, w, limit)
	p := digestPicked{Items: make([]tool.Resource, len(sel)), More: more, Gap: scan.Beyond}
	for i, k := range sel {
		p.Items[i] = rs[k.Index]
	}
	if scan.Beyond > 0 && !acceptGap {
		p.Kept, p.More = true, true
		return p
	}
	if scan.Beyond > 0 {
		p.Skipped = scan.Beyond
	}
	if w := digestAdvance(w, sel); w.TS != "" {
		if scan.Beyond > 0 {
			// accept_gap: the skipped items lie below the new watermark and
			// must not come back through the overlap window.
			w.Floor, _ = w.time()
		}
		next.Sources[name] = w
	}
	return p
}

// digestApplyPick folds p into so.
func digestApplyPick(so *digestSourceOut, p digestPicked, capped bool) {
	so.More = so.More || p.More
	so.Gap += p.Gap
	so.Skipped += p.Skipped
	so.GapKept = so.GapKept || p.Kept
	so.GapAtLeast = so.GapAtLeast || capped
}

func digestReviews(ctx context.Context, env *tool.Env, app tool.App, st digestState, next *digestState, limit int, acceptGap bool, base time.Time) digestSourceOut {
	q := url.Values{
		"sort":                    {"-createdDate"},
		"limit":                   {"200"},
		"fields[customerReviews]": {"rating,title,body,reviewerNickname,createdDate,territory"},
	}
	w, fresh := digestStart(st, digestSrcRev, base)
	scan, err := digestScan(ctx, env, "/v1/apps/"+url.PathEscape(app.ID)+"/customerReviews", q, "createdDate", w)
	if err != nil {
		return digestFail(fmt.Errorf("customer reviews: %w", err))
	}
	p := digestPick(scan, "createdDate", digestSrcRev, w, next, limit, acceptGap)
	so := digestSourceOut{Status: digestOK, Returned: len(p.Items), Items: []map[string]any{}, gapTool: "reviews_list"}
	if fresh {
		so.Baseline = base.Format(time.DateOnly)
	}
	digestApplyPick(&so, p, scan.BeyondCapped)
	for _, r := range p.Items {
		rating, _ := r.Int("rating")
		so.Items = append(so.Items, map[string]any{
			"id": r.ID, "created": r.Str("createdDate"), "rating": rating, "territory": r.Str("territory"),
			"title": r.Text("title"), "body": r.Text("body"),
			"nickname": r.Text("reviewerNickname"),
		})
	}
	return so
}

func digestFeedback(ctx context.Context, env *tool.Env, app tool.App, st digestState, next *digestState, limit int, acceptGap bool, base time.Time) digestSourceOut {
	so := digestSourceOut{Status: digestOK, Items: []map[string]any{}, gapTool: "testflight_feedback_list"}
	failed := 0
	var errs []error
	for _, k := range digestFeedbackKinds {
		name := digestSrcFb + "_" + k.name
		fields := "createdDate,comment,deviceModel,osVersion,appPlatform"
		if k.name == "screenshots" {
			fields += ",screenshots"
		}
		q := url.Values{"sort": {"-createdDate"}, "limit": {"100"}, "fields[" + k.resource + "]": {fields}}
		w, fresh := digestStart(st, name, base)
		if fresh {
			so.Baseline = base.Format(time.DateOnly)
		}
		scan, err := digestScan(ctx, env, "/v1/apps/"+url.PathEscape(app.ID)+"/"+k.resource, q, "createdDate", w)
		if err != nil {
			failed++
			errs = append(errs, fmt.Errorf("%s feedback: %w", k.name, err))
			continue
		}
		p := digestPick(scan, "createdDate", name, w, next, limit, acceptGap)
		digestApplyPick(&so, p, scan.BeyondCapped)
		for _, r := range p.Items {
			device, d1 := tool.DeviceField(tool.DeviceModel, r.Str("deviceModel")) // trusted: shape-checked by DeviceField
			osv, d2 := tool.DeviceField(tool.OSVersion, r.Str("osVersion"))        // trusted: shape-checked by DeviceField
			if d1 {
				so.DroppedDevice++
			}
			if d2 {
				so.DroppedDevice++
			}
			item := map[string]any{
				"id": r.ID, "kind": k.name, "created": r.Str("createdDate"),
				"device": device, "os": osv, "platform": r.Str("appPlatform"),
				"comment": r.Text("comment"),
			}
			if shots, ok := tool.Unwrap(r.Attributes["screenshots"]).([]any); ok {
				item["screenshot_count"] = len(shots)
			}
			so.Items = append(so.Items, item)
		}
	}
	so.Returned = len(so.Items)
	if failed > 0 {
		b := tool.BodyFor(errors.Join(errs...))
		so.Error = &b
		so.Status = digestError
		if failed < len(digestFeedbackKinds) {
			so.Note = "partial: one feedback kind failed"
		}
	}
	return so
}

// digestDiff turns current object states into transitions against the
// snapshot, capped at limit; listed transitions update next.Snapshot.
func digestDiff(cur []digestTransition, st digestState, next *digestState, limit int, so *digestSourceOut) {
	var changed []digestTransition
	for _, t := range cur {
		if prev, ok := st.Snapshot[t.key]; ok && prev == t.To {
			continue
		}
		t.From = st.Snapshot[t.key]
		changed = append(changed, t)
	}
	slices.SortFunc(changed, func(a, b digestTransition) int { return strings.Compare(a.key, b.key) })
	if len(changed) > limit {
		changed, so.More = changed[:limit], true
	}
	for _, t := range changed {
		next.Snapshot[t.key] = t.To
	}
	so.Transitions = append(so.Transitions, changed...)
	so.Returned = len(so.Transitions)
}

// digestStates diffs version and review-submission states against the
// snapshot. On a first digest (no "states" marker) objects dated before base
// are recorded silently instead of being reported as new.
func digestStates(ctx context.Context, env *tool.Env, app tool.App, st digestState, next *digestState, limit int, now, base time.Time) digestSourceOut {
	at := now.Format(time.RFC3339)
	_, fresh := digestStart(st, digestSrcState, base)
	vs, err := env.ASC.Get(ctx, "/v1/apps/"+url.PathEscape(app.ID)+"/appStoreVersions", url.Values{
		"fields[appStoreVersions]": {"platform,versionString,appVersionState,createdDate"},
		"limit":                    {"20"},
	})
	if err != nil {
		return digestFail(fmt.Errorf("app store versions: %w", err))
	}
	subs, err := env.ASC.Get(ctx, "/v1/reviewSubmissions", url.Values{
		"filter[app]":               {app.ID},
		"fields[reviewSubmissions]": {"platform,state,submittedDate"},
		"limit":                     {"20"},
	})
	if err != nil {
		return digestFail(fmt.Errorf("review submissions: %w", err))
	}
	var cur []digestTransition
	// old reports whether a dated object predates the baseline of a first
	// digest; undated objects are always reported.
	old := func(date string) bool {
		t := digestParseTime(date)
		return fresh && !t.IsZero() && t.Before(base)
	}
	add := func(t digestTransition, date string) {
		if old(date) {
			next.Snapshot[t.key] = t.To
			return
		}
		cur = append(cur, t)
	}
	for _, v := range vs.Data {
		add(digestTransition{
			key: "appStoreVersions/" + v.ID, Object: "appStoreVersions/" + v.ID,
			Label: strings.TrimSpace(v.Str("platform") + " " + v.Str("versionString")), To: v.Str("appVersionState"), ObservedAt: at,
		}, v.Str("createdDate"))
	}
	for _, s := range subs.Data {
		add(digestTransition{
			key: "reviewSubmissions/" + s.ID, Object: "reviewSubmissions/" + s.ID,
			Label: strings.TrimSpace(s.Str("platform") + " " + s.Str("submittedDate")), To: s.Str("state"), ObservedAt: at,
		}, s.Str("submittedDate"))
	}
	so := digestSourceOut{Status: digestOK}
	if fresh {
		so.Baseline = base.Format(time.DateOnly)
		next.Sources[digestSrcState] = digestWatermark{TS: now.Format(time.RFC3339Nano)}
	}
	digestDiff(cur, st, next, limit, &so)
	return so
}

// digestBuildChunk is how many pending build ids one filter[id] GET carries.
const digestBuildChunk = 50

// digestBuildTerminal are the processing states a build never leaves.
var digestBuildTerminal = []string{"VALID", "INVALID", "FAILED"}

func digestBuilds(ctx context.Context, env *tool.Env, app tool.App, st digestState, next *digestState, limit int, now time.Time, acceptGap bool, base time.Time) digestSourceOut {
	// Builds reported while not yet terminal are re-read by id on every
	// call, independent of the watermark, so a late transition is never
	// missed. Terminal entries (from older records) are just dropped.
	var pending []string
	for k, state := range st.Snapshot {
		id, ok := strings.CutPrefix(k, "builds/")
		switch {
		case !ok:
		case slices.Contains(digestBuildTerminal, state):
			delete(next.Snapshot, k)
		default:
			pending = append(pending, id)
		}
	}
	slices.Sort(pending)
	at := now.Format(time.RFC3339)
	var cur []digestTransition
	for chunk := range slices.Chunk(pending, digestBuildChunk) {
		res, err := env.ASC.Get(ctx, "/v1/builds", url.Values{
			"filter[id]":     {strings.Join(chunk, ",")},
			"fields[builds]": {"processingState,version"},
			"limit":          {strconv.Itoa(digestBuildChunk)},
		})
		if err != nil {
			return digestFail(fmt.Errorf("pending builds: %w", err))
		}
		found := map[string]bool{}
		for _, r := range res.Data {
			found[r.ID] = true
			k := "builds/" + r.ID
			cur = append(cur, digestTransition{key: k, Object: k, Label: "build " + r.Str("version"), To: r.Str("processingState"), ObservedAt: at})
		}
		for _, id := range chunk {
			if !found[id] {
				delete(next.Snapshot, "builds/"+id)
			}
		}
	}
	q := url.Values{
		"filter[app]":    {app.ID},
		"sort":           {"-uploadedDate"},
		"limit":          {"200"},
		"fields[builds]": {"version,uploadedDate,processingState,expired"},
	}
	w, fresh := digestStart(st, digestSrcBuild, base)
	scan, err := digestScan(ctx, env, "/v1/builds", q, "uploadedDate", w)
	if err != nil {
		return digestFail(fmt.Errorf("builds: %w", err))
	}
	p := digestPick(scan, "uploadedDate", digestSrcBuild, w, next, limit, acceptGap)
	so := digestSourceOut{Status: digestOK, Items: []map[string]any{}, gapTool: "builds_list"}
	if fresh {
		so.Baseline = base.Format(time.DateOnly)
	}
	digestApplyPick(&so, p, scan.BeyondCapped)
	for _, r := range p.Items {
		state := r.Str("processingState")
		if !slices.Contains(digestBuildTerminal, state) {
			next.Snapshot["builds/"+r.ID] = state
		}
		expired, _ := r.Bool("expired")
		so.Items = append(so.Items, map[string]any{
			"id": r.ID, "version": r.Str("version"), "uploaded": r.Str("uploadedDate"),
			"processing_state": state, "expired": expired,
		})
	}
	newCount := len(so.Items)
	digestDiff(cur, st, next, limit, &so)
	for _, t := range so.Transitions {
		if slices.Contains(digestBuildTerminal, t.To) {
			delete(next.Snapshot, t.key)
		}
	}
	so.Returned += newCount
	return so
}

// digestSales compares the last complete Pacific day with the same weekday a
// week earlier. It has no watermark.
func digestSales(ctx context.Context, env *tool.Env, app tool.App, now time.Time) digestSourceOut {
	if env.Profile.VendorNumber == "" {
		return digestSourceOut{Status: digestUnavailable, Note: "no vendor number: set vendor_number in the profile (or ASC_VENDOR_NUMBER) to include sales"}
	}
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		loc = time.FixedZone("PT", -8*3600)
	}
	day := now.In(loc).AddDate(0, 0, -1).Format(time.DateOnly)
	prevDay := now.In(loc).AddDate(0, 0, -8).Format(time.DateOnly)
	cur, pending, err := digestSalesDay(ctx, env, app, day)
	if err != nil {
		return digestFail(err)
	}
	if pending {
		return digestSourceOut{Status: digestPending, Note: "sales report for " + day + " is not available yet"}
	}
	so := digestSourceOut{Status: digestOK, Sales: &digestSalesOut{Day: day, CompareDay: prevDay, Current: cur}}
	prev, pending, err := digestSalesDay(ctx, env, app, prevDay)
	switch {
	case err != nil:
		so.Note = "comparison day failed: " + err.Error()
	case pending:
		so.Note = "comparison day " + prevDay + " not available"
	default:
		so.Sales.Previous = &prev
	}
	return so
}

func digestSalesDay(ctx context.Context, env *tool.Env, app tool.App, day string) (reports.SalesGroup, bool, error) {
	body, _, err := env.ASC.Download(ctx, tool.Request{Method: http.MethodGet, Path: "/v1/salesReports", Accept: "application/a-gzip", Query: url.Values{
		"filter[vendorNumber]":  {env.Profile.VendorNumber},
		"filter[reportType]":    {"SALES"},
		"filter[reportSubType]": {"SUMMARY"},
		"filter[frequency]":     {"DAILY"},
		"filter[reportDate]":    {day},
		"filter[version]":       {"1_0"},
	}})
	if err != nil {
		if tool.IsNotFound(err) {
			return reports.SalesGroup{}, true, nil
		}
		return reports.SalesGroup{}, false, fmt.Errorf("sales report %s: %w", day, err)
	}
	t, err := reports.Decode(bytes.NewReader(body), reports.DefaultLimits())
	if err != nil {
		return reports.SalesGroup{}, false, fmt.Errorf("decode sales report %s: %w", day, err)
	}
	rows, err := reports.SalesRows(t)
	if err != nil {
		return reports.SalesGroup{}, false, fmt.Errorf("sales report %s: %w", day, err)
	}
	mine := rows[:0]
	for _, r := range rows {
		if r.AppleID == app.ID || (app.SKU != "" && r.ParentID == app.SKU) {
			mine = append(mine, r)
		}
	}
	return reports.AggregateSales(mine, nil, 0, nil).Totals, false, nil
}

// digestNext suggests the detail tools for sources with news and another
// digest call when a source has more or nothing was marked seen.
func digestNext(res *tool.Result, outs []digestAppOut, in digestIn, stateDown bool) {
	var rev, fb, states, more, news bool
	var gapTools []string
	for _, o := range outs {
		for name, s := range o.Sources {
			news = news || s.Returned > 0
			// A held gap repeats the same items until caught up, so it does
			// not ask for another mark_seen call.
			more = more || (s.More && !s.GapKept)
			if s.Gap > 0 && s.Skipped == 0 && !slices.Contains(gapTools, s.gapTool) {
				gapTools = append(gapTools, s.gapTool)
			}
			switch {
			case name == digestSrcRev && s.Returned > 0:
				rev = true
			case name == digestSrcFb && s.Returned > 0:
				fb = true
			case name == digestSrcState && s.Returned > 0:
				states = true
			}
		}
	}
	rev = rev || slices.Contains(gapTools, "reviews_list")
	fb = fb || slices.Contains(gapTools, "testflight_feedback_list")
	app := map[string]any{}
	if in.App != "" {
		app["app"] = string(in.App)
	}
	if rev {
		res.Then(tool.NextStep{Tool: "reviews_list", Args: app})
	}
	if fb {
		res.Then(tool.NextStep{Tool: "testflight_feedback_list", Args: app})
	}
	if states {
		res.Then(tool.NextStep{Tool: "release_review_status", Args: app})
	}
	if slices.Contains(gapTools, "builds_list") {
		res.Then(tool.NextStep{Tool: "builds_list", Args: app})
	}
	if stateDown {
		return
	}
	if (news && !in.MarkSeen) || (more && in.MarkSeen) {
		args := map[string]any{"mark_seen": true}
		for k, v := range app {
			args[k] = v
		}
		if in.LimitPerSource > 0 {
			args["limit_per_source"] = in.LimitPerSource
		}
		res.Then(tool.NextStep{Tool: "digest_summary", Args: args})
	}
}
