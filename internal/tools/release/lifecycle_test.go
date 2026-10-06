package release

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// writableKit is a writable kit with one iOS app.
func writableKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t)
	k.Resolve.Apps = []tool.App{{ID: "100", Name: "Hjul", BundleID: "se.jvcr.hjul", Platforms: []tool.Platform{tool.IOS}}}
	return k
}

// versionsFixture serves the app's versions: an old live 1.3 and v1 (1.4) in
// state, with a phased release in phasedState ("" = none).
func versionsFixture(t *testing.T, k *tooltest.Kit, state, phasedState string, day int) {
	t.Helper()
	v1 := jres{Type: "appStoreVersions", ID: "v1", Attrs: map[string]any{"versionString": "1.4", "platform": "IOS", "appVersionState": state, "createdDate": "2026-01-01T00:00:00Z"}}
	var inc []jres
	if phasedState != "" {
		v1.Rels = map[string]any{"appStoreVersionPhasedRelease": "appStoreVersionPhasedReleases:pr1"}
		inc = append(inc, jres{Type: "appStoreVersionPhasedReleases", ID: "pr1", Attrs: map[string]any{"phasedReleaseState": phasedState, "currentDayNumber": day, "totalPauseDuration": 0, "startDate": "2026-01-01T00:00:00Z"}})
	}
	old := jres{Type: "appStoreVersions", ID: "v0", Attrs: map[string]any{"versionString": "1.3", "platform": "IOS", "appVersionState": "READY_FOR_DISTRIBUTION", "createdDate": "2025-06-01T00:00:00Z"}}
	k.ASC.Handle("GET", "/v1/apps/{id}/appStoreVersions", 200, doc(t, []jres{old, v1}, inc...))
}

func preview(t *testing.T, k *tooltest.Kit, name, args string) {
	t.Helper()
	got := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, name), k.Env, args)))
	if got["status"] != tool.ErrNeedsConfirm.Code || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("preview = %+v, mutations %d", got, len(k.ASC.Mutations()))
	}
}

