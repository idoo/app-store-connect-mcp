package insights

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const healthAppID = "1000000002"

// healthFixture describes one release health scenario.
type healthFixture struct {
	CurWeights, PrevWeights map[string]float64
	Regressions             int
	EmptyPerf               bool
	Crashes                 int
	RatingsNow, RatingsPrev []int
	Phased                  string
	ExtraSignatures         int
}

func jsonDoc(t testing.TB, data any, included ...map[string]any) string {
	t.Helper()
	m := map[string]any{"data": data, "links": map[string]string{"self": "https://api.appstoreconnect.apple.com/v1/x"}}
	if included != nil {
		m["included"] = included
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func rel(typ, id string) map[string]any {
	return map[string]any{"data": map[string]any{"type": typ, "id": id}}
}

// installHealthFixture registers every endpoint release_health_report reads.
func installHealthFixture(t testing.TB, k *tooltest.Kit, f healthFixture) {
	t.Helper()
	k.Clock.Set(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	k.Resolve.Apps = []tool.App{{ID: healthAppID, BundleID: "com.example.sail", Name: "Sail", SKU: "sail"}}
	curRels := map[string]any{"build": rel("builds", "b2")}
	var inc []map[string]any
	if f.Phased != "" {
		curRels["appStoreVersionPhasedRelease"] = rel("appStoreVersionPhasedReleases", "pr1")
		inc = append(inc, map[string]any{
			"type": "appStoreVersionPhasedReleases", "id": "pr1",
			"attributes": map[string]any{"phasedReleaseState": f.Phased, "currentDayNumber": 3},
		})
	}
	versions := []map[string]any{
		{
			"type": "appStoreVersions", "id": "v1", "attributes": map[string]any{"versionString": "1.0", "appVersionState": "REPLACED_WITH_NEW_VERSION", "createdDate": "2026-09-01T00:00:00Z"},
			"relationships": map[string]any{"build": rel("builds", "b1")},
		},
		{
			"type": "appStoreVersions", "id": "v2", "attributes": map[string]any{"versionString": "1.1", "appVersionState": "READY_FOR_DISTRIBUTION", "createdDate": "2026-10-10T00:00:00Z"},
			"relationships": curRels,
		},
	}
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appStoreVersions", http.StatusOK, jsonDoc(t, versions, inc...))
	sigs := func(build string, w map[string]float64, extra int) string {
		data := []map[string]any{}
		for typ, weight := range w {
			data = append(data, map[string]any{
				"type": "diagnosticSignatures", "id": build + typ,
				"attributes": map[string]any{"diagnosticType": typ, "signature": "sig " + typ, "weight": weight},
			})
		}
		for i := range extra {
			data = append(data, map[string]any{
				"type": "diagnosticSignatures", "id": fmt.Sprintf("%s-x%d", build, i),
				"attributes": map[string]any{"diagnosticType": "HANGS", "signature": "tiny", "weight": 0.0},
			})
		}
		return jsonDoc(t, data)
	}
	k.ASC.Handle(http.MethodGet, "/v1/builds/b2/diagnosticSignatures", http.StatusOK, sigs("b2", f.CurWeights, f.ExtraSignatures))
	k.ASC.Handle(http.MethodGet, "/v1/builds/b1/diagnosticSignatures", http.StatusOK, sigs("b1", f.PrevWeights, 0))
	perf := map[string]any{"productData": []any{map[string]any{"platform": "IOS"}}, "insights": map[string]any{"regressions": []any{}}}
	if f.EmptyPerf {
		perf["productData"] = []any{}
	}
	regs := []any{}
	for i := range f.Regressions {
		regs = append(regs, map[string]any{"metric": fmt.Sprintf("m%d", i), "summaryString": "worse"})
	}
	perf["insights"] = map[string]any{"regressions": regs}
	pb, _ := json.Marshal(perf)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/perfPowerMetrics", http.StatusOK, string(pb))
	crashes := []map[string]any{}
	for i := range f.Crashes {
		crashes = append(crashes, map[string]any{"type": "betaFeedbackCrashSubmissions", "id": fmt.Sprintf("c%d", i), "attributes": map[string]any{}})
	}
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/betaFeedbackCrashSubmissions", http.StatusOK, jsonDoc(t, crashes))
	reviews := []map[string]any{}
	for i, r := range f.RatingsNow {
		reviews = append(reviews, map[string]any{"type": "customerReviews", "id": fmt.Sprintf("n%d", i), "attributes": map[string]any{"rating": r, "createdDate": "2026-10-15T00:00:00Z"}})
	}
	for i, r := range f.RatingsPrev {
		reviews = append(reviews, map[string]any{"type": "customerReviews", "id": fmt.Sprintf("p%d", i), "attributes": map[string]any{"rating": r, "createdDate": "2026-09-15T00:00:00Z"}})
	}
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/customerReviews", http.StatusOK, jsonDoc(t, reviews))
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/analyticsReportRequests", http.StatusOK, jsonDoc(t, []any{}))
}

var (
	healthyFixture = healthFixture{
		CurWeights: map[string]float64{"HANGS": 1, "LAUNCHES": 1}, PrevWeights: map[string]float64{"HANGS": 1, "LAUNCHES": 1},
		RatingsNow: []int{5, 5}, RatingsPrev: []int{5, 4},
	}
	watchFixture = healthFixture{
		CurWeights: map[string]float64{"HANGS": 1.5}, PrevWeights: map[string]float64{"HANGS": 1},
		Crashes: 4, RatingsNow: []int{5}, RatingsPrev: []int{5},
	}
	breachFixture = healthFixture{
		CurWeights: map[string]float64{"HANGS": 3, "DISK_WRITES": 1}, PrevWeights: map[string]float64{"HANGS": 1, "DISK_WRITES": 1},
		Regressions: 2, RatingsNow: []int{1, 2}, RatingsPrev: []int{5, 5}, Phased: "ACTIVE",
	}
)

func healthCall(t *testing.T, k *tooltest.Kit, args string) (*tool.Result, HealthReport) {
	t.Helper()
	tl, ok := tool.Lookup("release_health_report")
	if !ok {
		t.Fatal("release_health_report not registered")
	}
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args))
	return res, tooltest.Decode[HealthReport](t, res)
}

func TestReleaseHealthVerdicts(t *testing.T) {
	for _, c := range []struct {
		name    string
		f       healthFixture
		verdict string
	}{
		{"healthy", healthyFixture, VerdictHealthy},
		{"watch", watchFixture, VerdictWatch},
		{"breach", breachFixture, VerdictBreach},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := tooltest.New(t).ReadOnly()
			k.Env.Artifacts = &funnelArtifacts{puts: map[string][]byte{}}
			installHealthFixture(t, k, c.f)
			res, out := healthCall(t, k, `{"app":"Sail"}`)
			if out.Verdict != c.verdict {
				t.Fatalf("verdict %s, want %s; reasons %v", out.Verdict, c.verdict, out.Reasons)
			}
			if out.Version != "1.1" || out.Previous != "1.0" {
				t.Errorf("versions %q vs %q", out.Version, out.Previous)
			}
			if c.verdict != VerdictHealthy && len(out.Reasons) == 0 {
				t.Error("no reasons")
			}
			pause := false
			for _, n := range res.Next {
				if n.Tool == "release_phased" {
					pause = n.Args["action"] == "pause"
				}
			}
			if pause != (c.verdict == VerdictBreach) {
				t.Errorf("pause next = %v, next %+v", pause, res.Next)
			}
			if len(k.ASC.Mutations()) != 0 {
				t.Errorf("mutations: %+v", k.ASC.Mutations())
			}
		})
	}
}

