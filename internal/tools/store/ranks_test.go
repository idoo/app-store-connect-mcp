package store

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

type fakeArtifacts struct{ got map[string][]byte }

func (a *fakeArtifacts) Put(_ context.Context, name, mime string, b []byte) (tool.Link, error) {
	a.got[name] = b
	return tool.Link{URI: "app-store-connect-mcp://artifacts/1", Name: name, MIMEType: mime, Size: int64(len(b))}, nil
}

type rankRow struct {
	Keyword   string           `json:"keyword"`
	Country   string           `json:"country"`
	Rank      *int             `json:"rank"`
	Previous  *int             `json:"previous"`
	Change7d  *int             `json:"change_7d"`
	Best      *int             `json:"best"`
	Trend     string           `json:"trend"`
	Spark     string           `json:"spark"`
	Tracked   bool             `json:"tracked"`
	Above     []map[string]any `json:"above"`
	Points    int              `json:"points"`
	Untracked int              `json:"untracked"`
}

type ranksRes struct {
	App       int64     `json:"app"`
	Measured  int       `json:"measured"`
	Remaining int       `json:"remaining"`
	Throttled bool      `json:"throttled"`
	Untracked int       `json:"untracked"`
	Ranks     []rankRow `json:"ranks"`
	Tracked   []string  `json:"tracked"`
	Points    int       `json:"history_points"`
}

func ranksKit(t *testing.T) (*tooltest.Kit, *fakeArtifacts) {
	t.Helper()
	ranksPacer.recent = nil
	k := tooltest.New(t)
	a := &fakeArtifacts{got: map[string][]byte{}}
	k.Env.Artifacts = a
	return k, a
}

func ranksTool(t *testing.T) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup("store_ranks_report")
	if !ok {
		t.Fatal("store_ranks_report not registered")
	}
	return tl
}

func searchURL(term, country string, limit int) string {
	return "https://itunes.apple.com/search?country=" + country + "&entity=software&limit=" + strconv.Itoa(limit) + "&term=" + term
}

func results(apps ...string) []byte {
	return []byte(`{"resultCount":` + strconv.Itoa(len(apps)) + `,"results":[` + strings.Join(apps, ",") + `]}`)
}

func TestRanksTrackFoundAndNotFound(t *testing.T) {
	k, art := ranksKit(t)
	k.Fetch.Handle(http.MethodGet, "https://itunes.apple.com/lookup?bundleId=se.jvcr.hjul&country=us&entity=software", http.StatusOK, results(hjul))
	k.Fetch.Handle(http.MethodGet, searchURL("bike+route", "us", 100), http.StatusOK, results(other, hjul))
	k.Fetch.Handle(http.MethodGet, searchURL("bike+route", "se", 100), http.StatusOK, results(other))
	res := tooltest.MustOK(t, tooltest.Call(t, ranksTool(t), k.Env,
		`{"app":"se.jvcr.hjul","keywords":["  Bike   Route "],"countries":["US","se"]}`))
	out := tooltest.Decode[ranksRes](t, res)
	if out.App != 1234567890 || out.Measured != 2 || out.Throttled || len(out.Ranks) != 2 {
		t.Fatalf("out = %+v", out)
	}
	us, se := out.Ranks[0], out.Ranks[1]
	if us.Rank == nil || *us.Rank != 2 || us.Trend != "new" || !us.Tracked || len(us.Above) != 1 {
		t.Fatalf("us = %+v", us)
	}
	if name, _ := us.Above[0]["name"].(map[string]any); name["untrusted"] != "Bike Map" {
		t.Fatalf("competitor name not untrusted: %v", us.Above[0])
	}
	if se.Rank != nil || se.Trend != "new" || len(se.Above) != 1 {
		t.Fatalf("se = %+v", se)
	}
	if k.Clock.Slept != ranksSpacing {
		t.Fatalf("slept %v, want one %v spacing", k.Clock.Slept, ranksSpacing)
	}
	if len(k.ASC.Requests()) != 0 {
		t.Fatal("called App Store Connect")
	}
	var h asoHistory
	if ok, err := k.State.Load(asoKind, "1234567890", &h); !ok || err != nil || len(h.Points) != 2 || len(h.Tracked) != 2 {
		t.Fatalf("history ok=%v err=%v %+v", ok, err, h)
	}
	csv := string(art.got["aso-1234567890.csv"])
	if len(res.Links) != 1 || !strings.HasPrefix(csv, "time,keyword,country,rank,depth\n") ||
		!strings.Contains(csv, ",bike route,us,2,100") || !strings.Contains(csv, ",bike route,se,,100") {
		t.Fatalf("links = %+v csv = %q", res.Links, csv)
	}
}

