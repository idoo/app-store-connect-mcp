package xcodecloud

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// The kit's clock is 2026-01-02T03:04:05Z.
const (
	urP1 = "7c0e5a10-0000-1111-2222-333344445555"
	urP2 = "7c0e5a20-0000-1111-2222-333344445555"
)

// urID is a made-up run or action id.
func urID(n int) string { return fmt.Sprintf("5e5e5e5e-0000-1111-2222-%012d", n) }

// urRun is a ciBuildRuns resource; an empty started leaves startedDate out.
func urRun(id string, number int, created, started, progress, status string) string {
	attrs := fmt.Sprintf(`"number":%d,"createdDate":%q,"executionProgress":%q,"completionStatus":%q`, number, created, progress, status)
	if started != "" {
		attrs += fmt.Sprintf(`,"startedDate":%q`, started)
	}
	return `{"type":"ciBuildRuns","id":"` + id + `","attributes":{` + attrs + `}}`
}

// urDone is a run that started and succeeded.
func urDone(id string, number int, created, started string) string {
	return urRun(id, number, created, started, "COMPLETE", "SUCCEEDED")
}

// urAct is a ciBuildActions resource; an empty finished leaves finishedDate out.
func urAct(id, started, finished string) string {
	attrs := fmt.Sprintf(`"startedDate":%q`, started)
	if finished != "" {
		attrs += fmt.Sprintf(`,"finishedDate":%q`, finished)
	}
	return `{"type":"ciBuildActions","id":"` + id + `","attributes":{` + attrs + `}}`
}

func urDoc(items ...string) string { return `{"data":[` + strings.Join(items, ",") + `]}` }

// urKit lists the team's products P1 and P2.
func urKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, urDoc(
		`{"type":"ciProducts","id":"`+urP1+`","attributes":{"name":"One"}}`,
		`{"type":"ciProducts","id":"`+urP2+`","attributes":{"name":"Two"}}`))
	return k
}

func urRuns(k *tooltest.Kit, product string, runs ...string) {
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/"+product+"/buildRuns", http.StatusOK, urDoc(runs...))
}

func urActions(k *tooltest.Kit, run string, acts ...string) {
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/"+run+"/actions", http.StatusOK, urDoc(acts...))
}

// urCall calls the tool and fails on a still_processing answer.
func urCall(t *testing.T, k *tooltest.Kit, args string) usageOut {
	t.Helper()
	out, pending := urCallPending(t, k, args)
	if pending {
		t.Fatalf("still processing: %+v", out)
	}
	return out
}

// urCallPending calls the tool and says whether it answered still_processing.
func urCallPending(t *testing.T, k *tooltest.Kit, args string) (usageOut, bool) {
	t.Helper()
	res := tooltest.MustOK(t, tooltest.Call(t, lookup(t, "xcc_usage_report"), k.Env, args))
	if out, ok := res.Data.(usageOut); ok {
		return out, false
	}
	m := tooltest.Decode[map[string]any](t, res)
	if m["status"] != "still_processing" {
		t.Fatalf("answer = %v", m)
	}
	return res.Data.(map[string]any)["result"].(usageOut), true
}

