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

// healthFixture describes one release health scenario. The live version 1.1
// was created 2026-10-10 (the default, approximate window start); 1.0 on
// 2026-09-01.
type healthFixture struct {
	CurWeights, PrevWeights map[string]float64
	Regressions             int
	EmptyPerf               bool
	// Crashes are TestFlight crashes dated after the window start;
	// CrashesBefore are older ones on the same build.
	Crashes, CrashesBefore  int
	RatingsNow, RatingsPrev []int
	Phased                  string
	// PhasedStart is the phased release startDate.
	PhasedStart     string
	ExtraSignatures int
	// NoLive serves no READY_FOR_DISTRIBUTION version.
	NoLive bool
}

// ratingsN repeats rating n times.
func ratingsN(rating, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = rating
	}
	return out
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
		attrs := map[string]any{"phasedReleaseState": f.Phased, "currentDayNumber": 3}
		if f.PhasedStart != "" {
			attrs["startDate"] = f.PhasedStart
		}
		inc = append(inc, map[string]any{"type": "appStoreVersionPhasedReleases", "id": "pr1", "attributes": attrs})
	}
	liveState := "READY_FOR_DISTRIBUTION"
	if f.NoLive {
		liveState = "PENDING_DEVELOPER_RELEASE"
	}
	versions := []map[string]any{
		{
			"type": "appStoreVersions", "id": "v1", "attributes": map[string]any{"versionString": "1.0", "appVersionState": "REPLACED_WITH_NEW_VERSION", "createdDate": "2026-09-01T00:00:00Z"},
			"relationships": map[string]any{"build": rel("builds", "b1")},
		},
		{
			"type": "appStoreVersions", "id": "v2", "attributes": map[string]any{"versionString": "1.1", "appVersionState": liveState, "createdDate": "2026-10-10T00:00:00Z"},
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
		crashes = append(crashes, map[string]any{"type": "betaFeedbackCrashSubmissions", "id": fmt.Sprintf("c%d", i), "attributes": map[string]any{"createdDate": "2026-10-15T00:00:00Z"}})
	}
	for i := range f.CrashesBefore {
		crashes = append(crashes, map[string]any{"type": "betaFeedbackCrashSubmissions", "id": fmt.Sprintf("o%d", i), "attributes": map[string]any{"createdDate": "2026-10-01T00:00:00Z"}})
	}
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/betaFeedbackCrashSubmissions", http.StatusOK, jsonDoc(t, crashes))
	k.ASC.Handle(http.MethodGet, "/v1/reviewSubmissions", http.StatusOK, jsonDoc(t, []any{}))
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

// pauseNext reports whether res suggests release_phased pause.
func pauseNext(res *tool.Result) bool {
	for _, n := range res.Next {
		if n.Tool == "release_phased" && n.Args["action"] == "pause" {
			return true
		}
	}
	return false
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
			// The breach comes from diagnostics, so it is pausing.
			if p := pauseNext(res); p != (c.verdict == VerdictBreach) {
				t.Errorf("pause next = %v, next %+v", p, res.Next)
			}
			if len(k.ASC.Mutations()) != 0 {
				t.Errorf("mutations: %+v", k.ASC.Mutations())
			}
		})
	}
}

// A macOS-only app and a multi-platform app whose only live version is on the
// Mac are measured on MAC_OS, and the pause step targets MAC_OS.
func TestReleaseHealthInfersPlatform(t *testing.T) {
	for _, c := range []struct {
		name      string
		platforms []tool.Platform
	}{
		{"mac_only", []tool.Platform{tool.MacOS}},
		{"mac_live_only", []tool.Platform{tool.IOS, tool.MacOS}},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := tooltest.New(t).ReadOnly()
			k.Env.Artifacts = &funnelArtifacts{puts: map[string][]byte{}}
			k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appStoreVersions?filter[platform]=IOS", http.StatusOK, jsonDoc(t, []any{}))
			installHealthFixture(t, k, breachFixture)
			k.Resolve.Apps[0].Platforms = c.platforms
			res, out := healthCall(t, k, `{"app":"Sail"}`)
			if out.Platform != tool.MacOS || out.Version != "1.1" || out.Verdict != VerdictBreach {
				t.Fatalf("platform %s version %q verdict %s; reasons %v", out.Platform, out.Version, out.Verdict, out.Reasons)
			}
			found := false
			for _, n := range res.Next {
				if n.Tool == "release_phased" {
					found = n.Args["platform"] == "MAC_OS"
				}
			}
			if !found {
				t.Errorf("pause step not on MAC_OS: %+v", res.Next)
			}
		})
	}
}

