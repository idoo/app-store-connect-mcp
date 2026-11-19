package xcodecloud

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// maxUsageRuns caps the started runs of a window that are counted, newest
// first; older ones are left out and the answer says Truncated. It bounds
// the kept record and the calls a busy team's first answers take. A
// variable so a test can lower it.
var maxUsageRuns = 400

// usageRunsPerProduct caps the runs listed of one product (newest first).
// A variable so a test can lower it.
var usageRunsPerProduct = 200

// usageReadsPerCall caps the runs whose actions one call reads when a state
// store keeps them: about 20 s at fanOut reads at once. The rest is read by
// the next call. A variable so a test can lower it.
var usageReadsPerCall = 100

// usageSpansKind is the state record (key: the profile name) of the actions
// read per run: a completed run's actions never change, so each is read once.
const usageSpansKind = "xcc_run_spans"

// usageSpansMu serializes the read-merge-save of the usageSpansKind record.
var usageSpansMu sync.Mutex

// usageCached is one completed run's actions in the usageSpansKind record.
type usageCached struct {
	// Created is the run's createdDate as Apple wrote it; old entries are
	// dropped by it.
	Created string      `json:"created"`
	Spans   []usageSpan `json:"spans"`
}

// usageRunFields are the run attributes the estimate needs.
const usageRunFields = "number,createdDate,startedDate,finishedDate,executionProgress,completionStatus"

// usageRun is one run of the window.
type usageRun struct {
	product int // index into the products read
	id      string
	number  int
	created time.Time
	// createdText and startedText are the dates as Apple wrote them.
	createdText, startedText string
	started                  time.Time
	status                   string
	dead                     bool
	// complete: the run ended, so its actions are final.
	complete bool
}

// usageSpan is one finished action: the UTC day it started and its seconds.
type usageSpan struct {
	Day     string  `json:"day"`
	Seconds float64 `json:"seconds"`
}

// usageFromRuns estimates the compute time from the runs' actions: every
// product of the team (or the one given), their runs created inside the
// window, and each started run's finished actions summed by product and by
// UTC day. Runs that ended without starting are listed apart. With a state
// store the completed runs' actions are kept, and one call reads at most
// usageReadsPerCall runs not kept yet; RunsPending counts the rest.
func usageFromRuns(ctx context.Context, env *tool.Env, days int, now time.Time, prod *productRef) (usageOut, error) {
	if days == 0 {
		days = maxUsageDays
	}
	from, to := usageWindow(now, days)
	out := usageOut{Source: usageRuns, From: usageDate(from), To: usageDate(to)}

	products, err := usageProducts(ctx, env, prod)
	if err != nil {
		return usageOut{}, err
	}
	runs, more, err := usageRunsSince(ctx, env, products, from)
	if err != nil {
		return usageOut{}, err
	}
	out.Truncated = more

	var dead, started []usageRun
	var newest *usageRun
	for i := range runs {
		r := &runs[i]
		if newest == nil || r.created.After(newest.created) {
			newest = r
		}
		switch {
		case r.dead:
			dead = append(dead, *r)
		case r.startedText != "":
			started = append(started, *r)
		}
	}
	out.NotTakingRuns = newest != nil && newest.dead

	slices.SortFunc(dead, func(a, b usageRun) int {
		return cmp.Or(b.created.Compare(a.created), cmp.Compare(a.id, b.id))
	})
	out.DeadRunsTotal = len(dead)
	for _, r := range dead[:min(len(dead), maxDeadRuns)] {
		out.DeadRuns = append(out.DeadRuns, deadRun{
			Product: products[r.product], ID: r.id, Number: r.number, CreatedDate: r.createdText, CompletionStatus: r.status,
		})
	}

	slices.SortFunc(started, func(a, b usageRun) int {
		return cmp.Or(b.started.Compare(a.started), cmp.Compare(a.id, b.id))
	})
	if len(started) > maxUsageRuns {
		started, out.Truncated = started[:maxUsageRuns], true
	}
	if len(started) > 0 {
		out.LastStartedAt = started[0].startedText
	}
	for _, r := range dead {
		if len(started) == 0 || r.created.After(started[0].started) {
			out.DeadRunsSinceStart++
		}
	}

	spans, known, pending, err := usageReadSpans(ctx, env, started, now)
	out.RunsPending = pending
	if err != nil {
		return usageOut{}, err
	}

	// Seconds are summed first and rounded once per product, day and total.
	type sum struct {
		seconds float64
		builds  int
	}
	byProduct := make([]sum, len(products))
	byDay := map[string]*sum{}
	day := func(d string) *sum {
		if byDay[d] == nil {
			byDay[d] = &sum{}
		}
		return byDay[d]
	}
	total := 0.0
	for i, r := range started {
		if !known[i] {
			continue
		}
		out.RunsScanned++
		byProduct[r.product].builds++
		day(usageDate(r.started)).builds++
		for _, s := range spans[i] {
			byProduct[r.product].seconds += s.Seconds
			day(s.Day).seconds += s.Seconds
			total += s.Seconds
		}
	}
	out.Builds = out.RunsScanned
	out.Minutes = usageMinutes(total)
	for i, s := range byProduct {
		if s.builds > 0 {
			out.Products = append(out.Products, usageProduct{
				ID: products[i].ID, Name: products[i].Name, Minutes: usageMinutes(s.seconds), Builds: s.builds,
			})
		}
	}
	slices.SortFunc(out.Products, func(a, b usageProduct) int {
		return cmp.Or(cmp.Compare(b.Minutes, a.Minutes), cmp.Compare(a.ID, b.ID))
	})
	for d, s := range byDay {
		out.Days = append(out.Days, usageDay{Date: d, Minutes: usageMinutes(s.seconds), Builds: s.builds})
	}
	slices.SortFunc(out.Days, func(a, b usageDay) int { return cmp.Compare(a.Date, b.Date) })
	return out, nil
}

