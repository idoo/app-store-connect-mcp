package reports

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

const (
	s10cApp      = "1000000001"
	requestsPath = "/v1/apps/{id}/analyticsReportRequests"
	emptyList    = `{"data":[],"links":{"self":"https://api.appstoreconnect.apple.com/v1/x"},"meta":{"paging":{"total":0,"limit":50}}}`
	ongoingList  = `{"data":[{"type":"analyticsReportRequests","id":"req-1","attributes":{"accessType":"ONGOING","stoppedDueToInactivity":false}}],"links":{"self":"https://api.appstoreconnect.apple.com/v1/x"}}`
)

// gzFile returns the gzip bytes of a testdata/s10c file.
func gzFile(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "s10c", name))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// doc builds a JSON:API collection document from resources.
func doc(t *testing.T, items ...map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"data": items, "links": map[string]string{"self": "https://api.appstoreconnect.apple.com/v1/x"}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func res(typ, id string, attrs map[string]any) map[string]any {
	return map[string]any{"type": typ, "id": id, "attributes": attrs}
}

func TestAnalyticsSetupAlready(t *testing.T) {
	k := s10bKit(t)
	k.ASC.Handle(http.MethodGet, requestsPath, http.StatusOK, ongoingList)
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "analytics_setup"), k.Env, `{"app":"Example Notes"}`))
	got := tooltest.Decode[map[string]any](t, r)
	if got["already"] != true || !strings.Contains(tooltest.JSON(t, got["requests"]), `"status":"already"`) {
		t.Errorf("got = %s", tooltest.JSON(t, got))
	}
	if m := k.ASC.Mutations(); len(m) != 0 {
		t.Errorf("mutations = %+v", m)
	}
	if len(r.Next) != 1 || r.Next[0].Tool != "analytics_report" {
		t.Errorf("next = %+v", r.Next)
	}
}

func TestAnalyticsSetupCreates(t *testing.T) {
	k := s10bKit(t)
	k.ASC.Handle(http.MethodGet, requestsPath, http.StatusOK, emptyList)
	k.ASC.HandleFunc(http.MethodPost, "/v1/analyticsReportRequests", func(req tool.Request) tooltest.Fixture {
		access := req.Body.(map[string]any)["data"].(map[string]any)["attributes"].(map[string]any)["accessType"].(string)
		return tooltest.Fixture{Status: http.StatusCreated, Body: []byte(`{"data":{"type":"analyticsReportRequests","id":"new-` + access +
			`","attributes":{"accessType":"` + access + `"}},"links":{"self":"https://api.appstoreconnect.apple.com/v1/x"}}`)}
	})
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "analytics_setup"), k.Env, `{"one_time_snapshot":true}`))
	got := tooltest.Decode[map[string]any](t, r)
	if _, already := got["already"]; already {
		t.Fatalf("got already: %s", tooltest.JSON(t, got))
	}
	if s := tooltest.JSON(t, got["requests"]); s != `[{"access_type":"ONGOING","id":"new-ONGOING","status":"created"},{"access_type":"ONE_TIME_SNAPSHOT","id":"new-ONE_TIME_SNAPSHOT","status":"created"}]` {
		t.Errorf("requests = %s", s)
	}
	if !strings.Contains(got["note"].(string), "24–48 h") {
		t.Errorf("note = %v", got["note"])
	}
	posts := k.ASC.Mutations()
	if len(posts) != 2 {
		t.Fatalf("mutations = %d", len(posts))
	}
	body := tooltest.JSON(t, posts[0].Body)
	if !strings.Contains(body, `"app":{"data":{"id":"1000000001","type":"apps"}}`) || !strings.Contains(body, `"accessType":"ONGOING"`) {
		t.Errorf("body = %s", body)
	}
}

func TestAnalyticsSetupWithheldReadOnly(t *testing.T) {
	k := s10bKit(t).ReadOnly()
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "analytics_setup"), k.Env, ``), "writes_disabled")
	if n := len(k.ASC.Requests()); n != 0 {
		t.Errorf("requests = %d", n)
	}
}