func TestReleaseReviewStatusWithItems(t *testing.T) {
	k := writableKit(t)
	k.ASC.Handle("GET", "/v1/reviewSubmissions", 200, doc(t, []jres{
		{
			Type: "reviewSubmissions", ID: "rs0", Attrs: map[string]any{"platform": "IOS", "state": "COMPLETE", "submittedDate": "2025-06-01T00:00:00Z"},
			Rels: map[string]any{"items": []string{"reviewSubmissionItems:i0"}, "appStoreVersionForReview": "appStoreVersions:v0"},
		},
		{
			Type: "reviewSubmissions", ID: "rs1", Attrs: map[string]any{"platform": "IOS", "state": "UNRESOLVED_ISSUES", "submittedDate": "2026-01-02T00:00:00Z"},
			Rels: map[string]any{"items": []string{"reviewSubmissionItems:i1"}, "appStoreVersionForReview": "appStoreVersions:v1"},
		},
	},
		jres{Type: "reviewSubmissionItems", ID: "i0", Attrs: map[string]any{"state": "APPROVED"}},
		jres{Type: "reviewSubmissionItems", ID: "i1", Attrs: map[string]any{"state": "REJECTED"}},
		jres{Type: "appStoreVersions", ID: "v0", Attrs: map[string]any{"versionString": "1.3", "appVersionState": "READY_FOR_DISTRIBUTION"}},
		jres{Type: "appStoreVersions", ID: "v1", Attrs: map[string]any{"versionString": "1.4", "appVersionState": "REJECTED"}},
	))
	k.ASC.Handle("GET", "/v1/reviewSubmissions/{id}/items", 200, doc(t, []jres{
		{Type: "reviewSubmissionItems", ID: "i1", Attrs: map[string]any{"state": "REJECTED"}, Rels: map[string]any{"appStoreVersion": "appStoreVersions:v1"}},
	}))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_status"), k.Env, `{"app":"Hjul"}`))
	out := tooltest.Decode[reviewStatusOut](t, res)
	if out.Current == nil || out.Current.ID != "rs1" || out.Current.Version == nil || out.Current.Version.State != "REJECTED" {
		t.Fatalf("current = %+v", out.Current)
	}
	if len(out.Current.Items) != 1 || out.Current.Items[0].Kind != kindAppStoreVersion || out.Current.Items[0].Target != "v1" {
		t.Errorf("items = %+v", out.Current.Items)
	}
	if len(out.History) != 1 || out.History[0].ID != "rs0" || out.History[0].ItemStates["APPROVED"] != 1 {
		t.Errorf("history = %+v", out.History)
	}
	if out.Rejection == nil || !strings.HasSuffix(out.Rejection.ResolutionCenter, "/apps/100/distribution/reviewsubmissions") || len(out.Rejection.RejectedItems) != 1 {
		t.Errorf("rejection = %+v", out.Rejection)
	}
	if len(res.Next) != 2 || res.Next[1].Tool != "release_submit" {
		t.Errorf("next = %+v", res.Next)
	}
	if n := len(k.ASC.Requests()); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

func TestReleaseReviewStatusNone(t *testing.T) {
	k := writableKit(t)
	k.ASC.Handle("GET", "/v1/reviewSubmissions", 200, `{"data":[]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_status"), k.Env, `{}`))
	if out := tooltest.Decode[reviewStatusOut](t, res); out.Current != nil || out.Rejection != nil {
		t.Errorf("out = %+v", out)
	}
}

func TestReleasePublishWrongStateRefused(t *testing.T) {
	k := writableKit(t)
	versionsFixture(t, k, "PREPARE_FOR_SUBMISSION", "", 0)
	res := tooltest.Call(t, mustTool(t, "release_publish"), k.Env, `{"app":"Hjul","confirm":true}`)
	body := tooltest.MustFail(t, res, "invalid_state")
	if !strings.Contains(body.Message, "PREPARE_FOR_SUBMISSION") {
		t.Errorf("message = %q", body.Message)
	}
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Errorf("%d mutations", n)
	}
}

func TestReleasePublishPosts(t *testing.T) {
	k := writableKit(t)
	versionsFixture(t, k, "PENDING_DEVELOPER_RELEASE", "INACTIVE", 0)
	k.ASC.Handle("POST", "/v1/appStoreVersionReleaseRequests", 201, doc(t, jres{Type: "appStoreVersionReleaseRequests", ID: "rr1", Attrs: map[string]any{}}))
	preview(t, k, "release_publish", `{"app":"Hjul"}`)
	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_publish"), k.Env, `{"app":"Hjul","confirm":true}`))
	out := tooltest.Decode[publishOut](t, res)
	if out.Action != "release_requested" || out.Version.ID != "v1" || out.Phased == nil || out.Phased.State != "INACTIVE" {
		t.Errorf("out = %+v", out)
	}
	muts := k.ASC.Mutations()
	if len(muts) != 1 || muts[0].Method != "POST" || muts[0].Path != "/v1/appStoreVersionReleaseRequests" {
		t.Fatalf("mutations = %+v", mutationLog(k))
	}
	want := `{"data":{"relationships":{"appStoreVersion":{"data":{"id":"v1","type":"appStoreVersions"}}},"type":"appStoreVersionReleaseRequests"}}`
	if got := tooltest.JSON(t, muts[0].Body); got != want {
		t.Errorf("body = %s", got)
	}
}

func TestReleasePublishAlreadyReleased(t *testing.T) {
	k := writableKit(t)
	versionsFixture(t, k, "READY_FOR_DISTRIBUTION", "", 0)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_publish"), k.Env, `{"app":"Hjul","confirm":true}`))
	if got := tooltest.Decode[map[string]any](t, res); got["already"] != true {
		t.Errorf("out = %+v", got)
	}
}

func TestReleasePublishExplicitVersion(t *testing.T) {
	k := writableKit(t)
	k.Resolve.Versions = map[tool.VersionRef]tool.Version{"1.4": {ID: "v1", String: "1.4", Platform: tool.IOS, State: "WAITING_FOR_REVIEW"}}
	k.ASC.Handle("GET", "/v1/appStoreVersions/{id}", 200, doc(t, jres{Type: "appStoreVersions", ID: "v1", Attrs: map[string]any{"versionString": "1.4", "appVersionState": "WAITING_FOR_REVIEW"}}))
	body := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "release_publish"), k.Env, `{"version":"1.4","confirm":true}`), "invalid_state")
	if !strings.Contains(body.Message, "WAITING_FOR_REVIEW") || len(body.Next) != 1 || body.Next[0].Tool != "release_review_status" {
		t.Errorf("body = %+v", body)
	}
}

func TestReleasePhasedTransitions(t *testing.T) {
	cases := []struct {
		action, from, to string
	}{
		{"pause", "ACTIVE", "PAUSED"},
		{"resume", "PAUSED", "ACTIVE"},
		{"complete", "ACTIVE", "COMPLETE"},
	}
	for _, c := range cases {
		t.Run(c.action, func(t *testing.T) {
			k := writableKit(t)
			versionsFixture(t, k, "READY_FOR_DISTRIBUTION", c.from, 4)
			k.ASC.Handle("PATCH", "/v1/appStoreVersionPhasedReleases/{id}", 200, doc(t, jres{Type: "appStoreVersionPhasedReleases", ID: "pr1", Attrs: map[string]any{"phasedReleaseState": c.to, "currentDayNumber": 4, "totalPauseDuration": 0}}))
			args := `{"app":"Hjul","action":"` + c.action + `"`
			preview(t, k, "release_phased", args+`}`)
			if c.action == "complete" {
				k.Elicit(true)
			}
			out := tooltest.Decode[phasedOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_phased"), k.Env, args+`,"confirm":true}`)))
			if out.Before == nil || out.Before.State != c.from || out.Before.CurrentDay != 4 || out.After == nil || out.After.State != c.to {
				t.Errorf("out = %+v before %+v after %+v", out, out.Before, out.After)
			}
			if c.from == "ACTIVE" && out.Before.Percent != 10 {
				t.Errorf("percent = %d, want 10 on day 4", out.Before.Percent)
			}
			muts := k.ASC.Mutations()
			want := `{"data":{"attributes":{"phasedReleaseState":"` + c.to + `"},"id":"pr1","type":"appStoreVersionPhasedReleases"}}`
			if len(muts) != 1 || muts[0].Path != "/v1/appStoreVersionPhasedReleases/pr1" || tooltest.JSON(t, muts[0].Body) != want {
				t.Errorf("mutations = %+v", mutationLog(k))
			}
		})
	}
}

// complete pushes the version to 100% of users: it needs a human, while pause
// and resume (reversible) proceed on the agent's confirm:true alone.
func TestReleasePhasedCompleteNeedsHuman(t *testing.T) {
	for _, c := range []struct {
		name   string
		elicit func(k *tooltest.Kit)
		code   string
	}{
		{"no elicitation", func(*tooltest.Kit) {}, tool.ErrHumanRequired.Code},
		{"declined", func(k *tooltest.Kit) { k.Elicit(false) }, tool.ErrDeclined.Code},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := writableKit(t)
			versionsFixture(t, k, "READY_FOR_DISTRIBUTION", "ACTIVE", 4)
			k.ASC.Handle("PATCH", "/v1/appStoreVersionPhasedReleases/{id}", 200, doc(t, jres{Type: "appStoreVersionPhasedReleases", ID: "pr1", Attrs: map[string]any{"phasedReleaseState": "COMPLETE"}}))
			c.elicit(k)
			tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "release_phased"), k.Env, `{"action":"complete","confirm":true}`), c.code)
			if n := len(k.ASC.Mutations()); n != 0 {
				t.Fatalf("%d mutations: %+v", n, mutationLog(k))
			}
		})
	}
	k := writableKit(t)
	versionsFixture(t, k, "READY_FOR_DISTRIBUTION", "ACTIVE", 4)
	k.ASC.Handle("PATCH", "/v1/appStoreVersionPhasedReleases/{id}", 200, doc(t, jres{Type: "appStoreVersionPhasedReleases", ID: "pr1", Attrs: map[string]any{"phasedReleaseState": "PAUSED"}}))
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_phased"), k.Env, `{"action":"pause"}`))
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_phased"), k.Env, `{"action":"pause","confirm":true}`))
	if n := len(k.ASC.Mutations()); n != 1 {
		t.Fatalf("pause without elicitation: %d mutations", n)
	}
}

func TestReleasePhasedAlready(t *testing.T) {
	k := writableKit(t)
	versionsFixture(t, k, "READY_FOR_DISTRIBUTION", "PAUSED", 3)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_phased"), k.Env, `{"action":"pause","confirm":true}`))
	if got := tooltest.Decode[map[string]any](t, res); got["already"] != true || len(k.ASC.Mutations()) != 0 {
		t.Errorf("out = %+v", got)
	}
}

func TestReleasePhasedRemoveOnlyInactive(t *testing.T) {
	k := writableKit(t)
	versionsFixture(t, k, "READY_FOR_DISTRIBUTION", "ACTIVE", 2)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "release_phased"), k.Env, `{"action":"remove","confirm":true}`), "invalid_state")
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("%d mutations", n)
	}

	k = writableKit(t)
	versionsFixture(t, k, "PENDING_DEVELOPER_RELEASE", "INACTIVE", 0)
	k.ASC.Handle("DELETE", "/v1/appStoreVersionPhasedReleases/{id}", 204, "")
	preview(t, k, "release_phased", `{"action":"remove"}`)
	// Dropping the phased schedule releases to everyone: a human must approve.
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "release_phased"), k.Env, `{"action":"remove","confirm":true}`), tool.ErrHumanRequired.Code)
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("remove without a human: %d mutations", n)
	}
	k.Elicit(true)
	out := tooltest.Decode[phasedOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_phased"), k.Env, `{"action":"remove","confirm":true}`)))
	muts := k.ASC.Mutations()
	if out.Status != "removed" || len(muts) != 1 || muts[0].Method != "DELETE" || muts[0].Path != "/v1/appStoreVersionPhasedReleases/pr1" {
		t.Errorf("out = %+v mutations = %+v", out, mutationLog(k))
	}
}

func TestReleasePhasedStartOnLiveIsManual(t *testing.T) {
	k := writableKit(t)
	versionsFixture(t, k, "READY_FOR_DISTRIBUTION", "", 0)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_phased"), k.Env, `{"action":"start"}`))
	out := tooltest.Decode[phasedOut](t, res)
	if out.Status != "manual" || len(k.ASC.Mutations()) != 0 || len(res.Next) != 1 || res.Next[0].Tool != "release_status" {
		t.Errorf("out = %+v next = %+v", out, res.Next)
	}
}

func TestBuildsList(t *testing.T) {
	k := writableKit(t)
	k.ASC.Handle("GET", "/v1/builds", 200, `{"data":[`+
		`{"type":"builds","id":"b3","attributes":{"version":"44","processingState":"PROCESSING","uploadedDate":"2026-01-02T00:00:00Z","expired":false,"usesNonExemptEncryption":null},"relationships":{"preReleaseVersion":{"data":{"type":"preReleaseVersions","id":"p1"}},"buildBetaDetail":{"data":{"type":"buildBetaDetails","id":"bd3"}},"betaGroups":{"data":[]}}},`+
		`{"type":"builds","id":"b2","attributes":{"version":"43","processingState":"VALID","uploadedDate":"2026-01-01T00:00:00Z","expirationDate":"2026-04-01T00:00:00Z","expired":false,"usesNonExemptEncryption":false},"relationships":{"preReleaseVersion":{"data":{"type":"preReleaseVersions","id":"p1"}},"buildBetaDetail":{"data":{"type":"buildBetaDetails","id":"bd2"}},"betaGroups":{"data":[{"type":"betaGroups","id":"g1"},{"type":"betaGroups","id":"g2"}]}}}],`+
		`"included":[{"type":"preReleaseVersions","id":"p1","attributes":{"version":"1.4","platform":"IOS"}},`+
		`{"type":"buildBetaDetails","id":"bd2","attributes":{"internalBuildState":"IN_BETA_TESTING","externalBuildState":"READY_FOR_BETA_SUBMISSION"}},`+
		`{"type":"buildBetaDetails","id":"bd3","attributes":{"internalBuildState":"PROCESSING","externalBuildState":"PROCESSING"}},`+
		`{"type":"betaGroups","id":"g1","attributes":{"name":"Team","isInternalGroup":true}},{"type":"betaGroups","id":"g2","attributes":{"name":"Public","isInternalGroup":false}}],`+
		`"meta":{"paging":{"total":2,"limit":20}}}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "builds_list"), k.Env, `{"app":"Hjul","version":"1.4"}`))
	out := tooltest.Decode[buildsListOut](t, res)
	if out.Total != 2 || out.Returned != 2 {
		t.Errorf("total/returned = %d/%d", out.Total, out.Returned)
	}
	b := out.Builds[1]
	if b.Number != "43" || b.Version != "1.4" || b.ProcessingState != "VALID" || b.InternalState != "IN_BETA_TESTING" ||
		b.UsesNonExemptEncryption == nil || *b.UsesNonExemptEncryption || b.GroupCount != 2 || !slices.Equal(b.Groups, []string{"Team", "Public"}) || b.Expires == "" {
		t.Errorf("build = %+v", b)
	}
	if out.Builds[0].UsesNonExemptEncryption != nil {
		t.Errorf("unanswered encryption = %v", *out.Builds[0].UsesNonExemptEncryption)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "builds_wait" || res.Next[0].Args["build"] != "b3" {
		t.Errorf("next = %+v", res.Next)
	}
	reqs := k.ASC.Requests()
	if len(reqs) > 2 {
		t.Errorf("%d requests, want ≤2", len(reqs))
	}
	q := reqs[0].Query
	if q.Get("filter[app]") != "100" || q.Get("filter[preReleaseVersion.version]") != "1.4" || q.Get("filter[expired]") != "false" ||
		q.Get("include") != "preReleaseVersion,buildBetaDetail,betaGroups" || q.Get("fields[builds]") == "" {
		t.Errorf("query = %s", q.Encode())
	}
}

