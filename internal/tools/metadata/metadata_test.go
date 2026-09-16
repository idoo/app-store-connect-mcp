package metadata

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

var testApp = tool.App{ID: "app1", BundleID: "se.jvcr.hjul", Name: "Hjul", Platforms: []tool.Platform{tool.IOS}}

// kit returns a tooltest kit with an app with a live and an editable version
// (its FakeASC checks every request against the spec).
func kit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t)
	k.Resolve.Apps = []tool.App{testApp}
	k.Resolve.AppLocales = []string{"de-DE", "en-US", "sv"}
	k.Resolve.Versions = map[tool.VersionRef]tool.Version{
		tool.VersionEditable: {ID: "v2", String: "1.1", Platform: tool.IOS, State: "PREPARE_FOR_SUBMISSION"},
		tool.VersionLive:     {ID: "v1", String: "1.0", Platform: tool.IOS, State: "READY_FOR_DISTRIBUTION"},
	}
	return k
}

func mustTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tl
}

const appInfosDoc = `{"data":[
 {"type":"appInfos","id":"info1","attributes":{"state":"READY_FOR_DISTRIBUTION"}},
 {"type":"appInfos","id":"info2","attributes":{"state":"PREPARE_FOR_SUBMISSION"}}
],"links":{}}`

func versionLocs(enDescription string) string {
	return `{"data":[
 {"type":"appStoreVersionLocalizations","id":"vl-en","attributes":{"locale":"en-US","description":"` + enDescription + `","keywords":"bike,ride","supportUrl":"https://example.com/help","whatsNew":"Fixes"}},
 {"type":"appStoreVersionLocalizations","id":"vl-de","attributes":{"locale":"de-DE","description":"Fahrrad","keywords":"rad","supportUrl":"https://example.com/de"}}
],"links":{}}`
}

const infoLocs = `{"data":[
 {"type":"appInfoLocalizations","id":"il-en","attributes":{"locale":"en-US","name":"Hjul","subtitle":"Ride more"}},
 {"type":"appInfoLocalizations","id":"il-de","attributes":{"locale":"de-DE","name":"Hjul","subtitle":"Mehr fahren"}}
],"links":{}}`

// listing registers the remote listing routes.
func listing(k *tooltest.Kit, enDescriptions ...string) {
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appInfos", http.StatusOK, appInfosDoc)
	if len(enDescriptions) == 0 {
		enDescriptions = []string{"Ride"}
	}
	for _, d := range enDescriptions {
		k.ASC.Handle(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", http.StatusOK, versionLocs(d))
	}
	k.ASC.Handle(http.MethodGet, "/v1/appInfos/{id}/appInfoLocalizations", http.StatusOK, infoLocs)
}

func decode(t *testing.T, res *tool.Result) map[string]any {
	t.Helper()
	return tooltest.Decode[map[string]any](t, res)
}

func TestPullTwoLocalesExport(t *testing.T) {
	k := kit(t)
	listing(k)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_pull"), k.Env,
		`{"app":"Hjul","locales":["en","de"],"export_dir":"fastlane/metadata"}`))
	out := decode(t, res)
	locs := out["locales"].(map[string]any)
	if len(locs) != 2 || locs["en-US"] == nil || locs["de-DE"] == nil {
		t.Fatalf("locales = %v", locs)
	}
	en := locs["en-US"].(map[string]any)
	if en["description"] != "Ride" || en["subtitle"] != "Ride more" {
		t.Errorf("en-US = %v", en)
	}
	if out["app_info"].(map[string]any)["id"] != "info2" {
		t.Errorf("app_info = %v, want editable info2", out["app_info"])
	}
	if len(out["written"].([]any)) == 0 {
		t.Fatal("no files written")
	}
	b, err := os.ReadFile(filepath.Join(k.Files.Root, "fastlane/metadata/en-US/description.txt"))
	if err != nil || string(b) != "Ride\n" {
		t.Errorf("description.txt = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(k.Files.Root, "fastlane/metadata/de-DE/name.txt")); err != nil {
		t.Errorf("de-DE name.txt: %v", err)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Error("pull mutated")
	}
}

