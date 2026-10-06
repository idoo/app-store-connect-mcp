package release

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// loggedReq is the golden form of one mutation.
type loggedReq struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   any    `json:"body,omitempty"`
}

// mutationLog returns the mutations in order; the concurrent What's New
// PATCHes are sorted among themselves so the log is deterministic.
func mutationLog(k *tooltest.Kit) []loggedReq {
	var out []loggedReq
	var locIdx []int
	for _, r := range k.ASC.Mutations() {
		if strings.HasPrefix(r.Path, "/v1/appStoreVersionLocalizations/") {
			locIdx = append(locIdx, len(out))
		}
		out = append(out, loggedReq{Method: r.Method, Path: r.Path, Body: r.Body})
	}
	locs := make([]loggedReq, 0, len(locIdx))
	for _, i := range locIdx {
		locs = append(locs, out[i])
	}
	slices.SortFunc(locs, func(a, b loggedReq) int { return strings.Compare(a.Path, b.Path) })
	for j, i := range locIdx {
		out[i] = locs[j]
	}
	return out
}

func mustTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	return tl
}

func argsJSON(t *testing.T, a map[string]any) string {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writable enables writes (setup returns a read-only kit for release_status).
func writable(k *tooltest.Kit) *tooltest.Kit {
	k.Env.Profile.AllowWrites = true
	k.ASC.SetAllowWrites(true)
	return k
}

// The state release_status fingerprints its plans over must equal the one
// release_prepare/release_submit read back, or status plans never verify.
func TestReleaseSnapshotFingerprintMatchesStatus(t *testing.T) {
	k := setup(t, readyFixture())
	for _, ref := range []tool.VersionRef{"1.4", "", "9.9"} {
		snap, err := loadReleaseSnapshot(t.Context(), k.Env, "100", tool.IOS, ref)
		if err != nil {
			t.Fatal(err)
		}
		got, err := snap.State.fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		sref := ref
		if sref == "" {
			sref = tool.VersionEditable
		}
		ss, err := loadStatusSnapshot(t.Context(), k.Env, k.Resolve.Apps[0], tool.IOS, sref)
		if err != nil {
			t.Fatal(err)
		}
		want, err := stateFromVersion(string(sref), tool.IOS, ss.Version, "").fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("ref %q: snapshot fingerprint differs from release_status state", ref)
		}
	}
}

func TestSubmitKeyMatchesStatusKey(t *testing.T) {
	app := tool.App{ID: "100"}
	a, _ := tool.CanonicalArgs(submitKey{submitPlanKey: submitArgsKey(app, tool.IOS, "1.4")})
	b, _ := tool.CanonicalArgs(submitArgsKey(app, tool.IOS, "1.4"))
	if string(a) != string(b) {
		t.Errorf("submitKey %s != submitArgsKey %s", a, b)
	}
}