// urSaved is the kept record of the runs' actions.
func urSaved(t *testing.T, k *tooltest.Kit) map[string]usageCached {
	t.Helper()
	var rec map[string]usageCached
	if _, err := k.State.Load(usageSpansKind, k.Env.Profile.Name, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// urLower sets *v for the test and restores it after.
func urLower(t *testing.T, v *int, n int) {
	old := *v
	*v = n
	t.Cleanup(func() { *v = old })
}

func urActionReads(k *tooltest.Kit, run string) int {
	return len(k.ASC.RequestsTo(http.MethodGet, "/v1/ciBuildRuns/"+run+"/actions"))
}

// The window starts at 00:00 UTC of its first day; seconds are summed per
// product, day and total before rounding; an unfinished action counts
// nothing.
func TestUsageRunsWindowAndRounding(t *testing.T) {
	k := urKit(t)
	before, first, other := urID(1), urID(2), urID(3)
	urRuns(k, urP1,
		urDone(first, 2, "2025-12-31T00:00:00Z", "2025-12-31T00:01:00Z"),
		urDone(before, 1, "2025-12-30T23:59:59Z", "2025-12-31T00:00:30Z"))
	urRuns(k, urP2, urDone(other, 7, "2026-01-01T10:00:00Z", "2026-01-01T10:00:05.250Z"))
	urActions(k, first,
		urAct(urID(11), "2025-12-31T00:01:00Z", "2025-12-31T00:02:40Z"),
		urAct(urID(12), "2025-12-31T00:03:00Z", ""))
	urActions(k, other, urAct(urID(13), "2026-01-01T10:00:05.250Z", "2026-01-01T10:01:45.250Z"))

	out := urCall(t, k, `{"source":"runs","days":3}`)
	if out.Source != usageRuns || out.From != "2025-12-31" || out.To != "2026-01-02" {
		t.Fatalf("window = %s %s..%s", out.Source, out.From, out.To)
	}
	if urActionReads(k, before) != 0 {
		t.Fatal("read the actions of a run created before the window")
	}
	// 100 s + 100 s: each product rounds to 2, the total to 3.
	if out.Minutes != 3 || out.Builds != 2 || out.RunsScanned != 2 || out.Truncated {
		t.Fatalf("totals = %+v", out)
	}
	if len(out.Products) != 2 || out.Products[0].ID != urP1 || out.Products[0].Minutes != 2 || out.Products[0].Builds != 1 ||
		out.Products[1].ID != urP2 || out.Products[1].Minutes != 2 || out.Products[1].Builds != 1 {
		t.Fatalf("products = %+v", out.Products)
	}
	want := []usageDay{{"2025-12-31", 2, 1}, {"2026-01-01", 2, 1}}
	if fmt.Sprint(out.Days) != fmt.Sprint(want) {
		t.Fatalf("days = %+v, want %+v", out.Days, want)
	}
	if out.LastStartedAt != "2026-01-01T10:00:05.250Z" || out.NotTakingRuns || out.DeadRunsTotal != 0 {
		t.Fatalf("runs = %+v", out)
	}
}

// Products are most minutes first; days oldest first, an action counted on
// the UTC day it started and a build on the day its run started.
func TestUsageRunsSorts(t *testing.T) {
	k := urKit(t)
	small, big := urID(1), urID(2)
	urRuns(k, urP1, urDone(small, 1, "2025-12-31T08:00:00Z", "2025-12-31T08:00:00Z"))
	urRuns(k, urP2, urDone(big, 1, "2026-01-01T23:58:00Z", "2026-01-01T23:59:00Z"))
	urActions(k, small, urAct(urID(11), "2025-12-31T08:00:00Z", "2025-12-31T08:01:00Z"))
	urActions(k, big,
		urAct(urID(12), "2026-01-02T00:00:00Z", "2026-01-02T00:10:00Z"),
		urAct(urID(13), "2026-01-01T23:59:00Z", "2026-01-01T23:59:50Z"))

	out := urCall(t, k, `{"source":"runs"}`)
	if out.From != "2025-12-03" || out.To != "2026-01-02" {
		t.Fatalf("default window = %s..%s", out.From, out.To)
	}
	if len(out.Products) != 2 || out.Products[0].ID != urP2 || out.Products[0].Minutes != 11 || out.Products[1].ID != urP1 {
		t.Fatalf("products = %+v", out.Products)
	}
	want := []usageDay{{"2025-12-31", 1, 1}, {"2026-01-01", 1, 1}, {"2026-01-02", 10, 0}}
	if fmt.Sprint(out.Days) != fmt.Sprint(want) {
		t.Fatalf("days = %+v, want %+v", out.Days, want)
	}
	if out.Minutes != 12 || out.Builds != 2 {
		t.Fatalf("totals = %d min %d builds", out.Minutes, out.Builds)
	}
}

// Dead runs of all products merge newest first, capped with the full count;
// not_taking_runs follows the newest run of the window only.
func TestUsageRunsDeadRuns(t *testing.T) {
	for _, tc := range []struct {
		name       string
		startedAt  string // created date of the one started run
		notTaking  bool
		lastStarts string
		sinceStart int
	}{
		{"newest is dead", "2025-12-31T12:00:00Z", true, "2025-12-31T12:01:00Z", 12},
		{"newest started", "2026-01-01T12:00:00Z", false, "2026-01-01T12:01:00Z", 0},
		{"started between", "2026-01-01T05:00:00Z", true, "2026-01-01T05:01:00Z", 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := urKit(t)
			var p1, p2 []string
			for h := 11; h >= 0; h-- {
				status := "FAILED"
				if h%3 == 0 {
					status = "ERRORED"
				}
				r := urRun(urID(100+h), 100+h, fmt.Sprintf("2026-01-01T%02d:00:00Z", h), "", "COMPLETE", status)
				if h%2 == 0 {
					p1 = append(p1, r)
				} else {
					p2 = append(p2, r)
				}
			}
			started := urID(1)
			p2 = append(p2, urDone(started, 1, tc.startedAt, strings.Replace(tc.startedAt, ":00:00Z", ":01:00Z", 1)))
			urRuns(k, urP1, p1...)
			urRuns(k, urP2, p2...)
			urActions(k, started)

			out := urCall(t, k, `{"source":"runs"}`)
			if out.DeadRunsTotal != 12 || len(out.DeadRuns) != maxDeadRuns {
				t.Fatalf("dead = %d listed of %d", len(out.DeadRuns), out.DeadRunsTotal)
			}
			for i, d := range out.DeadRuns {
				h := 11 - i
				wantProd := urP2
				if h%2 == 0 {
					wantProd = urP1
				}
				if d.ID != urID(100+h) || d.Number != 100+h || d.Product.ID != wantProd ||
					d.CreatedDate != fmt.Sprintf("2026-01-01T%02d:00:00Z", h) {
					t.Fatalf("dead[%d] = %+v", i, d)
				}
			}
			if out.NotTakingRuns != tc.notTaking || out.DeadRunsSinceStart != tc.sinceStart || out.LastStartedAt != tc.lastStarts || out.Builds != 1 || out.RunsScanned != 1 {
				t.Fatalf("out = %+v", out)
			}
			for h := range 12 {
				if urActionReads(k, urID(100+h)) != 0 {
					t.Fatal("read the actions of a dead run")
				}
			}
		})
	}
}

// Above maxUsageRuns only the newest started runs are read.
func TestUsageRunsTruncates(t *testing.T) {
	urLower(t, &maxUsageRuns, 2)
	k := urKit(t)
	oldest, mid, newest := urID(1), urID(2), urID(3)
	urRuns(k, urP1,
		urDone(newest, 3, "2026-01-01T10:00:00Z", "2026-01-01T10:00:00Z"),
		urDone(oldest, 1, "2025-12-20T10:00:00Z", "2025-12-20T10:00:00Z"))
	urRuns(k, urP2, urDone(mid, 2, "2025-12-25T10:00:00Z", "2025-12-25T10:00:00Z"))
	urActions(k, newest, urAct(urID(11), "2026-01-01T10:00:00Z", "2026-01-01T10:02:00Z"))
	urActions(k, mid, urAct(urID(12), "2025-12-25T10:00:00Z", "2025-12-25T10:03:00Z"))

	out := urCall(t, k, `{"source":"runs"}`)
	if !out.Truncated || out.RunsScanned != 2 || out.Builds != 2 || out.Minutes != 5 {
		t.Fatalf("out = %+v", out)
	}
	if urActionReads(k, oldest) != 0 {
		t.Fatal("read the actions of a run past the cap")
	}
	if len(out.Days) != 2 || out.Days[0].Date != "2025-12-25" {
		t.Fatalf("days = %+v", out.Days)
	}
}

// A pinned default app does not narrow the team's products.
func TestUsageRunsPinnedAppDoesNotNarrow(t *testing.T) {
	k := urKit(t)
	k.Env.Profile.DefaultApp = "A1"
	k.Resolve.DefaultApp = "A1"
	r1, r2 := urID(1), urID(2)
	urRuns(k, urP1, urDone(r1, 1, "2026-01-01T10:00:00Z", "2026-01-01T10:00:00Z"))
	urRuns(k, urP2, urDone(r2, 1, "2026-01-01T11:00:00Z", "2026-01-01T11:00:00Z"))
	urActions(k, r1, urAct(urID(11), "2026-01-01T10:00:00Z", "2026-01-01T10:01:00Z"))
	urActions(k, r2, urAct(urID(12), "2026-01-01T11:00:00Z", "2026-01-01T11:02:00Z"))

	out := urCall(t, k, `{"source":"runs"}`)
	for _, q := range k.ASC.RequestsTo(http.MethodGet, "/v1/ciProducts") {
		if q.Query.Has("filter[app]") {
			t.Fatalf("products narrowed: %v", q.Query)
		}
	}
	if len(out.Products) != 2 || out.Minutes != 3 {
		t.Fatalf("out = %+v", out)
	}
}

// A product given reads only that product's runs.
func TestUsageRunsOneProduct(t *testing.T) {
	k := urKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/"+urP2, http.StatusOK,
		`{"data":{"type":"ciProducts","id":"`+urP2+`","attributes":{"name":"Two"}}}`)
	r := urID(1)
	urRuns(k, urP2, urDone(r, 1, "2026-01-01T10:00:00Z", "2026-01-01T10:00:00Z"))
	urActions(k, r, urAct(urID(11), "2026-01-01T10:00:00Z", "2026-01-01T10:04:00Z"))

	out := urCall(t, k, `{"source":"runs","product":"`+urP2+`"}`)
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/ciProducts")); n != 0 {
		t.Fatalf("listed the team's products %d times", n)
	}
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/ciProducts/"+urP1+"/buildRuns")); n != 0 {
		t.Fatal("read another product's runs")
	}
	if len(out.Products) != 1 || out.Products[0].ID != urP2 || out.Minutes != 4 || out.Builds != 1 {
		t.Fatalf("out = %+v", out)
	}
}