// usageMinutes rounds seconds to the nearest minute.
func usageMinutes(seconds float64) int { return int(math.Round(seconds / 60)) }

// usageProducts returns the one product asked for, or every product of the
// team: the plan is the team's, so the pinned app narrows nothing.
func usageProducts(ctx context.Context, env *tool.Env, prod *productRef) ([]productRef, error) {
	if prod != nil {
		return []productRef{*prod}, nil
	}
	res, err := env.ASC.List(ctx, "/v1/ciProducts", tool.ListOptions{
		Query: url.Values{"fields[ciProducts]": {"name"}}, MaxItems: 200,
	})
	if err != nil {
		return nil, fmt.Errorf("list Xcode Cloud products: %w", err)
	}
	out := make([]productRef, 0, len(res.Data))
	for _, r := range res.Data {
		out = append(out, productRef{ID: r.ID, Name: r.Text("name")})
	}
	return out, nil
}

// usageRunsSince reads the newest runs of each product, fanOut products at
// a time, and keeps those created on or after from. more says a product's
// list ended at usageRunsPerProduct with its oldest run still inside the
// window: older runs of the window were not listed.
func usageRunsSince(ctx context.Context, env *tool.Env, products []productRef, from time.Time) (runs []usageRun, more bool, err error) {
	lists := make([][]usageRun, len(products))
	capped := make([]bool, len(products))
	err = tool.Parallel(ctx, fanOut, products, func(ctx context.Context, i int, p productRef) error {
		res, err := env.ASC.List(ctx, "/v1/ciProducts/"+url.PathEscape(p.ID)+"/buildRuns", tool.ListOptions{
			Query: url.Values{"sort": {"-number"}, "fields[ciBuildRuns]": {usageRunFields}}, MaxItems: usageRunsPerProduct,
		})
		if err != nil {
			return fmt.Errorf("list build runs of product %s: %w", p.ID, err)
		}
		for _, r := range res.Data {
			created, err := time.Parse(time.RFC3339, r.Str("createdDate"))
			if err != nil || created.Before(from) {
				continue
			}
			run := usageRun{
				product: i, id: r.ID, created: created, createdText: r.Str("createdDate"), status: r.Str("completionStatus"),
			}
			run.number, _ = r.Int("number")
			run.complete = r.Str("executionProgress") == "COMPLETE"
			run.dead = diedAtStart(r.Str("executionProgress"), run.status, r.Str("startedDate"))
			// A start date that does not parse is no start: the run has no day.
			if t, err := time.Parse(time.RFC3339, r.Str("startedDate")); err == nil {
				run.started, run.startedText = t, r.Str("startedDate")
			}
			lists[i] = append(lists[i], run)
			capped[i] = len(res.Data) >= usageRunsPerProduct && r.ID == res.Data[len(res.Data)-1].ID
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return slices.Concat(lists...), slices.Contains(capped, true), nil
}

// usageReadSpans returns the actions of the started runs: kept ones from
// the state store, the others read now, newest first and at most
// usageReadsPerCall of them. known[i] says started[i]'s actions are in
// spans[i]; pending counts the runs left unread. What was read is kept even
// when a read failed, so the next call goes on. Without a state store every
// run is read.
func usageReadSpans(ctx context.Context, env *tool.Env, started []usageRun, now time.Time) (spans [][]usageSpan, known []bool, pending int, err error) {
	spans, known = make([][]usageSpan, len(started)), make([]bool, len(started))
	var cache map[string]usageCached
	_, loadErr := env.State.Load(usageSpansKind, env.Profile.Name, &cache)
	keep := loadErr == nil
	var toRead []int
	for i, r := range started {
		if c, ok := cache[r.id]; ok && keep {
			spans[i], known[i] = c.Spans, true
			continue
		}
		toRead = append(toRead, i)
	}
	if keep && len(toRead) > usageReadsPerCall {
		pending, toRead = len(toRead)-usageReadsPerCall, toRead[:usageReadsPerCall]
	}
	read := make([]bool, len(started))
	err = tool.Parallel(ctx, fanOut, toRead, func(ctx context.Context, _ int, i int) error {
		s, err := usageRunSpans(ctx, env, started[i].id)
		if err != nil {
			return err
		}
		spans[i], read[i] = s, true
		return nil
	})
	for _, i := range toRead {
		if !read[i] {
			pending++
		}
	}
	if keep {
		usageSaveSpans(env, started, spans, read, now)
	}
	if err != nil {
		return nil, nil, pending, err
	}
	for i, r := range read {
		known[i] = known[i] || r
	}
	return spans, known, pending, nil
}

// usageSaveSpans adds the completed runs read now to the usageSpansKind
// record and drops entries older than the longest window. The record is
// reloaded under usageSpansMu so concurrent calls do not lose each other's
// reads. A failed save only costs a re-read next time, so it is ignored.
func usageSaveSpans(env *tool.Env, started []usageRun, spans [][]usageSpan, read []bool, now time.Time) {
	usageSpansMu.Lock()
	defer usageSpansMu.Unlock()
	var cache map[string]usageCached
	if _, err := env.State.Load(usageSpansKind, env.Profile.Name, &cache); err != nil || cache == nil {
		cache = map[string]usageCached{}
	}
	for i, r := range started {
		if read[i] && r.complete {
			cache[r.id] = usageCached{Created: r.createdText, Spans: spans[i]}
		}
	}
	oldest := now.AddDate(0, 0, -(maxUsageDays + 1))
	for id, c := range cache {
		if t, err := time.Parse(time.RFC3339, c.Created); err != nil || t.Before(oldest) {
			delete(cache, id)
		}
	}
	_ = env.State.Save(usageSpansKind, env.Profile.Name, cache)
}

// usageRunSpans reads a run's actions and returns the finished ones. An
// action still running has no duration yet and is left out.
func usageRunSpans(ctx context.Context, env *tool.Env, runID string) ([]usageSpan, error) {
	res, err := env.ASC.List(ctx, "/v1/ciBuildRuns/"+url.PathEscape(runID)+"/actions", tool.ListOptions{
		Query: url.Values{"fields[ciBuildActions]": {"startedDate,finishedDate"}}, MaxItems: 50,
	})
	if err != nil {
		return nil, fmt.Errorf("list actions of build run %s: %w", runID, err)
	}
	var out []usageSpan
	for _, a := range res.Data {
		started, err := time.Parse(time.RFC3339, a.Str("startedDate"))
		if err != nil {
			continue
		}
		finished, err := time.Parse(time.RFC3339, a.Str("finishedDate"))
		if err != nil || !finished.After(started) {
			continue
		}
		out = append(out, usageSpan{Day: usageDate(started), Seconds: finished.Sub(started).Seconds()})
	}
	return out, nil
}