func TestReleaseHealthUnknownWithoutProductionData(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{EmptyPerf: true, Crashes: 1, Phased: "ACTIVE"})
	res, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.Verdict != VerdictUnknown || len(out.Reasons) == 0 || out.Reasons[0].Level != VerdictUnknown || out.Reasons[0].Source != SourceDiagnostics {
		t.Fatalf("verdict %s reasons %v", out.Verdict, out.Reasons)
	}
	want := map[string]string{
		SourceDiagnostics: covNoData, SourcePerf: covNoData, SourceAnalytics: covNotSetUp,
		SourceTestFlight: covMeasured, SourceRatings: covNoData,
	}
	for src, st := range want {
		if out.Coverage[src] != st {
			t.Errorf("coverage[%s] = %q, want %q (all %v)", src, out.Coverage[src], st, out.Coverage)
		}
	}
	setup := false
	for _, n := range res.Next {
		setup = setup || n.Tool == "analytics_setup"
	}
	if pauseNext(res) {
		t.Errorf("pause suggested on unknown: %+v", res.Next)
	}
	if !setup {
		t.Errorf("no analytics_setup next: %+v", res.Next)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Errorf("mutations: %+v", k.ASC.Mutations())
	}
}

// Analytics crashes and perf metrics are informational: data from them alone
// never makes the release measured.
func TestReleaseHealthAnalyticsAndPerfNotProduction(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{Regressions: 3})
	_, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.Coverage[SourcePerf] != covMeasured {
		t.Fatalf("perf coverage %q", out.Coverage[SourcePerf])
	}
	if out.Verdict != VerdictUnknown {
		t.Errorf("verdict %s, want unknown with only perf data; reasons %v", out.Verdict, out.Reasons)
	}
	for _, r := range out.Reasons {
		if r.Source == SourcePerf && (r.Level != LevelInfo || r.Pausing) {
			t.Errorf("perf reason not informational: %+v", r)
		}
	}
	if len(out.ProductionSources) != 1 || out.ProductionSources[0] != SourceDiagnostics {
		t.Errorf("production sources %v", out.ProductionSources)
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
	if pauseNext(res) {
		t.Errorf("pause suggested for a paused release: %+v", res.Next)
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
	const n = MinReviewsPerWindow
	for _, c := range []struct {
		name string
		s    HealthSignals
		want string
	}{
		{"empty", HealthSignals{}, VerdictHealthy},
		{"new type", HealthSignals{Weights: map[string][2]float64{"HANGS": {1, 0}}}, VerdictWatch},
		{"sig watch", HealthSignals{Weights: map[string][2]float64{"HANGS": {1.3, 1}}}, VerdictWatch},
		{"sig breach", HealthSignals{Weights: map[string][2]float64{"HANGS": {2, 1}}}, VerdictBreach},
		{"regressions informational", HealthSignals{Regressions: i(5)}, VerdictHealthy},
		{"crash watch", HealthSignals{CrashReports: i(3)}, VerdictWatch},
		{"crash breach", HealthSignals{CrashReports: i(10)}, VerdictBreach},
		{"rating only now", HealthSignals{RatingNow: f(1), RatingCountNow: n}, VerdictHealthy},
		{"rating watch", HealthSignals{RatingNow: f(4.5), RatingBefore: f(4.9), RatingCountNow: n, RatingCountBefore: n}, VerdictWatch},
		{"rating breach", HealthSignals{RatingNow: f(3.5), RatingBefore: f(4.9), RatingCountNow: n, RatingCountBefore: n}, VerdictBreach},
		{"rating below floor", HealthSignals{RatingNow: f(1), RatingBefore: f(5), RatingCountNow: n - 1, RatingCountBefore: n}, VerdictHealthy},
		{"rating approximate window", HealthSignals{RatingNow: f(1), RatingBefore: f(5), RatingCountNow: n, RatingCountBefore: n, ApproximateWindow: true}, VerdictHealthy},
		{"breach wins", HealthSignals{CrashReports: i(3), Weights: map[string][2]float64{"HANGS": {5, 1}}}, VerdictBreach},
	} {
		if got, _ := EvaluateHealth(c.s, HealthThresholds{}); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if got, _ := EvaluateHealth(HealthSignals{CrashReports: i(3)}, HealthThresholds{CrashWatch: 5, CrashBreach: 9}); got != VerdictHealthy {
		t.Errorf("custom thresholds: %s", got)
	}
}

func TestEvaluateHealthPausingOnlyDiagnostics(t *testing.T) {
	i := func(n int) *int { return &n }
	f := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		name    string
		s       HealthSignals
		pausing bool
	}{
		{"diagnostics breach", HealthSignals{Weights: map[string][2]float64{"HANGS": {3, 1}}}, true},
		{"diagnostics watch", HealthSignals{Weights: map[string][2]float64{"HANGS": {1.3, 1}}}, false},
		{"testflight breach", HealthSignals{CrashReports: i(50)}, false},
		{"ratings breach", HealthSignals{RatingNow: f(1), RatingBefore: f(5), RatingCountNow: 30, RatingCountBefore: 30}, false},
	} {
		v, rs := EvaluateHealth(c.s, HealthThresholds{})
		if got := AnyPausing(rs); got != c.pausing {
			t.Errorf("%s: verdict %s pausing %v, want %v (%+v)", c.name, v, got, c.pausing, rs)
		}
	}
}

// steady is a current build with measured, unchanged diagnostics.
var steady = map[string]float64{"HANGS": 1}

// IQ-1: one review per window never breaches or pauses.
func TestHealthOneReviewNeverBreaches(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{
		CurWeights: steady, PrevWeights: steady,
		RatingsNow: []int{1}, RatingsPrev: []int{5}, Phased: "ACTIVE", PhasedStart: "2026-10-11T00:00:00Z",
	})
	res, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.Verdict == VerdictBreach || out.Verdict == VerdictWatch || pauseNext(res) {
		t.Fatalf("verdict %s reasons %v next %+v", out.Verdict, out.Reasons, res.Next)
	}
	if out.Ratings.Status != "insufficient_data" {
		t.Errorf("ratings status %q", out.Ratings.Status)
	}
}