// buildFixture queues processing states for build b2 (the last repeats).
func buildFixture(t *testing.T, k *tooltest.Kit, states ...string) {
	t.Helper()
	k.Resolve.Builds = map[tool.BuildRef]tool.Build{"latest": {ID: "b2", Number: "43", ProcessingState: "PROCESSING"}}
	for _, s := range states {
		k.ASC.Handle("GET", "/v1/builds/{id}", 200, doc(t, jres{Type: "builds", ID: "b2", Attrs: map[string]any{"version": "43", "processingState": s}}))
	}
}

type progressCall struct {
	done, total float64
	msg         string
}

func recordProgress(k *tooltest.Kit) *[]progressCall {
	calls := &[]progressCall{}
	k.Env.Progress = func(done, total float64, msg string) { *calls = append(*calls, progressCall{done, total, msg}) }
	return calls
}

func TestBuildsWaitValid(t *testing.T) {
	k := writableKit(t)
	clock := tooltest.NewFakeClock()
	k.Env.Clock = clock
	calls := recordProgress(k)
	buildFixture(t, k, "PROCESSING", "VALID")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "builds_wait"), k.Env, `{}`))
	out := tooltest.Decode[buildsWaitOut](t, res)
	if out.ProcessingState != "VALID" || out.Polls != 2 || out.BuildID != "b2" {
		t.Errorf("out = %+v", out)
	}
	if len(*calls) != 2 || (*calls)[1].done != 15 || (*calls)[1].total != 45 || !strings.Contains((*calls)[1].msg, "VALID") {
		t.Errorf("progress = %+v", *calls)
	}
	if clock.Slept != waitInterval {
		t.Errorf("slept %v", clock.Slept)
	}
	if len(res.Next) == 0 || res.Next[0].Tool != "testflight_distribute" {
		t.Errorf("next = %+v", res.Next)
	}
}