func TestReleasePrepareAppliesStatusPlan(t *testing.T) {
	k := setup(t, readyFixture())
	_, sres := call(t, k, `{"app":"Hjul","desired":{"build":"latest","whats_new":{"*":"New sails."},"release_type":"MANUAL","phased":true,"uses_non_exempt_encryption":false}}`)
	if len(sres.Next) == 0 || sres.Next[0].Tool != "release_prepare" {
		t.Fatalf("status next = %+v", sres.Next)
	}
	writable(k)
	k.ASC.Handle("PATCH", "/v1/appStoreVersions/{id}/relationships/build", 204, "")
	k.ASC.Handle("GET", "/v1/builds/{id}", 200, doc(t, jres{Type: "builds", ID: "b2", Attrs: map[string]any{"version": "43", "processingState": "VALID", "usesNonExemptEncryption": nil}}))
	k.ASC.Handle("PATCH", "/v1/builds/{id}", 200, doc(t, jres{Type: "builds", ID: "b2", Attrs: map[string]any{"usesNonExemptEncryption": false}}))
	k.ASC.Handle("PATCH", "/v1/appStoreVersions/{id}", 200, doc(t, jres{Type: "appStoreVersions", ID: "v1", Attrs: map[string]any{"releaseType": "MANUAL"}}))
	k.ASC.Handle("POST", "/v1/appStoreVersionPhasedReleases", 201, doc(t, jres{Type: "appStoreVersionPhasedReleases", ID: "pr1", Attrs: map[string]any{"phasedReleaseState": "INACTIVE"}}))
	k.ASC.HandleFunc("PATCH", "/v1/appStoreVersionLocalizations/{id}", func(r tool.Request) tooltest.Fixture {
		id := strings.Split(r.Path, "/")[3]
		return tooltest.Fixture{Status: 200, Body: []byte(doc(t, jres{Type: "appStoreVersionLocalizations", ID: id, Attrs: map[string]any{"whatsNew": "New sails."}}))}
	})
	before := len(k.ASC.Requests())

	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_prepare"), k.Env, argsJSON(t, sres.Next[0].Args)))
	out := tooltest.Decode[prepareOut](t, res)
	for _, s := range out.Steps {
		if s.Status != stepDone {
			t.Errorf("step %+v not done", s)
		}
	}
	if len(out.WhatsNew) != 2 {
		t.Errorf("whats_new results = %+v", out.WhatsNew)
	}
	// AgentUX-1: the next step is a fresh release_submit preview (no stale plan_id).
	if len(res.Next) != 2 || res.Next[0].Tool != "release_submit" || res.Next[0].Args["plan_id"] != nil ||
		res.Next[0].Args["version"] != "1.4" || res.Next[1].Tool != "release_status" {
		t.Errorf("next = %+v", res.Next)
	}
	if out.Note != "re-preview: prepare changed the version" {
		t.Errorf("note = %q", out.Note)
	}
	tooltest.AssertGolden(t, "testdata/prepare_submit/prepare_full.json", map[string]any{"requests": mutationLog(k), "result": res.Data})
	if got := len(k.ASC.Requests()) - before; got > 14 {
		t.Errorf("prepare made %d requests", got)
	}

	// The plan is single use.
	res = tooltest.Call(t, mustTool(t, "release_prepare"), k.Env, argsJSON(t, sres.Next[0].Args))
	tooltest.MustFail(t, res, tool.ErrStalePlan.Code)
}

func TestReleasePrepareWithoutPlanIsRefused(t *testing.T) {
	k := writable(setup(t, readyFixture()))
	res := tooltest.Call(t, mustTool(t, "release_prepare"), k.Env, `{"app":"Hjul","version":"1.4","build":"latest","release_type":"MANUAL"}`)
	body := tooltest.MustFail(t, res, tool.ErrNeedsConfirm.Code)
	if len(body.Next) != 1 || body.Next[0].Tool != "release_status" {
		t.Fatalf("next = %+v", body.Next)
	}
	desired, _ := body.Next[0].Args["desired"].(map[string]any)
	if desired["build"] != "latest" || desired["release_type"] != "MANUAL" || body.Next[0].Args["version"] != "1.4" {
		t.Errorf("next args = %+v", body.Next[0].Args)
	}
	if !strings.Contains(body.Message, "release_status") {
		t.Errorf("message not actionable: %q", body.Message)
	}
	if n := len(k.ASC.Requests()); n != 0 {
		t.Errorf("refusal made %d requests", n)
	}
}

func TestReleasePrepareBuildStillProcessing(t *testing.T) {
	k := setup(t, readyFixture())
	k.Resolve.Builds["latest:1.4"] = tool.Build{ID: "b2", Number: "43", ProcessingState: "PROCESSING"}
	_, sres := call(t, k, `{"desired":{"build":"latest","release_type":"MANUAL"}}`)
	writable(k)
	k.ASC.Handle("PATCH", "/v1/appStoreVersions/{id}", 200, doc(t, jres{Type: "appStoreVersions", ID: "v1", Attrs: map[string]any{"releaseType": "MANUAL"}}))

	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_prepare"), k.Env, argsJSON(t, sres.Next[0].Args)))
	data := tooltest.Decode[map[string]any](t, res)
	if data["status"] != "still_processing" {
		t.Fatalf("data = %+v", data)
	}
	if len(res.Next) == 0 || res.Next[0].Tool != "builds_wait" {
		t.Errorf("next = %+v", res.Next)
	}
	muts := k.ASC.Mutations()
	if len(muts) != 1 || muts[0].Path != "/v1/appStoreVersions/v1" {
		t.Errorf("mutations = %+v", mutationLog(k))
	}
}