// A product whose list ends at the per-product cap with its oldest run
// still inside the window leaves older runs out.
func TestUsageRunsPerProductCap(t *testing.T) {
	urLower(t, &usageRunsPerProduct, 2)
	for _, tc := range []struct {
		name   string
		oldest string
		want   bool
	}{
		{"oldest inside", "2025-12-30T10:00:00Z", true},
		{"oldest before the window", "2025-11-30T10:00:00Z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := urKit(t)
			r1, r2 := urID(1), urID(2)
			urRuns(k, urP1, urDone(r2, 2, "2026-01-01T10:00:00Z", "2026-01-01T10:00:00Z"),
				urDone(r1, 1, tc.oldest, tc.oldest))
			urRuns(k, urP2)
			urActions(k, r1)
			urActions(k, r2)
			if out := urCall(t, k, `{"source":"runs"}`); out.Truncated != tc.want {
				t.Fatalf("truncated = %v", out.Truncated)
			}
		})
	}
}

// urThree is P1 with three completed runs of 1, 2 and 3 minutes (newest
// last) and P2 with the runs p2.
func urThree(t *testing.T, p2 ...string) (*tooltest.Kit, []string) {
	t.Helper()
	k := urKit(t)
	ids := []string{urID(1), urID(2), urID(3)}
	var runs []string
	for i, id := range ids {
		at := fmt.Sprintf("2026-01-01T1%d:00:00Z", i)
		runs = append(runs, urDone(id, i+1, at, at))
		urActions(k, id, urAct(urID(10+i), at, fmt.Sprintf("2026-01-01T1%d:0%d:00Z", i, i+1)))
	}
	urRuns(k, urP1, runs...)
	urRuns(k, urP2, p2...)
	return k, ids
}