func TestRanksHistoryTrendAndUntrack(t *testing.T) {
	k, _ := ranksKit(t)
	k.Fetch.Handle(http.MethodGet, searchURL("bike", "us", 50), http.StatusOK, results(other, hjul))
	tooltest.MustOK(t, tooltest.Call(t, ranksTool(t), k.Env, `{"app":"1234567890","keywords":["bike"],"depth":50}`))

	// Eight days later a track without keywords re-measures the tracked set.
	k.Clock.Set(k.Clock.Now().Add(8 * 24 * time.Hour))
	k.Fetch.Handle(http.MethodGet, searchURL("bike", "us", 50), http.StatusOK, results(hjul, other))
	out := tooltest.Decode[ranksRes](t, tooltest.MustOK(t, tooltest.Call(t, ranksTool(t), k.Env, `{"app":"1234567890","depth":50}`)))
	r := out.Ranks[0]
	if r.Rank == nil || *r.Rank != 1 || r.Previous == nil || *r.Previous != 2 || r.Trend != "up" ||
		r.Change7d == nil || *r.Change7d != 1 || r.Best == nil || *r.Best != 1 || len(r.Above) != 0 || r.Points != 2 ||
		len([]rune(r.Spark)) != 2 || out.Points != 2 {
		t.Fatalf("out = %+v row = %+v", out, r)
	}

	// The same day replaces the measurement instead of appending.
	k.Fetch.Handle(http.MethodGet, searchURL("bike", "us", 50), http.StatusOK, results(other))
	out = tooltest.Decode[ranksRes](t, tooltest.MustOK(t, tooltest.Call(t, ranksTool(t), k.Env, `{"app":"1234567890","depth":50}`)))
	if r := out.Ranks[0]; out.Points != 2 || r.Rank != nil || r.Trend != "dropped_out" {
		t.Fatalf("same-day out = %+v", out)
	}

	untracked := tooltest.Decode[ranksRes](t, tooltest.MustOK(t, tooltest.Call(t, ranksTool(t), k.Env,
		`{"action":"untrack","app":"1234567890","keywords":["bike"]}`)))
	if untracked.Untracked != 1 || len(untracked.Tracked) != 0 || untracked.Points != 2 {
		t.Fatalf("untrack = %+v", untracked)
	}
	calls := len(k.Fetch.Calls)
	rep := tooltest.Decode[ranksRes](t, tooltest.MustOK(t, tooltest.Call(t, ranksTool(t), k.Env, `{"action":"report","app":"1234567890"}`)))
	if len(rep.Ranks) != 1 || rep.Ranks[0].Tracked || rep.Ranks[0].Points != 2 || len(k.Fetch.Calls) != calls {
		t.Fatalf("report = %+v", rep)
	}
	// Nothing tracked any more: track without keywords is an argument error.
	tooltest.MustFail(t, tooltest.Call(t, ranksTool(t), k.Env, `{"app":"1234567890"}`), tool.ErrInvalidArgs.Code)

	// A year later the old points roll off.
	h := asoHistory{Points: []asoPoint{{T: 0, K: "x", C: "us", R: 1, D: 10}}}
	h.add(time.Unix(400*24*3600, 0), []asoPoint{{T: 400 * 24 * 3600, K: "x", C: "us", R: 3, D: 10}})
	if len(h.Points) != 1 || h.Points[0].R != 3 {
		t.Fatalf("rollover = %+v", h.Points)
	}
}