func TestAnalyticsReportNotSetUp(t *testing.T) {
	k := s10bKit(t).ReadOnly()
	k.ASC.Handle(http.MethodGet, requestsPath, http.StatusOK, emptyList)
	r := tooltest.Call(t, mustTool(t, "analytics_report"), k.Env, `{"report":"App Store Discovery"}`)
	body := tooltest.MustFail(t, r, "analytics_not_set_up")
	if len(r.Next) != 1 || r.Next[0].Tool != "analytics_setup" || r.Next[0].Args["app"] != s10cApp {
		t.Errorf("next = %+v", r.Next)
	}
	if !strings.Contains(body.Hint, "--allow-writes") {
		t.Errorf("hint = %q", body.Hint)
	}
}

func analyticsKit(t *testing.T) *tooltest.Kit {
	k := s10bKit(t).ReadOnly()
	k.ASC.Handle(http.MethodGet, requestsPath, http.StatusOK, ongoingList)
	k.ASC.Handle(http.MethodGet, "/v1/analyticsReportRequests/req-1/reports", http.StatusOK, doc(t,
		res("analyticsReports", "rep-1", map[string]any{"name": "App Store Discovery and Engagement Standard", "category": "APP_STORE_ENGAGEMENT"}),
		res("analyticsReports", "rep-2", map[string]any{"name": "App Sessions Standard", "category": "APP_USAGE"}),
	))
	k.ASC.Handle(http.MethodGet, "/v1/analyticsReports/rep-1/instances", http.StatusOK, doc(t,
		res("analyticsReportInstances", "inst-1", map[string]any{"granularity": "DAILY", "processingDate": "2026-10-01"}),
		res("analyticsReportInstances", "inst-2", map[string]any{"granularity": "DAILY", "processingDate": "2026-10-20"}),
		res("analyticsReportInstances", "inst-3", map[string]any{"granularity": "DAILY", "processingDate": "2026-10-21"}),
	))
	seg := func(id, u string) map[string]any {
		return res("analyticsReportSegments", id, map[string]any{"url": u, "checksum": "x", "sizeInBytes": 10})
	}
	k.ASC.Handle(http.MethodGet, "/v1/analyticsReportInstances/inst-3/segments", http.StatusOK, doc(t, seg("seg-b", "https://segments.example.com/b.gz")))
	k.ASC.Handle(http.MethodGet, "/v1/analyticsReportInstances/inst-2/segments", http.StatusOK, doc(t,
		seg("seg-a", "https://segments.example.com/a.gz"), seg("seg-a2", "https://segments.example.com/a2.gz")))
	k.Fetch.Handle(http.MethodGet, "https://segments.example.com/b.gz", http.StatusOK, gzFile(t, "segment_b.csv"))
	k.Fetch.Handle(http.MethodGet, "https://segments.example.com/a.gz", http.StatusOK, gzFile(t, "segment_a.csv"))
	return k
}

func TestAnalyticsReportList(t *testing.T) {
	k := analyticsKit(t)
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "analytics_report"), k.Env, `{"category":"APP_STORE_ENGAGEMENT"}`))
	got := tooltest.Decode[map[string]any](t, r)
	if s := tooltest.JSON(t, got["reports"]); !strings.HasPrefix(s, `[{"category":"APP_STORE_ENGAGEMENT","id":"rep-1"`) {
		t.Errorf("reports = %s", s)
	}
	reqs := k.ASC.RequestsTo(http.MethodGet, "/v1/analyticsReportRequests/req-1/reports")
	if len(reqs) != 1 || reqs[0].Query.Get("filter[category]") != "APP_STORE_ENGAGEMENT" {
		t.Errorf("reports requests = %+v", reqs)
	}
}