func TestPullRefusesNonEmptyDir(t *testing.T) {
	k := kit(t)
	listing(k)
	if err := k.Files.WriteFile("notes/readme.txt", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "metadata_pull"), k.Env, `{"export_dir":"notes"}`), "invalid_arguments")
}

func TestPlanInlineLimitError(t *testing.T) {
	k := kit(t)
	listing(k)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env,
		`{"listing":{"locales":{"en-US":{"subtitle":"`+strings.Repeat("x", 31)+`"}}}}`))
	out := decode(t, res)
	if out["status"] != statusInvalid || out["plan_id"] != nil {
		t.Fatalf("status = %v plan_id = %v", out["status"], out["plan_id"])
	}
	errs := out["errors"].([]any)
	if len(errs) != 1 || errs[0].(map[string]any)["code"] != "too_long" {
		t.Errorf("errors = %v", errs)
	}
	if len(k.Plans.Proposed) != 0 {
		t.Error("plan proposed despite errors")
	}
}

func TestPlanFromFastlane(t *testing.T) {
	k := kit(t)
	listing(k)
	for p, v := range map[string]string{
		"fl/en-US/description.txt": "Ride further\n",
		"fl/en-US/keywords.txt":    "bike,ride\n",
		"fl/fr-FR/description.txt": "Rouler",
		"fl/fr-FR/keywords.txt":    "vélo",
		"fl/fr-FR/name.txt":        "Hjul",
	} {
		if err := k.Files.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, `{"fastlane_dir":"fl"}`))
	out := decode(t, res)
	if out["status"] != statusPlanned || out["plan_id"] == "" || out["source"] != "fastlane_dir" {
		t.Fatalf("out = %v", out)
	}
	counts := out["counts"].(map[string]any)
	if counts["updates"] != 1.0 || counts["creates"] != 2.0 || counts["deletes"] != 0.0 {
		t.Errorf("counts = %v", counts)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "metadata_apply" || res.Next[0].Args["plan_id"] != out["plan_id"] {
		t.Errorf("next = %+v", res.Next)
	}
}

func TestPlanApplyRoundTripNeverDeletesByOmission(t *testing.T) {
	k := kit(t)
	listing(k)
	k.ASC.Handle(http.MethodPatch, "/v1/appStoreVersionLocalizations/{id}", http.StatusOK,
		`{"data":{"type":"appStoreVersionLocalizations","id":"vl-en","attributes":{"locale":"en-US"}}}`)
	k.ASC.Handle(http.MethodPatch, "/v1/appInfoLocalizations/{id}", http.StatusOK,
		`{"data":{"type":"appInfoLocalizations","id":"il-en","attributes":{"locale":"en-US"}}}`)
	args := `{"app":"Hjul","listing":{"en-US":{"keywords":"bike,ride,cycle","subtitle":"Ride further"}}}`
	plan := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, args)))
	id, _ := plan["plan_id"].(string)
	if id == "" {
		t.Fatalf("no plan: %v", plan)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("plan mutated")
	}
	confirmed := strings.TrimSuffix(args, "}") + `,"plan_id":"` + id + `"}`
	out := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_apply"), k.Env, confirmed)))
	if out["status"] != "applied" || out["applied"] != 2.0 {
		t.Fatalf("apply = %v", out)
	}
	muts := k.ASC.Mutations()
	if len(muts) != 2 {
		t.Fatalf("mutations = %d", len(muts))
	}
	for _, m := range muts {
		if m.Method != http.MethodPatch {
			t.Errorf("unexpected %s %s (omitted de-DE must stay)", m.Method, m.Path)
		}
	}
	body, _ := json.Marshal(k.ASC.RequestsTo(http.MethodPatch, "/v1/appStoreVersionLocalizations/{id}")[0].Body)
	if !strings.Contains(string(body), `"keywords":"bike,ride,cycle"`) || strings.Contains(string(body), "description") {
		t.Errorf("version PATCH body = %s", body)
	}
	// The plan is consumed.
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "metadata_apply"), k.Env, confirmed), "stale_plan")
}