func TestReleaseHealthUnknownWithoutProductionData(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{EmptyPerf: true, Crashes: 1, Phased: "ACTIVE"})
	res, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.Verdict != VerdictUnknown || len(out.Reasons) == 0 || out.Reasons[0] != "no production data yet" {
		t.Fatalf("verdict %s reasons %v", out.Verdict, out.Reasons)
	}
	want := map[string]string{
		"diagnostics": covNoData, "perf_power_metrics": covNoData, "analytics_crashes": covNotSetUp,
		"testflight_crash_feedback": covMeasured, "ratings": covNoData,
	}
	for src, st := range want {
		if out.Coverage[src] != st {
			t.Errorf("coverage[%s] = %q, want %q (all %v)", src, out.Coverage[src], st, out.Coverage)
		}
	}
	setup := false
	for _, n := range res.Next {
		setup = setup || n.Tool == "analytics_setup"
		if n.Tool == "release_phased" {
			t.Errorf("pause suggested on unknown: %+v", n)
		}
	}
	if !setup {
		t.Errorf("no analytics_setup next: %+v", res.Next)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Errorf("mutations: %+v", k.ASC.Mutations())
	}
}

func TestReleaseHealthBreachPausedPhasedNoPauseNext(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	f := breachFixture
	f.Phased = "PAUSED"
	installHealthFixture(t, k, f)
	res, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.Verdict != VerdictBreach {
		t.Fatalf("verdict %s", out.Verdict)
	}
	for _, n := range res.Next {
		if n.Tool == "release_phased" {
			t.Errorf("pause suggested for a paused release: %+v", n)
		}
	}
}