// ReleaseManager#7: a hotfix (new version + What's New) takes one approval
// round; What's New lands on the locales Apple copied to the new version.
func TestReleasePrepareCreatesVersion(t *testing.T) {
	k := setup(t, readyFixture())
	_, sres := call(t, k, `{"version":"2.0","desired":{"release_type":"AFTER_APPROVAL","phased":true,"whats_new":{"*":"Hi"}}}`)
	writable(k)
	k.ASC.Handle("POST", "/v1/appStoreVersions", 201, doc(t, jres{Type: "appStoreVersions", ID: "v2", Attrs: map[string]any{"versionString": "2.0"}}))
	k.ASC.Handle("POST", "/v1/appStoreVersionPhasedReleases", 201, doc(t, jres{Type: "appStoreVersionPhasedReleases", ID: "pr1", Attrs: map[string]any{}}))
	k.ASC.HandleFunc("PATCH", "/v1/appStoreVersionLocalizations/{id}", func(r tool.Request) tooltest.Fixture {
		return tooltest.Fixture{Status: 200, Body: []byte(doc(t, jres{Type: "appStoreVersionLocalizations", ID: strings.Split(r.Path, "/")[3], Attrs: map[string]any{"whatsNew": "Hi"}}))}
	})
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_prepare"), k.Env, argsJSON(t, sres.Next[0].Args)))
	out := tooltest.Decode[prepareOut](t, res)
	if !out.Version.Created || out.Version.ID != "v2" {
		t.Errorf("version = %+v", out.Version)
	}
	if n := len(k.ASC.RequestsTo("PATCH", "/v1/appStoreVersionLocalizations/{id}")); n != 2 {
		t.Errorf("What's New PATCHes = %d, want 2 (one per copied locale)", n)
	}
	for _, s := range out.Steps {
		if s.Step == "whats_new" && s.Status != stepDone {
			t.Errorf("whats_new step = %+v", s)
		}
	}
	tooltest.AssertGolden(t, "testdata/prepare_submit/prepare_create.json", map[string]any{"requests": mutationLog(k), "result": res.Data})
}

// iapVersionRoute serves the ready Pro IAP's open version iv1.
func iapVersionRoute(t *testing.T, k *tooltest.Kit) {
	k.ASC.Handle("GET", "/v2/inAppPurchases/{id}/versions", 200, doc(t, []jres{{Type: "inAppPurchaseVersions", ID: "iv1", Attrs: map[string]any{"state": "READY_FOR_REVIEW"}}}))
}

// submissionFixture registers an open submission (state "" = none).
func submissionFixture(t *testing.T, k *tooltest.Kit, state string, items ...jres) {
	t.Helper()
	if state == "" {
		return
	}
	// The query-constrained route outranks setup's empty list.
	k.ASC.Handle("GET", "/v1/reviewSubmissions?filter[app]=100", 200, doc(t, []jres{{Type: "reviewSubmissions", ID: "rs1", Attrs: map[string]any{"platform": "IOS", "state": state}}}))
	if items == nil {
		items = []jres{}
	}
	k.ASC.Handle("GET", "/v1/reviewSubmissions/{id}/items", 200, doc(t, items))
}

func submitHandlers(t *testing.T, k *tooltest.Kit) {
	k.ASC.Handle("POST", "/v1/reviewSubmissions", 201, doc(t, jres{Type: "reviewSubmissions", ID: "rs9", Attrs: map[string]any{"platform": "IOS", "state": "READY_FOR_REVIEW"}}))
	k.ASC.HandleFunc("POST", "/v1/reviewSubmissionItems", func(tool.Request) tooltest.Fixture {
		return tooltest.Fixture{Status: 201, Body: []byte(doc(t, jres{Type: "reviewSubmissionItems", ID: "new-item", Attrs: map[string]any{"state": "READY_FOR_REVIEW"}}))}
	})
	k.ASC.HandleFunc("PATCH", "/v1/reviewSubmissions/{id}", func(r tool.Request) tooltest.Fixture {
		id := strings.Split(r.Path, "/")[3]
		return tooltest.Fixture{Status: 200, Body: []byte(doc(t, jres{Type: "reviewSubmissions", ID: id, Attrs: map[string]any{"state": "WAITING_FOR_REVIEW"}}))}
	})
	k.ASC.Handle("PATCH", "/v1/reviewSubmissionItems/{id}", 200, doc(t, jres{Type: "reviewSubmissionItems", ID: "it1", Attrs: map[string]any{"state": "READY_FOR_REVIEW"}}))
}