// A completed run's actions are read once; a run still going every time.
func TestUsageRunsKeepsCompletedRuns(t *testing.T) {
	going := urID(4)
	k, ids := urThree(t, urRun(going, 9, "2026-01-02T01:00:00Z", "2026-01-02T01:00:00Z", "RUNNING", ""))
	urActions(k, going, urAct(urID(20), "2026-01-02T01:00:00Z", "2026-01-02T01:04:00Z"))

	first := urCall(t, k, `{"source":"runs"}`)
	second := urCall(t, k, `{"source":"runs"}`)
	if fmt.Sprintf("%+v", first) != fmt.Sprintf("%+v", second) {
		t.Fatalf("numbers moved:\n%+v\n%+v", first, second)
	}
	if first.Minutes != 10 || first.Builds != 4 || first.RunsScanned != 4 {
		t.Fatalf("out = %+v", first)
	}
	for _, id := range ids {
		if n := urActionReads(k, id); n != 1 {
			t.Fatalf("completed run read %d times", n)
		}
	}
	if n := urActionReads(k, going); n != 2 {
		t.Fatalf("running run read %d times", n)
	}
	if rec := urSaved(t, k); len(rec) != 3 {
		t.Fatalf("saved = %+v", rec)
	}
}

// Above usageReadsPerCall unread runs, a call reads the newest, answers
// still_processing with numbers over those only, and the next goes on.
func TestUsageRunsReadsInSteps(t *testing.T) {
	urLower(t, &usageReadsPerCall, 2)
	k, ids := urThree(t)
	out, pending := urCallPending(t, k, `{"source":"runs"}`)
	if !pending || out.RunsPending != 1 || out.RunsScanned != 2 || out.Builds != 2 || out.Minutes != 5 {
		t.Fatalf("first = pending %v %+v", pending, out)
	}
	if urActionReads(k, ids[0]) != 0 || out.LastStartedAt != "2026-01-01T12:00:00Z" {
		t.Fatalf("read the oldest run first: %+v", out)
	}
	out = urCall(t, k, `{"source":"runs"}`)
	if out.RunsPending != 0 || out.RunsScanned != 3 || out.Minutes != 6 {
		t.Fatalf("second = %+v", out)
	}
	for _, id := range ids {
		if n := urActionReads(k, id); n != 1 {
			t.Fatalf("run read %d times", n)
		}
	}
}