func TestReleaseHealthAnalyticsNotSetUpAndAccept(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthyFixture)
	res, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.Analytics.Status != "not_set_up" {
		t.Errorf("analytics status %q", out.Analytics.Status)
	}
	found := false
	for _, n := range res.Next {
		found = found || n.Tool == "analytics_setup"
	}
	if !found {
		t.Errorf("no analytics_setup next: %+v", res.Next)
	}
	reqs := k.ASC.RequestsTo(http.MethodGet, "/v1/apps/{id}/perfPowerMetrics")
	if len(reqs) != 1 || reqs[0].Accept != xcodeMetricsAccept {
		t.Errorf("perf requests %+v", reqs)
	}
}

func TestReleaseHealthEmptyPerfInsufficient(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	f := healthyFixture
	f.EmptyPerf = true
	installHealthFixture(t, k, f)
	_, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.Perf.Status != "insufficient_data" || out.Verdict != VerdictHealthy {
		t.Errorf("perf %+v verdict %s", out.Perf, out.Verdict)
	}
}

func TestReleaseHealthLargeSignaturesLinked(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	art := &funnelArtifacts{puts: map[string][]byte{}}
	k.Env.Artifacts = art
	f := healthyFixture
	f.ExtraSignatures = 20
	installHealthFixture(t, k, f)
	res, out := healthCall(t, k, `{"app":"Sail"}`)
	if len(res.Links) != 1 || len(art.puts) != 1 {
		t.Fatalf("links %+v puts %d", res.Links, len(art.puts))
	}
	if n := len(out.Diagnostics.Current.Top); n != maxInlineSignatures || out.Diagnostics.Current.Count != 22 {
		t.Errorf("inline %d count %d", n, out.Diagnostics.Current.Count)
	}
	for _, b := range art.puts {
		if !strings.Contains(string(b), "tiny") {
			t.Errorf("artifact lacks signatures: %s", b)
		}
	}
}

func TestEvaluateHealthTable(t *testing.T) {
	i := func(n int) *int { return &n }
	f := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		name string
		s    HealthSignals
		want string
	}{
		{"empty", HealthSignals{}, VerdictHealthy},
		{"new type", HealthSignals{Weights: map[string][2]float64{"HANGS": {1, 0}}}, VerdictWatch},
		{"sig watch", HealthSignals{Weights: map[string][2]float64{"HANGS": {1.3, 1}}}, VerdictWatch},
		{"sig breach", HealthSignals{Weights: map[string][2]float64{"HANGS": {2, 1}}}, VerdictBreach},
		{"one regression", HealthSignals{Regressions: i(1)}, VerdictWatch},
		{"two regressions", HealthSignals{Regressions: i(2)}, VerdictBreach},
		{"crash watch", HealthSignals{CrashReports: i(3)}, VerdictWatch},
		{"crash breach", HealthSignals{CrashReports: i(10)}, VerdictBreach},
		{"rating only now", HealthSignals{RatingNow: f(1)}, VerdictHealthy},
		{"rating watch", HealthSignals{RatingNow: f(4.5), RatingBefore: f(4.9)}, VerdictWatch},
		{"rating breach", HealthSignals{RatingNow: f(3.5), RatingBefore: f(4.9)}, VerdictBreach},
		{"breach wins", HealthSignals{CrashReports: i(3), Regressions: i(5)}, VerdictBreach},
	} {
		if got, _ := EvaluateHealth(c.s, HealthThresholds{}); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if got, _ := EvaluateHealth(HealthSignals{CrashReports: i(3)}, HealthThresholds{CrashWatch: 5, CrashBreach: 9}); got != VerdictHealthy {
		t.Errorf("custom thresholds: %s", got)
	}
}