func TestRanksThrottledStopsAndSavesPartial(t *testing.T) {
	k, _ := ranksKit(t)
	k.Fetch.Handle(http.MethodGet, searchURL("a", "us", 100), http.StatusOK, results(hjul))
	k.Fetch.Handle(http.MethodGet, searchURL("b", "us", 100), http.StatusTooManyRequests, nil)
	res := tooltest.MustOK(t, tooltest.Call(t, ranksTool(t), k.Env, `{"app":"1234567890","keywords":["a","b","c"]}`))
	out := tooltest.Decode[ranksRes](t, res)
	if !out.Throttled || out.Measured != 1 || out.Remaining != 2 || len(out.Ranks) != 1 || len(k.Fetch.Calls) != 2 {
		t.Fatalf("out = %+v calls = %v", out, k.Fetch.Calls)
	}
	if len(res.Meta.Warnings) != 1 || !strings.Contains(res.Meta.Warnings[0], "HTTP 429") || len(res.Next) != 1 {
		t.Fatalf("warnings = %v next = %+v", res.Meta.Warnings, res.Next)
	}
	if k.Clock.Slept != ranksSpacing {
		t.Fatalf("slept %v", k.Clock.Slept)
	}
	var h asoHistory
	if _, err := k.State.Load(asoKind, "1234567890", &h); err != nil || len(h.Points) != 1 || len(h.Tracked) != 3 {
		t.Fatalf("history err=%v %+v", err, h)
	}
}

func TestRanksArgumentErrors(t *testing.T) {
	kw := `"k1","k2","k3","k4","k5","k6","k7"`
	cs := `"us","se","de","fr","gb","no","dk","fi","nl"`
	for _, args := range []string{
		`{"app":"1234567890","keywords":[` + kw + `],"countries":[` + cs + `]}`, // 63 lookups
		`{"app":"1234567890","keywords":["x"],"depth":201}`,
		`{"app":"1234567890","keywords":["x"],"countries":["usa"]}`,
		`{"app":"1234567890","keywords":[" "]}`,
		`{"app":"1234567890","action":"untrack"}`,
		`{"app":"Hjul","keywords":["x"]}`, // a name needs an ASC key
		`{"keywords":["x"]}`,              // so does the pinned app
	} {
		k, _ := ranksKit(t)
		k.Env.Resolve = nil // no ASC key
		res := tooltest.MustFail(t, tooltest.Call(t, ranksTool(t), k.Env, args), tool.ErrInvalidArgs.Code)
		if len(k.Fetch.Calls) != 0 {
			t.Fatalf("%s: fetched", args)
		}
		if strings.Contains(args, "k7") && !strings.Contains(tooltest.JSON(t, res), "exceed 60") {
			t.Fatalf("cap error = %s", tooltest.JSON(t, res))
		}
	}
}