func TestApplyStaleFingerprint(t *testing.T) {
	k := kit(t)
	listing(k, "Ride", "Ride (edited in ASC)")
	args := `{"listing":{"en-US":{"keywords":"cycle"}}}`
	plan := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, args)))
	confirmed := `{"listing":{"en-US":{"keywords":"cycle"}},"plan_id":"` + plan["plan_id"].(string) + `"}`
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "metadata_apply"), k.Env, confirmed), "stale_plan")
	if len(k.ASC.Mutations()) != 0 {
		t.Error("stale plan mutated")
	}
}

func TestApplyDifferentArgsRefused(t *testing.T) {
	k := kit(t)
	listing(k)
	plan := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, `{"listing":{"en-US":{"keywords":"cycle"}}}`)))
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "metadata_apply"), k.Env,
		`{"listing":{"en-US":{"keywords":"other"}},"plan_id":"`+plan["plan_id"].(string)+`"}`), "stale_plan")
	if len(k.ASC.Mutations()) != 0 {
		t.Error("mutated")
	}
}

func TestPlanRemoveIsExplicit(t *testing.T) {
	k := kit(t)
	listing(k)
	out := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, `{"remove":["de"]}`)))
	counts := out["counts"].(map[string]any)
	if out["status"] != statusPlanned || counts["deletes"] != 2.0 || counts["updates"] != 0.0 {
		t.Fatalf("out = %v", out)
	}
	if len(k.Plans.Proposed) != 1 || len(k.Plans.Proposed[0].Irreversible) != 2 {
		t.Errorf("preview = %+v", k.Plans.Proposed)
	}
}

func TestPlanNeedsOneSource(t *testing.T) {
	k := kit(t)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env,
		`{"listing":{"en-US":{"keywords":"a"}},"fastlane_dir":"fl"}`), "invalid_arguments")
}

func TestApplyReadOnlyRefused(t *testing.T) {
	k := kit(t).ReadOnly()
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "metadata_apply"), k.Env, `{"listing":{"en-US":{"keywords":"a"}}}`), "writes_disabled")
}