// IQ-1: a real rating drop is reported, but ratings are never pausing.
func TestHealthRatingBreachNeverPauses(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{
		CurWeights: steady, PrevWeights: steady,
		RatingsNow: ratingsN(1, 25), RatingsPrev: ratingsN(5, 25), Phased: "ACTIVE", PhasedStart: "2026-10-11T00:00:00Z",
	})
	res, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.Verdict != VerdictBreach || AnyPausing(out.Reasons) || pauseNext(res) {
		t.Fatalf("verdict %s reasons %+v next %+v", out.Verdict, out.Reasons, res.Next)
	}
}

// IQ-7: with an approximate window start the same drop is informational.
func TestHealthApproximateWindowRatingsInformational(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{
		CurWeights: steady, PrevWeights: steady,
		RatingsNow: ratingsN(1, 25), RatingsPrev: ratingsN(5, 25),
	})
	_, out := healthCall(t, k, `{"app":"Sail"}`)
	if !out.WindowApproximate || out.WindowStartSource != windowCreated {
		t.Fatalf("window %s approximate=%v", out.WindowStartSource, out.WindowApproximate)
	}
	if out.Verdict != VerdictHealthy {
		t.Errorf("verdict %s reasons %+v", out.Verdict, out.Reasons)
	}
}

// IQ-2: current build without signatures vs previous HANGS 5 is unknown,
// never a −100 % improvement.
func TestHealthNoCurrentSignaturesUnknown(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{PrevWeights: map[string]float64{"HANGS": 5}, Phased: "ACTIVE"})
	res, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.Verdict != VerdictUnknown || out.Coverage[SourceDiagnostics] != covNoData || pauseNext(res) {
		t.Fatalf("verdict %s coverage %v reasons %v", out.Verdict, out.Coverage, out.Reasons)
	}
	if out.Diagnostics.DeltaPct != nil {
		t.Errorf("delta computed without current data: %v", out.Diagnostics.DeltaPct)
	}
}