func TestBuildsWaitTimeout(t *testing.T) {
	k := writableKit(t)
	clock := tooltest.NewFakeClock()
	k.Env.Clock = clock
	calls := recordProgress(k)
	buildFixture(t, k, "PROCESSING")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "builds_wait"), k.Env, `{"build":"43","timeout_seconds":40}`))
	got := tooltest.Decode[map[string]any](t, res)
	if got["status"] != "still_processing" {
		t.Fatalf("out = %+v", got)
	}
	if clock.Slept != 40*time.Second || len(*calls) != 4 {
		t.Errorf("slept %v, %d progress calls", clock.Slept, len(*calls))
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "builds_wait" || res.Next[0].Args["build"] != "b2" {
		t.Errorf("next = %+v", res.Next)
	}
}

func TestBuildsWaitTimeoutCapped(t *testing.T) {
	k := writableKit(t)
	clock := tooltest.NewFakeClock()
	k.Env.Clock = clock
	recordProgress(k) // a progress token lifts the 50 s clamp
	buildFixture(t, k, "PROCESSING")
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "builds_wait"), k.Env, `{"timeout_seconds":5000}`))
	if clock.Slept != waitMax {
		t.Errorf("slept %v, want %v", clock.Slept, waitMax)
	}
}

// Clients that send no progress token cancel long requests at ~60 s, so the
// wait clamps to 50 s and returns still_processing with the resume step.
func TestBuildsWaitNoProgressTokenCapped(t *testing.T) {
	k := writableKit(t)
	clock := tooltest.NewFakeClock()
	k.Env.Clock = clock
	buildFixture(t, k, "PROCESSING")
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "builds_wait"), k.Env, `{"timeout_seconds":300}`))
	if got := tooltest.Decode[map[string]any](t, res); got["status"] != "still_processing" {
		t.Fatalf("out = %+v", got)
	}
	if clock.Slept != tool.NoProgressWaitCap {
		t.Errorf("slept %v, want %v", clock.Slept, tool.NoProgressWaitCap)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "builds_wait" {
		t.Errorf("next = %+v", res.Next)
	}
}

func TestPhasedPercent(t *testing.T) {
	for day, want := range map[int]int{0: 0, 1: 1, 3: 5, 7: 100, 9: 100} {
		if got := phasedPercent(day); got != want {
			t.Errorf("day %d = %d, want %d", day, got, want)
		}
	}
}