// submitTwice previews release_submit, then confirms its plan.
func submitTwice(t *testing.T, k *tooltest.Kit, args string) *tool.Result {
	t.Helper()
	tl := mustTool(t, "release_submit")
	prev := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args)))
	id, _ := prev["plan_id"].(string)
	if prev["status"] != "planned" || id == "" {
		t.Fatalf("preview = %+v", prev)
	}
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("preview sent %d mutations", n)
	}
	var a map[string]any
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		t.Fatal(err)
	}
	a["plan_id"] = id
	k.Elicit(true)
	return tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, argsJSON(t, a)))
}

func TestReleaseSubmitNewSubmission(t *testing.T) {
	k := writable(setup(t, readyFixture()))
	submitHandlers(t, k)
	iapVersionRoute(t, k)
	res := submitTwice(t, k, `{"app":"Hjul","version":"1.4"}`)
	out := tooltest.Decode[submitOut](t, res)
	if !out.Created || out.Submission.ID != "rs9" || out.Submission.State != "WAITING_FOR_REVIEW" {
		t.Errorf("out = %+v", out)
	}
	tooltest.AssertGolden(t, "testdata/prepare_submit/submit_new.json", map[string]any{"requests": mutationLog(k), "result": res.Data})
}

func TestReleaseSubmitReusesDraft(t *testing.T) {
	k := writable(setup(t, readyFixture()))
	submissionFixture(t, k, "READY_FOR_REVIEW")
	submitHandlers(t, k)
	iapVersionRoute(t, k)
	submitTwice(t, k, `{"app":"Hjul"}`)
	got := mutationLog(k)
	if len(got) != 3 || got[0].Path != "/v1/reviewSubmissionItems" || got[1].Path != "/v1/reviewSubmissionItems" || got[2].Path != "/v1/reviewSubmissions/rs1" {
		t.Errorf("mutations = %+v", got)
	}
	if len(k.ASC.RequestsTo("POST", "/v1/reviewSubmissions")) != 0 {
		t.Error("created a second submission")
	}
}

func TestReleaseSubmitAfterRejectionResolvesItems(t *testing.T) {
	k := writable(setup(t, readyFixture()))
	submissionFixture(t, k, "UNRESOLVED_ISSUES",
		jres{Type: "reviewSubmissionItems", ID: "it1", Attrs: map[string]any{"state": "REJECTED"}, Rels: map[string]any{"appStoreVersion": "appStoreVersions:v1"}})
	submitHandlers(t, k)
	iapVersionRoute(t, k)
	res := submitTwice(t, k, `{"app":"Hjul"}`)
	tooltest.AssertGolden(t, "testdata/prepare_submit/submit_resubmit.json", map[string]any{"requests": mutationLog(k), "result": res.Data})
}

func TestReleaseSubmitAlreadySubmitted(t *testing.T) {
	k := writable(setup(t, readyFixture()))
	submissionFixture(t, k, "WAITING_FOR_REVIEW",
		jres{Type: "reviewSubmissionItems", ID: "it1", Attrs: map[string]any{"state": "READY_FOR_REVIEW"}, Rels: map[string]any{"appStoreVersion": "appStoreVersions:v1"}})
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_submit"), k.Env, `{"app":"Hjul"}`))
	data := tooltest.Decode[map[string]any](t, res)
	if data["already"] != true {
		t.Errorf("data = %+v", data)
	}
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Errorf("%d mutations", n)
	}
	if len(k.Plans.Proposed) != 0 {
		t.Error("proposed a plan for a no-op")
	}
}

func TestReleaseSubmitAttachesIAPVersion(t *testing.T) {
	k := writable(setup(t, readyFixture()))
	submitHandlers(t, k)
	k.ASC.Handle("GET", "/v2/inAppPurchases/{id}/versions", 200, `{"data":[]}`)
	k.ASC.Handle("POST", "/v1/inAppPurchaseVersions", 201, doc(t, jres{Type: "inAppPurchaseVersions", ID: "iv9", Attrs: map[string]any{"state": "PREPARE_FOR_SUBMISSION"}}))
	res := submitTwice(t, k, `{"app":"Hjul","include_iaps":["Pro"]}`)
	tooltest.AssertGolden(t, "testdata/prepare_submit/submit_iap.json", map[string]any{"requests": mutationLog(k), "result": res.Data})
}