// IQ-8: crashes before the window start are not counted.
func TestHealthCrashesBeforeWindowIgnored(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{CurWeights: steady, PrevWeights: steady, CrashesBefore: 10})
	_, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.CrashFeedback.Count != 0 || out.Verdict != VerdictHealthy {
		t.Fatalf("count %d verdict %s reasons %v", out.CrashFeedback.Count, out.Verdict, out.Reasons)
	}
}

// IQ-8: a TestFlight-only breach is non-pausing.
func TestHealthTestFlightBreachNonPausing(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{CurWeights: steady, PrevWeights: steady, Crashes: 25, CrashesBefore: 5, Phased: "ACTIVE"})
	res, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.Verdict != VerdictBreach || out.CrashFeedback.Count != 25 {
		t.Fatalf("verdict %s count %d", out.Verdict, out.CrashFeedback.Count)
	}
	for _, r := range out.Reasons {
		if r.Source == SourceTestFlight && r.Pausing {
			t.Errorf("pausing TestFlight reason %+v", r)
		}
	}
	if AnyPausing(out.Reasons) || pauseNext(res) {
		t.Errorf("pause offered: %+v %+v", out.Reasons, res.Next)
	}
}

// IQ-10: no live version is unknown, not an error.
func TestHealthNoLiveVersionUnknown(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{NoLive: true})
	_, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.Verdict != VerdictUnknown || len(out.Reasons) != 1 || out.Reasons[0].Message != "no live version on IOS" {
		t.Fatalf("verdict %s reasons %+v", out.Verdict, out.Reasons)
	}
}

// IQ-7: the window start chain prefers the phased release start.
func TestHealthWindowStartPhased(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{CurWeights: steady, PrevWeights: steady, Phased: "ACTIVE", PhasedStart: "2026-10-16T00:00:00Z", Crashes: 4})
	_, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.WindowStartSource != windowPhased || out.WindowApproximate || out.WindowStart != "2026-10-16T00:00:00Z" {
		t.Fatalf("window %s %s approximate=%v", out.WindowStart, out.WindowStartSource, out.WindowApproximate)
	}
	// The fixture's crashes (2026-10-15) predate the phased start.
	if out.CrashFeedback.Count != 0 {
		t.Errorf("crash count %d", out.CrashFeedback.Count)
	}
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/reviewSubmissions")); n != 0 {
		t.Errorf("%d review submission lookups after a phased start", n)
	}
}

// IQ-7: without phased or scheduled dates, the completed review submission
// of the version is used, flagged approximate.
func TestHealthWindowStartReviewSubmission(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{CurWeights: steady, PrevWeights: steady})
	// The query-constrained route outranks the fixture's empty list.
	k.ASC.Handle(http.MethodGet, "/v1/reviewSubmissions?filter[state]=COMPLETE", http.StatusOK, jsonDoc(t, []map[string]any{
		{"type": "reviewSubmissions", "id": "rs0", "attributes": map[string]any{"state": "COMPLETE", "submittedDate": "2026-10-18T00:00:00Z"}, "relationships": map[string]any{"appStoreVersionForReview": rel("appStoreVersions", "v9")}},
		{"type": "reviewSubmissions", "id": "rs1", "attributes": map[string]any{"state": "COMPLETE", "submittedDate": "2026-10-12T00:00:00Z"}, "relationships": map[string]any{"appStoreVersionForReview": rel("appStoreVersions", "v2")}},
	}))
	_, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.WindowStartSource != windowSubmission || !out.WindowApproximate || out.WindowStart != "2026-10-12T00:00:00Z" {
		t.Fatalf("window %s %s approximate=%v", out.WindowStart, out.WindowStartSource, out.WindowApproximate)
	}
}
