package insights

import (
	"net/http"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// steady is a current build with measured, unchanged diagnostics.
var steady = map[string]float64{"HANGS": 1}

// IQ-1: one review per window never breaches or pauses.
func TestHealthR5OneReviewNeverBreaches(t *testing.T) {
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
func TestHealthR5RatingBreachNeverPauses(t *testing.T) {
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
func TestHealthR5ApproximateWindowRatingsInformational(t *testing.T) {
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
func TestHealthR5NoCurrentSignaturesUnknown(t *testing.T) {
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
func TestHealthR5CrashesBeforeWindowIgnored(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{CurWeights: steady, PrevWeights: steady, CrashesBefore: 10})
	_, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.CrashFeedback.Count != 0 || out.Verdict != VerdictHealthy {
		t.Fatalf("count %d verdict %s reasons %v", out.CrashFeedback.Count, out.Verdict, out.Reasons)
	}
}

// IQ-8: a TestFlight-only breach is non-pausing.
func TestHealthR5TestFlightBreachNonPausing(t *testing.T) {
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
func TestHealthR5NoLiveVersionUnknown(t *testing.T) {
	k := tooltest.New(t).ReadOnly()
	installHealthFixture(t, k, healthFixture{NoLive: true})
	_, out := healthCall(t, k, `{"app":"Sail"}`)
	if out.Verdict != VerdictUnknown || len(out.Reasons) != 1 || out.Reasons[0].Message != "no live version on IOS" {
		t.Fatalf("verdict %s reasons %+v", out.Verdict, out.Reasons)
	}
}

// IQ-7: the window start chain prefers the phased release start.
func TestHealthR5WindowStartPhased(t *testing.T) {
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
func TestHealthR5WindowStartReviewSubmission(t *testing.T) {
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