func TestRanksResolvesNameViaASC(t *testing.T) {
	k, _ := ranksKit(t)
	k.Resolve.Apps = []tool.App{{ID: "1234567890", Name: "Hjul", BundleID: "se.jvcr.hjul.unreleased"}}
	k.Fetch.Handle(http.MethodGet, searchURL("bike", "us", 100), http.StatusOK, results(other, hjul))
	out := tooltest.Decode[ranksRes](t, tooltest.MustOK(t, tooltest.Call(t, ranksTool(t), k.Env, `{"app":"hjul","keywords":["bike"]}`)))
	if out.App != 1234567890 || len(out.Ranks) != 1 || out.Ranks[0].Rank == nil || *out.Ranks[0].Rank != 2 {
		t.Fatalf("out = %+v", out)
	}
	// A bundle id iTunes does not know (unreleased) falls back to the resolver.
	k.Fetch.Handle(http.MethodGet, "https://itunes.apple.com/lookup?bundleId=se.jvcr.hjul.unreleased&country=us&entity=software",
		http.StatusOK, results())
	out = tooltest.Decode[ranksRes](t, tooltest.MustOK(t, tooltest.Call(t, ranksTool(t), k.Env, `{"app":"se.jvcr.hjul.unreleased","action":"report"}`)))
	if out.App != 1234567890 || out.Points != 1 {
		t.Fatalf("report out = %+v", out)
	}
	if len(k.ASC.Requests()) != 0 {
		t.Fatal("called App Store Connect directly")
	}
	tooltest.MustFail(t, tooltest.Call(t, ranksTool(t), k.Env, `{"app":"Nope","keywords":["bike"]}`), "not_found")
}

func TestRanksPacer(t *testing.T) {
	p := &pacer{slot: make(chan struct{}, 1)}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if d := p.delay(t0); d != 0 {
		t.Fatalf("first delay %v", d)
	}
	p.mark(t0)
	if d := p.delay(t0.Add(time.Second)); d != 2*time.Second {
		t.Fatalf("spacing delay %v", d)
	}
	// A burst of 20 within a minute waits for the oldest to age out.
	for i := 1; i < ranksPerMinute; i++ {
		p.mark(t0.Add(time.Duration(i) * 100 * time.Millisecond))
	}
	if d := p.delay(t0.Add(10 * time.Second)); d != 50*time.Second {
		t.Fatalf("bucket delay %v", d)
	}
	if d := p.delay(t0.Add(-time.Hour)); d != 0 || len(p.recent) != 0 {
		t.Fatalf("backwards clock did not reset: %v", d)
	}
}

func TestRanksMeta(t *testing.T) {
	m := ranksTool(t).Meta()
	if m.Class != tool.Read || m.Idempotent || m.Toolset != tool.Store || len(m.Description) > 280 {
		t.Fatalf("meta = %+v (%d chars)", m, len(m.Description))
	}
}

// racingStore lets another writer save right after this call's first Load,
// as an overlapping store_ranks_report call (or process) would.
type racingStore struct {
	tool.StateStore
	once func()
}

func (s *racingStore) Load(kind, key string, v any) (bool, error) {
	ok, err := s.StateStore.Load(kind, key, v)
	if s.once != nil {
		f := s.once
		s.once = nil
		f()
	}
	return ok, err
}

func TestRanksTrackKeepsConcurrentWrites(t *testing.T) {
	k, _ := ranksKit(t)
	k.Fetch.Handle(http.MethodGet, "https://itunes.apple.com/lookup?bundleId=se.jvcr.hjul&country=us&entity=software", http.StatusOK, results(hjul))
	k.Fetch.Handle(http.MethodGet, searchURL("alpha", "us", 100), http.StatusOK, results(hjul))
	inner := k.Env.State
	k.Env.State = &racingStore{StateStore: inner, once: func() {
		other := asoHistory{
			Tracked: []asoPair{{"beta", "us"}},
			Points:  []asoPoint{{T: k.Clock.Now().Unix(), K: "beta", C: "us", R: 7, D: 100}},
		}
		if err := inner.Save(asoKind, "1234567890", &other); err != nil {
			t.Error(err)
		}
	}}
	tooltest.MustOK(t, tooltest.Call(t, ranksTool(t), k.Env, `{"app":"se.jvcr.hjul","keywords":["alpha"],"countries":["us"]}`))
	var h asoHistory
	if _, err := inner.Load(asoKind, "1234567890", &h); err != nil {
		t.Fatal(err)
	}
	if len(h.Tracked) != 2 || len(h.Points) != 2 {
		t.Fatalf("concurrent keyword lost: %+v", h)
	}
}