func TestReleaseCancelInReview(t *testing.T) {
	k := writable(setup(t, readyFixture()))
	submissionFixture(t, k, "IN_REVIEW",
		jres{Type: "reviewSubmissionItems", ID: "it1", Attrs: map[string]any{"state": "READY_FOR_REVIEW"}, Rels: map[string]any{"appStoreVersion": "appStoreVersions:v1"}})
	k.ASC.Handle("PATCH", "/v1/reviewSubmissions/{id}", 200, doc(t, jres{Type: "reviewSubmissions", ID: "rs1", Attrs: map[string]any{"state": "CANCELING"}}))
	tl := mustTool(t, "release_cancel")
	prev := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul"}`)))
	if prev["status"] != tool.ErrNeedsConfirm.Code || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("preview = %+v", prev)
	}
	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","confirm":true}`))
	out := tooltest.Decode[cancelOut](t, res)
	if out.Action != "cancel" || out.Submission.State != "CANCELING" {
		t.Errorf("out = %+v", out)
	}
	tooltest.AssertGolden(t, "testdata/prepare_submit/cancel_in_review.json", map[string]any{"requests": mutationLog(k), "result": res.Data})
}

func TestReleaseCancelDraftDeletesItems(t *testing.T) {
	k := writable(setup(t, readyFixture()))
	submissionFixture(t, k, "READY_FOR_REVIEW",
		jres{Type: "reviewSubmissionItems", ID: "it1", Attrs: map[string]any{"state": "READY_FOR_REVIEW"}, Rels: map[string]any{"appStoreVersion": "appStoreVersions:v1"}},
		jres{Type: "reviewSubmissionItems", ID: "it2", Attrs: map[string]any{"state": "READY_FOR_REVIEW"}, Rels: map[string]any{"inAppPurchaseVersion": "inAppPurchaseVersions:iv1"}})
	k.ASC.Handle("DELETE", "/v1/reviewSubmissionItems/{id}", 204, "")
	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_cancel"), k.Env, `{"app":"Hjul","confirm":true}`))
	out := tooltest.Decode[cancelOut](t, res)
	if out.Action != "remove_draft_items" || !slices.Equal(out.Removed, []string{"it1", "it2"}) {
		t.Errorf("out = %+v", out)
	}
	if len(k.ASC.RequestsTo("PATCH", "/v1/reviewSubmissions/{id}")) != 0 {
		t.Error("draft was PATCHed canceled")
	}
}

// ReleaseManager#6: release_submit refuses on a block check before it
// creates anything or proposes a plan.
func TestReleaseSubmitRefusesOnPreflightBlock(t *testing.T) {
	f := readyFixture()
	f.noPrice = true
	k := writable(setup(t, f))
	submitHandlers(t, k)
	iapVersionRoute(t, k)
	body := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "release_submit"), k.Env, `{"app":"Hjul"}`), tool.ErrInvalidArgs.Code)
	if !strings.Contains(body.Message, "pricing_availability") {
		t.Errorf("message = %q", body.Message)
	}
	if n := len(k.ASC.Mutations()); n != 0 || len(k.Plans.Proposed) != 0 {
		t.Errorf("mutations = %d, plans = %d", n, len(k.Plans.Proposed))
	}
}

// ReleaseManager#2: ready products are attached by default (listed in the
// preview); attach_first_time_products:false leaves them out.
func TestReleaseSubmitAutoAttachesReadyProducts(t *testing.T) {
	k := writable(setup(t, readyFixture()))
	submitHandlers(t, k)
	iapVersionRoute(t, k)
	out := tooltest.Decode[submitOut](t, submitTwice(t, k, `{"app":"Hjul"}`))
	if !slices.ContainsFunc(out.Items, func(w wantedItem) bool { return w.Kind == kindIAPVersion && w.Target == "iv1" && w.Action == "added" }) {
		t.Errorf("items = %+v", out.Items)
	}
	if b, _ := json.Marshal(k.Plans.Proposed); !strings.Contains(string(b), "add Pro") {
		t.Errorf("preview does not list the IAP: %s", b)
	}

	k = writable(setup(t, readyFixture()))
	submitHandlers(t, k)
	out = tooltest.Decode[submitOut](t, submitTwice(t, k, `{"app":"Hjul","attach_first_time_products":false}`))
	if len(out.Items) != 1 || !slices.ContainsFunc(out.Notes, func(n string) bool { return strings.Contains(n, "not attached") }) {
		t.Errorf("opt-out items = %+v notes = %v", out.Items, out.Notes)
	}
}