func TestAppinfoReviewCreateAndCopyright(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreVersions/{id}", http.StatusOK,
		`{"data":{"type":"appStoreVersions","id":"v2","attributes":{"versionString":"1.1","copyright":"2025 Old AB"},
		"relationships":{"appStoreReviewDetail":{"data":null}}}}`)
	k.ASC.Handle(http.MethodPost, "/v1/appStoreReviewDetails", http.StatusCreated,
		`{"data":{"type":"appStoreReviewDetails","id":"rd-new","attributes":{}}}`)
	k.ASC.Handle(http.MethodPatch, "/v1/appStoreVersions/{id}", http.StatusOK,
		`{"data":{"type":"appStoreVersions","id":"v2","attributes":{}}}`)
	args := `{"copyright":"2026 Example AB","review":{"contact_email":"dev@example.com","demo_account_required":true,"demo_account_name":"demo","demo_account_password":"hunter2"}}`
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, args))
	txt, _ := json.Marshal(res.Data)
	if !strings.Contains(string(txt), "needs_confirm") || strings.Contains(string(txt), "hunter2") {
		t.Fatalf("preview = %s", txt)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("preview mutated")
	}
	asked := k.Elicit(true)
	out := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, strings.TrimSuffix(args, "}")+`,"confirm":true}`)))
	if out["status"] != "applied" || len(*asked) != 1 || strings.Contains((*asked)[0], "hunter2") {
		t.Fatalf("out = %v asked = %v", out, *asked)
	}
	posts := k.ASC.RequestsTo(http.MethodPost, "/v1/appStoreReviewDetails")
	if len(posts) != 1 {
		t.Fatalf("posts = %d", len(posts))
	}
	body, _ := json.Marshal(posts[0].Body)
	for _, want := range []string{`"demoAccountPassword":"hunter2"`, `"demoAccountRequired":true`, `"id":"v2"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("POST body %s lacks %s", body, want)
		}
	}
	if len(k.ASC.RequestsTo(http.MethodPatch, "/v1/appStoreVersions/{id}")) != 1 {
		t.Error("copyright not patched")
	}
	if strings.Contains(mustJSON(t, out), "hunter2") {
		t.Error("password echoed")
	}
}

func TestAppinfoPatchDetailCategoriesAgeRights(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreVersions/{id}", http.StatusOK,
		`{"data":{"type":"appStoreVersions","id":"v2","attributes":{"versionString":"1.1","copyright":"2026 Example AB"},
		"relationships":{"appStoreReviewDetail":{"data":{"type":"appStoreReviewDetails","id":"rd1"}}}},
		"included":[{"type":"appStoreReviewDetails","id":"rd1","attributes":{"contactEmail":"old@example.com","notes":"Use demo"}}]}`)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appInfos", http.StatusOK, `{"data":[
		{"type":"appInfos","id":"info1","attributes":{"state":"READY_FOR_DISTRIBUTION"}},
		{"type":"appInfos","id":"info2","attributes":{"state":"PREPARE_FOR_SUBMISSION"},"relationships":{
			"ageRatingDeclaration":{"data":{"type":"ageRatingDeclarations","id":"age1"}},
			"primaryCategory":{"data":{"type":"appCategories","id":"UTILITIES"}},
			"secondaryCategory":{"data":null}}}],
		"included":[{"type":"ageRatingDeclarations","id":"age1","attributes":{"gambling":false,"violenceCartoonOrFantasy":"INFREQUENT_OR_MILD"}}],"links":{}}`)
	k.ASC.Handle(http.MethodGet, "/v1/appCategories", http.StatusOK, `{"data":[
		{"type":"appCategories","id":"HEALTH_AND_FITNESS","attributes":{"platforms":["IOS"]}},
		{"type":"appCategories","id":"UTILITIES","attributes":{"platforms":["IOS"]}},
		{"type":"appCategories","id":"PRODUCTIVITY","attributes":{"platforms":["IOS"]}}],"links":{}}`)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}", http.StatusOK,
		`{"data":{"type":"apps","id":"app1","attributes":{"contentRightsDeclaration":"USES_THIRD_PARTY_CONTENT"}}}`)
	for _, p := range []string{"/v1/appStoreReviewDetails/{id}", "/v1/appInfos/{id}", "/v1/ageRatingDeclarations/{id}", "/v1/apps/{id}"} {
		k.ASC.Handle(http.MethodPatch, p, http.StatusOK, `{"data":{"type":"x","id":"x","attributes":{}}}`)
	}
	args := `{"primary_category":"Health & Fitness","secondary_category":"productivity",` +
		`"age_rating":{"gambling":false,"violenceCartoonOrFantasy":"NONE"},` +
		`"review":{"contact_email":"old@example.com","notes":"Login with demo/demo"},` +
		`"content_rights":"DOES_NOT_USE_THIRD_PARTY_CONTENT","copyright":"2026 Example AB"}`
	prev := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, args)))
	intended := prev["preview"].(map[string]any)["intended"].([]any)
	// primary, secondary, violence, notes, content rights; unchanged gambling/email/copyright skipped.
	if prev["status"] != "needs_confirm" || len(intended) != 5 {
		t.Fatalf("preview = %v", prev)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("preview mutated")
	}
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, strings.TrimSuffix(args, "}")+`,"confirm":true}`))
	if n := len(k.ASC.RequestsTo(http.MethodPost, "/v1/appStoreReviewDetails")); n != 0 {
		t.Errorf("existing detail re-POSTed %d times", n)
	}
	checks := map[string][]string{
		"/v1/appStoreReviewDetails/{id}": {`"id":"rd1"`, `"notes":"Login with demo/demo"`},
		"/v1/appInfos/{id}":              {`"id":"info2"`, `"HEALTH_AND_FITNESS"`, `"PRODUCTIVITY"`},
		"/v1/ageRatingDeclarations/{id}": {`"violenceCartoonOrFantasy":"NONE"`},
		"/v1/apps/{id}":                  {`"contentRightsDeclaration":"DOES_NOT_USE_THIRD_PARTY_CONTENT"`},
	}
	for p, wants := range checks {
		reqs := k.ASC.RequestsTo(http.MethodPatch, p)
		if len(reqs) != 1 {
			t.Errorf("PATCH %s sent %d times", p, len(reqs))
			continue
		}
		body := mustJSON(t, reqs[0].Body)
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("PATCH %s body %s lacks %s", p, body, w)
			}
		}
		if p == "/v1/ageRatingDeclarations/{id}" && strings.Contains(body, "gambling") {
			t.Errorf("unchanged gambling sent: %s", body)
		}
	}
	if n := len(k.ASC.RequestsTo(http.MethodPatch, "/v1/appStoreVersions/{id}")); n != 0 {
		t.Errorf("unchanged copyright patched %d times", n)
	}
}

func TestAppinfoUnknownCategoryAndAgeAttr(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appInfos", http.StatusOK, `{"data":[
		{"type":"appInfos","id":"info2","attributes":{"state":"PREPARE_FOR_SUBMISSION"},"relationships":{
			"ageRatingDeclaration":{"data":{"type":"ageRatingDeclarations","id":"age1"}}}}],"links":{}}`)
	k.ASC.Handle(http.MethodGet, "/v1/appCategories", http.StatusOK, `{"data":[{"type":"appCategories","id":"UTILITIES"}],"links":{}}`)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, `{"primary_category":"Nope"}`), "invalid_arguments")
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, `{"age_rating":{"colour":"red"}}`), "invalid_arguments")
}

func TestPullReportsMissingLocales(t *testing.T) {
	k := kit(t)
	listing(k)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_pull"), k.Env, `{"app":"Hjul","locales":["sv","en"]}`))
	out := decode(t, res)
	if got := mustJSON(t, out["missing_locales"]); got != `["sv"]` {
		t.Errorf("missing_locales = %s", got)
	}
	if got := mustJSON(t, out["available_locales"]); got != `["de-DE","en-US"]` {
		t.Errorf("available_locales = %s", got)
	}
	if len(res.Meta.Warnings) == 0 || !strings.Contains(res.Meta.Warnings[0], "no listing for sv") {
		t.Errorf("warnings = %v", res.Meta.Warnings)
	}
	if out["source"] != "editable" || len(res.Next) != 1 || res.Next[0].Tool != "metadata_plan" {
		t.Errorf("source = %v next = %+v", out["source"], res.Next)
	}
}

func TestPullLiveFallbackNamesInFlightVersion(t *testing.T) {
	k := kit(t)
	delete(k.Resolve.Versions, tool.VersionEditable)
	listing(k)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appStoreVersions", http.StatusOK,
		`{"data":[{"type":"appStoreVersions","id":"v2","attributes":{"versionString":"1.1","appVersionState":"WAITING_FOR_REVIEW"}}]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_pull"), k.Env, `{"app":"Hjul"}`))
	out := decode(t, res)
	if out["source"] != "live" || out["reason"] != "version 1.1 is WAITING_FOR_REVIEW and not editable" {
		t.Errorf("source = %v reason = %v", out["source"], out["reason"])
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "release_review_status" {
		t.Errorf("next = %+v", res.Next)
	}
	if q := k.ASC.RequestsTo(http.MethodGet, "/v1/apps/{id}/appStoreVersions"); len(q) != 1 || !strings.Contains(q[0].Query.Get("filter[appVersionState]"), "WAITING_FOR_REVIEW") {
		t.Errorf("in-flight lookup = %+v", q)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