func TestAnalyticsReportFlow(t *testing.T) {
	k := analyticsKit(t)
	r := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "analytics_report"), k.Env,
		`{"report":"discovery","from":"2026-10-15","dimensions":["Source Type"],"max_segments":2}`))
	got := tooltest.Decode[map[string]any](t, r)
	if got["report"].(map[string]any)["id"] != "rep-1" {
		t.Errorf("report = %v", got["report"])
	}
	if s := tooltest.JSON(t, got["instances"]); s != `[{"granularity":"DAILY","id":"inst-3","processing_date":"2026-10-21"},{"granularity":"DAILY","id":"inst-2","processing_date":"2026-10-20"}]` {
		t.Errorf("instances = %s (newest first, out-of-range dropped)", s)
	}
	if got["segments_fetched"] != 2.0 || got["segments_skipped"] != 1.0 {
		t.Errorf("segments fetched/skipped = %v/%v", got["segments_fetched"], got["segments_skipped"])
	}
	sum := got["summary"].(map[string]any)
	if s := tooltest.JSON(t, sum["metrics"]); s != `["Counts","Unique Counts"]` {
		t.Errorf("default metrics = %s (identifier column must be skipped)", s)
	}
	if s := tooltest.JSON(t, sum["totals"]); s != `{"Counts":"288","Unique Counts":"226"}` {
		t.Errorf("totals = %s", s)
	}
	groups := sum["groups"].([]any)
	if len(groups) != 3 || tooltest.JSON(t, groups[0]) != `{"key":{"Source Type":"App Store search"},"metrics":{"Counts":"245","Unique Counts":"190"},"rows":3}` {
		t.Errorf("groups = %s", tooltest.JSON(t, groups))
	}
	if len(k.Fetch.Calls) != 2 || !strings.Contains(strings.Join(r.Meta.Warnings, ";"), "segment cap 2") {
		t.Errorf("fetch calls = %v warnings = %v", k.Fetch.Calls, r.Meta.Warnings)
	}
	if ins := k.ASC.RequestsTo(http.MethodGet, "/v1/analyticsReports/rep-1/instances"); len(ins) != 1 || ins[0].Query.Get("filter[granularity]") != "DAILY" {
		t.Errorf("instance requests = %+v", ins)
	}
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/analyticsReportInstances/inst-1/segments")); n != 0 {
		t.Error("out-of-range instance fetched")
	}

	// Unknown column → invalid arguments listing the columns.
	body := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "analytics_report"), k.Env,
		`{"report":"rep-1","dimensions":["Nope"],"max_segments":1}`), "invalid_arguments")
	if !strings.Contains(body.Message, "Source Type") {
		t.Errorf("message = %q", body.Message)
	}
	// Ambiguous substring.
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "analytics_report"), k.Env, `{"report":"Standard"}`), "invalid_arguments")
}

func TestPctChangeAndWindows(t *testing.T) {
	for _, c := range []struct {
		cur, prev int64
		want      string
	}{{120, 100, "+20.0%"}, {50, 100, "-50.0%"}, {100, 100, "0.0%"}, {5, 0, ""}, {0, 3, "-100.0%"}} {
		if got := pctChange(big.NewRat(c.cur, 1), big.NewRat(c.prev, 1)); got != c.want {
			t.Errorf("pctChange(%d,%d) = %q, want %q", c.cur, c.prev, got, c.want)
		}
	}
	d := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.DateOnly, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct{ preset, from, to, pFrom, pTo string }{
		{"last_7_days", "2026-10-15", "2026-10-21", "2026-10-08", "2026-10-14"},
		{"yesterday", "2026-10-21", "2026-10-21", "2026-10-20", "2026-10-20"},
		{"this_week", "2026-10-19", "2026-10-22", "2026-10-12", "2026-10-15"},
		{"month_to_date", "2026-03-01", "2026-03-31", "2026-02-01", "2026-02-28"},
		{"month_to_date", "2026-10-01", "2026-10-22", "2026-09-01", "2026-09-22"},
		{"last_month", "2026-09-01", "2026-09-30", "2026-08-01", "2026-08-31"},
	} {
		pf, pt := previousWindow(c.preset, d(c.from), d(c.to))
		if pf.Format(time.DateOnly) != c.pFrom || pt.Format(time.DateOnly) != c.pTo {
			t.Errorf("%s %s..%s → %s..%s, want %s..%s", c.preset, c.from, c.to, pf.Format(time.DateOnly), pt.Format(time.DateOnly), c.pFrom, c.pTo)
		}
	}
}