// Kept entries older than the longest window, or undated, are dropped; a
// fresh one stays and is used without a read.
func TestUsageRunsDropsOldEntries(t *testing.T) {
	k := urKit(t)
	kept, fresh := urID(1), urID(2)
	if err := k.State.Save(usageSpansKind, k.Env.Profile.Name, map[string]usageCached{
		kept:    {Created: "2025-12-20T10:00:00Z", Spans: []usageSpan{{Day: "2025-12-20", Seconds: 120}}},
		urID(8): {Created: "2025-11-30T10:00:00Z"},
		urID(9): {Created: "yesterday"},
		urID(7): {Created: "2025-12-02T10:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	urRuns(k, urP1, urDone(fresh, 2, "2026-01-01T10:00:00Z", "2026-01-01T10:00:00Z"),
		urDone(kept, 1, "2025-12-20T10:00:00Z", "2025-12-20T10:00:00Z"))
	urRuns(k, urP2)
	urActions(k, fresh, urAct(urID(11), "2026-01-01T10:00:00Z", "2026-01-01T10:01:00Z"))

	out := urCall(t, k, `{"source":"runs"}`)
	if urActionReads(k, kept) != 0 || out.Minutes != 3 || out.Builds != 2 {
		t.Fatalf("out = %+v", out)
	}
	rec := urSaved(t, k)
	if len(rec) != 3 || rec[kept].Created == "" || rec[fresh].Created == "" || rec[urID(7)].Created == "" {
		t.Fatalf("saved = %+v", rec)
	}
}

// A failed read keeps the runs read before it; the next call reads only
// the missing one.
func TestUsageRunsKeepsReadsOnFailure(t *testing.T) {
	k, ids := urThree(t)
	// The oldest run is read last, after the others started.
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/"+ids[0]+"/actions?fields[ciBuildActions]=startedDate,finishedDate",
		http.StatusInternalServerError, `{"errors":[{"status":"500","title":"boom"}]}`).
		Handle(http.MethodGet, "/v1/ciBuildRuns/"+ids[0]+"/actions?fields[ciBuildActions]=startedDate,finishedDate",
			http.StatusOK, urDoc(urAct(urID(10), "2026-01-01T10:00:00Z", "2026-01-01T10:01:00Z")))
	if res := tooltest.Call(t, lookup(t, "xcc_usage_report"), k.Env, `{"source":"runs"}`); !res.IsError {
		t.Fatalf("no error: %s", tooltest.JSON(t, res.Data))
	}
	if rec := urSaved(t, k); len(rec) != 2 {
		t.Fatalf("saved = %+v", rec)
	}
	out := urCall(t, k, `{"source":"runs"}`)
	if out.Minutes != 6 || out.RunsScanned != 3 || urActionReads(k, ids[1]) != 1 || urActionReads(k, ids[2]) != 1 {
		t.Fatalf("out = %+v", out)
	}
}

// Without a state store everything is read in the one call.
func TestUsageRunsWithoutStateStore(t *testing.T) {
	urLower(t, &usageReadsPerCall, 1)
	k, ids := urThree(t)
	k.Env.State = nil
	for range 2 {
		if out := urCall(t, k, `{"source":"runs"}`); out.RunsPending != 0 || out.RunsScanned != 3 || out.Minutes != 6 {
			t.Fatalf("out = %+v", out)
		}
	}
	if n := urActionReads(k, ids[0]); n != 2 {
		t.Fatalf("read %d times, want every call", n)
	}
}
