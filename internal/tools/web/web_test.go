package web

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func mustTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	return tl
}

const appID = "100"

func kit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t).WebSession()
	k.Resolve.Apps = []tool.App{{ID: appID, Name: "Hjul", BundleID: "se.jvcr.hjul", Platforms: []tool.Platform{tool.IOS}}}
	return k
}

// usagesBody renders appDataUsages the way iris returns them (relationship
// ids for category, purpose and dataProtection).
func usagesBody(us ...usage) string {
	var data []map[string]any
	for _, u := range us {
		rels := map[string]any{"dataProtection": tool.RelData("appDataUsageDataProtections", u.Protection)}
		if u.Category != "" {
			rels["category"] = tool.RelData("appDataUsageCategories", u.Category)
		}
		if u.Purpose != "" {
			rels["purpose"] = tool.RelData("appDataUsagePurposes", u.Purpose)
		}
		data = append(data, map[string]any{"type": "appDataUsages", "id": u.ID, "relationships": rels})
	}
	b, _ := json.Marshal(map[string]any{"data": data})
	if data == nil {
		return `{"data":[]}`
	}
	return string(b)
}

func publishBody(published bool) string {
	return fmt.Sprintf(`{"data":{"type":"appDataUsagesPublishState","id":"ps1","attributes":{"published":%t,"lastPublished":"2026-01-02T03:04:05Z"}}}`, published)
}

func u(id, cat, purpose, prot string) usage {
	return usage{ID: id, tuple: tuple{Category: cat, Purpose: purpose, Protection: prot}}
}

// privacyRoutes serves the catalog, the publish state and the usages (one
// body per read, the last repeating).
func privacyRoutes(k *tooltest.Kit, published bool, reads ...[]usage) {
	k.Web.HandleFile("GET", "/v1/appDataUsageCategories", "testdata/categories.json")
	k.Web.HandleFile("GET", "/v1/appDataUsagePurposes", "testdata/purposes.json")
	k.Web.HandleFile("GET", "/v1/appDataUsageDataProtections", "testdata/protections.json")
	k.Web.Handle("GET", "/v1/apps/{id}/dataUsagePublishState", 200, publishBody(published))
	for _, r := range reads {
		k.Web.Handle("GET", "/v1/apps/{id}/dataUsages", 200, usagesBody(r...))
	}
}

func ops(steps []step) []string {
	var out []string
	for _, s := range steps {
		x := s.Op + " " + s.t.key()
		if s.UsageID != "" {
			x += " #" + s.UsageID
		}
		out = append(out, x)
	}
	return out
}

func TestDiffStepsOrdering(t *testing.T) {
	email := u("u1", "EMAIL_ADDRESS", "APP_FUNCTIONALITY", protLinked)
	name := u("u3", "NAME", "APP_FUNCTIONALITY", protNotLinked)
	crash := u("u4", "CRASH_DATA", "ANALYTICS", protNotLinked)
	nc := u("u0", "", "", protNotCollected)
	tests := []struct {
		name    string
		remote  []usage
		desired declaration
		want    []string
	}{
		{
			"not collected to collected: prerequisite delete first",
			[]usage{nc},
			declaration{Data: []declEntry{{Category: "EMAIL_ADDRESS", Purposes: []string{"APP_FUNCTIONALITY"}, Linked: true}}},
			[]string{"delete ||DATA_NOT_COLLECTED #u0", "create EMAIL_ADDRESS|APP_FUNCTIONALITY|DATA_LINKED_TO_YOU"},
		},
		{
			"collected to not collected: all deletes first",
			[]usage{email, u("u2", "EMAIL_ADDRESS", "", protTracking)},
			declaration{NotCollected: true},
			[]string{"delete EMAIL_ADDRESS|APP_FUNCTIONALITY|DATA_LINKED_TO_YOU #u1", "delete EMAIL_ADDRESS||DATA_USED_TO_TRACK_YOU #u2", "create ||DATA_NOT_COLLECTED"},
		},
		{
			"flip linked: one PATCH",
			[]usage{email},
			declaration{Data: []declEntry{{Category: "EMAIL_ADDRESS", Purposes: []string{"APP_FUNCTIONALITY"}}}},
			[]string{"update EMAIL_ADDRESS|APP_FUNCTIONALITY|DATA_NOT_LINKED_TO_YOU #u1"},
		},
		{
			"add tracking: create only",
			[]usage{email},
			declaration{Data: []declEntry{{Category: "EMAIL_ADDRESS", Purposes: []string{"APP_FUNCTIONALITY"}, Linked: true, Tracking: true}}},
			[]string{"create EMAIL_ADDRESS||DATA_USED_TO_TRACK_YOU"},
		},
		{
			"remove category: trailing delete",
			[]usage{email, name},
			declaration{Data: []declEntry{{Category: "EMAIL_ADDRESS", Purposes: []string{"APP_FUNCTIONALITY"}, Linked: true}}},
			[]string{"delete NAME|APP_FUNCTIONALITY|DATA_NOT_LINKED_TO_YOU #u3"},
		},
		{
			"replace category: create before delete",
			[]usage{email, crash},
			declaration{Data: []declEntry{{Category: "EMAIL_ADDRESS", Purposes: []string{"APP_FUNCTIONALITY"}, Linked: true}, {Category: "name", Purposes: []string{"app_functionality"}}}},
			[]string{"create NAME|APP_FUNCTIONALITY|DATA_NOT_LINKED_TO_YOU", "delete CRASH_DATA|ANALYTICS|DATA_NOT_LINKED_TO_YOU #u4"},
		},
		{
			"duplicate remote row is deleted",
			[]usage{email, u("u9", "EMAIL_ADDRESS", "APP_FUNCTIONALITY", protLinked)},
			declaration{Data: []declEntry{{Category: "EMAIL_ADDRESS", Purposes: []string{"APP_FUNCTIONALITY"}, Linked: true}}},
			[]string{"delete EMAIL_ADDRESS|APP_FUNCTIONALITY|DATA_LINKED_TO_YOU #u9"},
		},
	}
	for _, tc := range tests {
		desired, err := desiredTuples(tc.desired)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := ops(diffSteps(tc.remote, desired)); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestDesiredTuplesValidation(t *testing.T) {
	for _, d := range []declaration{
		{},
		{NotCollected: true, Data: []declEntry{{Category: "NAME", Purposes: []string{"ANALYTICS"}}}},
		{Data: []declEntry{{Category: "NAME"}}},
		{Data: []declEntry{{Purposes: []string{"ANALYTICS"}}}},
		{Data: []declEntry{{Category: "NAME", Purposes: []string{"ANALYTICS"}, Linked: true}, {Category: "NAME", Purposes: []string{"ANALYTICS"}}}},
	} {
		if _, err := desiredTuples(d); err == nil {
			t.Errorf("%+v: accepted", d)
		}
	}
}

func TestPrivacyGetGroups(t *testing.T) {
	k := kit(t).ReadOnly()
	privacyRoutes(k, false, []usage{
		u("u1", "EMAIL_ADDRESS", "APP_FUNCTIONALITY", protLinked),
		u("u2", "EMAIL_ADDRESS", "ANALYTICS", protLinked),
		u("u3", "EMAIL_ADDRESS", "", protTracking),
		u("u4", "NAME", "APP_FUNCTIONALITY", protNotLinked),
	})
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "privacy_get"), k.Env, `{"app":"Hjul","catalog":true}`))
	out := tooltest.Decode[privacyGetOut](t, res)
	want := []declEntry{
		{Category: "EMAIL_ADDRESS", Purposes: []string{"ANALYTICS", "APP_FUNCTIONALITY"}, Linked: true, Tracking: true},
		{Category: "NAME", Purposes: []string{"APP_FUNCTIONALITY"}},
	}
	b1, _ := json.Marshal(out.Declaration.Data)
	b2, _ := json.Marshal(want)
	if string(b1) != string(b2) || out.State != "unpublished" || out.Published == nil || *out.Published {
		t.Fatalf("out = %s state %s", b1, out.State)
	}
	if out.Summary[0] != "EMAIL_ADDRESS: ANALYTICS, APP_FUNCTIONALITY; linked to the user; used for tracking" {
		t.Errorf("summary = %q", out.Summary)
	}
	if out.Catalog == nil || len(out.Catalog.Categories) != 3 || out.Catalog.Categories[0].Grouping != "CONTACT_INFO" || len(out.Catalog.Protections) != 4 {
		t.Errorf("catalog = %+v", out.Catalog)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "privacy_plan" {
		t.Errorf("next = %+v", res.Next)
	}

	// Data not collected, published.
	k = kit(t).ReadOnly()
	privacyRoutes(k, true, []usage{u("u0", "", "", protNotCollected)})
	out = tooltest.Decode[privacyGetOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "privacy_get"), k.Env, `{"app":"Hjul"}`)))
	if !out.Declaration.NotCollected || out.State != "published" || out.Summary[0] != "Data Not Collected" || out.Catalog != nil {
		t.Errorf("not collected = %+v", out)
	}
}

func TestPrivacyWebSessionRequired(t *testing.T) {
	k := kit(t)
	k.Env.Web = nil // the stand-in of a session without web login
	for _, c := range []struct{ tool, args string }{
		{"privacy_get", `{"app":"Hjul"}`},
		{"privacy_plan", `{"app":"Hjul","not_collected":true}`},
	} {
		b := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, c.tool), k.Env, c.args), tool.ErrWebSessionRequired.Code)
		if !strings.Contains(b.Hint, "web login") {
			t.Errorf("%s hint = %q", c.tool, b.Hint)
		}
	}
}

func TestPrivacyPlanInvalidTokens(t *testing.T) {
	k := kit(t).ReadOnly()
	privacyRoutes(k, true, []usage{})
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "privacy_plan"), k.Env,
		`{"app":"Hjul","data":[{"category":"OLD_CATEGORY","purposes":["APP_FUNCTIONALITY"]},{"category":"NAME","purposes":["SELLING"]}]}`))
	out := tooltest.Decode[privacyPlanOut](t, res)
	if out.Status != statusInvalid || out.PlanID != "" || len(k.Plans.Proposed) != 0 ||
		!slices.Equal(out.Problems, []string{"purpose SELLING is not in Apple's catalog", "category OLD_CATEGORY was retired by Apple"}) {
		t.Fatalf("out = %+v", out)
	}
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "privacy_plan"), k.Env, `{"app":"Hjul"}`), tool.ErrInvalidArgs.Code)
}

func TestPrivacyPlanNoChanges(t *testing.T) {
	k := kit(t).ReadOnly()
	privacyRoutes(k, true, []usage{u("u0", "", "", protNotCollected)})
	out := tooltest.Decode[privacyPlanOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "privacy_plan"), k.Env, `{"app":"Hjul","not_collected":true}`)))
	if out.Status != statusNoChanges || len(out.Steps) != 0 || len(k.Plans.Proposed) != 0 {
		t.Fatalf("out = %+v", out)
	}
}

const collectArgs = `"app":"Hjul","data":[{"category":"EMAIL_ADDRESS","purposes":["APP_FUNCTIONALITY"],"linked":true}]`

func planID(t *testing.T, k *tooltest.Kit, args string) privacyPlanOut {
	t.Helper()
	out := tooltest.Decode[privacyPlanOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "privacy_plan"), k.Env, "{"+args+"}")))
	if out.Status != statusPlanned || out.PlanID == "" {
		t.Fatalf("plan = %+v", out)
	}
	return out
}

func TestPrivacyApplyPublishes(t *testing.T) {
	k := kit(t)
	before := []usage{u("u0", "", "", protNotCollected)}
	after := []usage{u("n1", "EMAIL_ADDRESS", "APP_FUNCTIONALITY", protLinked)}
	// Reads: the plan and both apply rounds (asking, then with the human's
	// reply) see the old state; then the final read-back.
	privacyRoutes(k, true, before, before, before, after)
	// Deleting DATA_NOT_COLLECTED unpublishes the declaration before the
	// publish step; the final read-back is published.
	k.Web.Handle("GET", "/v1/apps/{id}/dataUsagePublishState", 200, publishBody(true))
	k.Web.Handle("GET", "/v1/apps/{id}/dataUsagePublishState", 200, publishBody(true))
	k.Web.Handle("GET", "/v1/apps/{id}/dataUsagePublishState", 200, publishBody(false))
	k.Web.Handle("GET", "/v1/apps/{id}/dataUsagePublishState", 200, publishBody(true))
	k.Web.Handle("DELETE", "/v1/appDataUsages/{id}", 204, "")
	k.Web.Handle("POST", "/v1/appDataUsages", 201, `{"data":{"type":"appDataUsages","id":"n1"}}`)
	k.Web.Handle("PATCH", "/v1/appDataUsagesPublishState/{id}", 200, publishBody(true))

	plan := planID(t, k, collectArgs)
	if !slices.Equal(plan.Removals, []string{"data not collected"}) || plan.Steps[len(plan.Steps)-1].Op != opPublish {
		t.Fatalf("plan = %+v", plan)
	}
	pv := k.Plans.Proposed[0]
	if !strings.Contains(strings.Join(pv.Warnings, "\n"), "removes data not collected") || len(pv.Irreversible) == 0 {
		t.Errorf("preview = %+v", pv)
	}
	if n := len(k.Web.Mutations()); n != 0 {
		t.Fatalf("plan mutated: %d", n)
	}
	asked := k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "privacy_apply"), k.Env, `{`+collectArgs+`,"plan_id":"`+plan.PlanID+`"}`))
	out := tooltest.Decode[privacyApplyOut](t, res)
	if len(*asked) != 1 {
		t.Errorf("human asked %d times", len(*asked))
	}
	var got []string
	for _, m := range k.Web.Mutations() {
		got = append(got, m.Method+" "+m.Path)
	}
	want := []string{"DELETE /v1/appDataUsages/u0", "POST /v1/appDataUsages", "PATCH /v1/appDataUsagesPublishState/ps1"}
	if !slices.Equal(got, want) {
		t.Fatalf("mutations = %q", got)
	}
	body, _ := json.Marshal(k.Web.Mutations()[1].Body)
	for _, s := range []string{`"app":{"data":{"id":"100","type":"apps"}}`, `"id":"DATA_LINKED_TO_YOU"`, `"id":"EMAIL_ADDRESS"`, `"id":"APP_FUNCTIONALITY"`} {
		if !strings.Contains(string(body), s) {
			t.Errorf("create body %s lacks %s", body, s)
		}
	}
	pb, _ := json.Marshal(k.Web.Mutations()[2].Body)
	if string(pb) != `{"data":{"attributes":{"published":true},"id":"ps1","type":"appDataUsagesPublishState"}}` {
		t.Errorf("publish body = %s", pb)
	}
	if out.Status != "applied" || out.Published == nil || !*out.Published || out.Steps[1].UsageID != "n1" ||
		out.Steps[0].Outcome != outcomeDone || out.Steps[2].Outcome != outcomeDone {
		t.Errorf("out = %+v", out)
	}
}

func TestPrivacyApplyPartialFailure(t *testing.T) {
	k := kit(t)
	remote := []usage{u("u1", "EMAIL_ADDRESS", "APP_FUNCTIONALITY", protLinked), u("u4", "CRASH_DATA", "ANALYTICS", protNotLinked)}
	privacyRoutes(k, true, remote)
	k.Web.HandleFixture("POST", "/v1/appDataUsages", tooltest.Fixture{Err: fmt.Errorf("POST: %w", tool.ErrOutcomeUnknown)})
	args := `"app":"Hjul","data":[{"category":"EMAIL_ADDRESS","purposes":["APP_FUNCTIONALITY"],"linked":true},{"category":"NAME","purposes":["APP_FUNCTIONALITY"]}]`
	plan := planID(t, k, args)
	k.Elicit(true)
	res := tooltest.Call(t, mustTool(t, "privacy_apply"), k.Env, `{`+args+`,"plan_id":"`+plan.PlanID+`"}`)
	if !res.IsError {
		t.Fatal("partial failure reported as success")
	}
	data := tooltest.Decode[privacyPartial](t, res)
	var outcomes []string
	for _, s := range data.Partial.Steps {
		outcomes = append(outcomes, s.Op+":"+s.Outcome)
	}
	// The create's outcome is resolved by the re-read: NAME is absent, so
	// it was not applied; the delete and publish never ran.
	if want := []string{"create:not_attempted", "delete:not_attempted", "publish:not_attempted"}; !slices.Equal(outcomes, want) {
		t.Errorf("outcomes = %q", outcomes)
	}
	if data.Error != tool.ErrOutcomeUnknown.Code || data.Partial.Remaining == nil || *data.Partial.Remaining != 2 {
		t.Errorf("partial = %+v", data)
	}
	if n := len(k.Web.RequestsTo("DELETE", "/v1/appDataUsages/{id}")); n != 0 {
		t.Errorf("deleted after a failed create: %d", n)
	}
}

func TestPrivacyApplyStalePlan(t *testing.T) {
	k := kit(t)
	privacyRoutes(k, true, []usage{u("u0", "", "", protNotCollected)}, []usage{u("u7", "NAME", "ANALYTICS", protNotLinked)})
	plan := planID(t, k, collectArgs)
	k.Elicit(true)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "privacy_apply"), k.Env, `{`+collectArgs+`,"plan_id":"`+plan.PlanID+`"}`), tool.ErrStalePlan.Code)
	if n := len(k.Web.Mutations()); n != 0 {
		t.Errorf("mutations = %d", n)
	}
}

// --- apps_create ---

const createArgs = `{"name":"Hamnen","bundle_id":"se.jvcr.hamnen","platforms":["IOS","MAC_OS"],"register_bundle_id":true`

func createRoutes(k *tooltest.Kit, registered bool) {
	k.ASC.Handle("GET", "/v1/apps", 200, `{"data":[]}`)
	bundles := `{"data":[{"type":"bundleIds","id":"B9","attributes":{"identifier":"se.jvcr.hamnen.widget","platform":"IOS"}}]}`
	if registered {
		bundles = `{"data":[{"type":"bundleIds","id":"B1","attributes":{"identifier":"se.jvcr.hamnen","platform":"UNIVERSAL"}}]}`
	}
	k.ASC.Handle("GET", "/v1/bundleIds", 200, bundles)
}

func TestAppsCreatePreviewAndConfirm(t *testing.T) {
	k := kit(t)
	createRoutes(k, false)
	k.ASC.Handle("POST", "/v1/bundleIds", 201, `{"data":{"type":"bundleIds","id":"B1","attributes":{"identifier":"se.jvcr.hamnen","platform":"UNIVERSAL"}}}`)
	k.Web.HandleFile("POST", "/v1/apps", "testdata/app_create_201.json")

	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "apps_create"), k.Env, createArgs+`}`))
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), `"needs_confirm"`) || !strings.Contains(string(b), "register bundle id se.jvcr.hamnen (platform UNIVERSAL") ||
		!strings.Contains(string(b), "platforms IOS, MAC_OS") {
		t.Fatalf("preview = %s", b)
	}
	if len(k.ASC.Mutations())+len(k.Web.Mutations()) != 0 {
		t.Fatal("preview mutated")
	}

	k.Elicit(true)
	out := tooltest.Decode[appsCreateOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "apps_create"), k.Env, createArgs+`,"confirm":true}`)))
	if !out.Created || out.App.ID != "6400000001" || out.App.SKU != "se.jvcr.hamnen" || len(out.Steps) != 2 {
		t.Fatalf("out = %+v", out)
	}
	reg, _ := json.Marshal(k.ASC.Mutations()[0].Body)
	if !strings.Contains(string(reg), `"platform":"UNIVERSAL"`) || !strings.Contains(string(reg), `"name":"Hamnen"`) {
		t.Errorf("bundle body = %s", reg)
	}
	body, _ := json.Marshal(k.Web.Mutations()[0].Body)
	for _, s := range []string{
		`"bundleId":"se.jvcr.hamnen"`, `"primaryLocale":"en-US"`, `"platform":"MAC_OS"`, `"platform":"IOS"`,
		`"versionString":"1.0"`, `"name":"Hamnen"`, `"type":"appInfoLocalizations"`,
	} {
		if !strings.Contains(string(body), s) {
			t.Errorf("create body lacks %s: %s", s, body)
		}
	}
	if strings.Contains(string(body), "companyName") {
		t.Error("companyName sent without company_name")
	}
}

func TestAppsCreateAlready(t *testing.T) {
	k := kit(t)
	k.ASC.Handle("GET", "/v1/apps", 200, `{"data":[{"type":"apps","id":"777","attributes":{"name":"Hamnen","bundleId":"se.jvcr.hamnen","sku":"HAMN"}}]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "apps_create"), k.Env, createArgs+`,"confirm":true}`))
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), `"already":true`) || !strings.Contains(string(b), `"id":"777"`) {
		t.Fatalf("out = %s", b)
	}
	if len(k.ASC.Mutations())+len(k.Web.Mutations()) != 0 {
		t.Error("already mutated")
	}

	// Same bundle id, another name: refused, never created.
	k = kit(t)
	k.ASC.Handle("GET", "/v1/apps", 200, `{"data":[{"type":"apps","id":"777","attributes":{"name":"Other","bundleId":"se.jvcr.hamnen"}}]}`)
	b2 := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "apps_create"), k.Env, createArgs+`}`), tool.ErrInvalidArgs.Code)
	if !strings.Contains(b2.Message, "already belongs to app 777") {
		t.Errorf("message = %q", b2.Message)
	}
}

func TestAppsCreateConflict(t *testing.T) {
	k := kit(t)
	createRoutes(k, true)
	k.Web.HandleFixture("POST", "/v1/apps", tooltest.Fixture{Status: 409, Body: mustRead(t, "testdata/app_create_409.json")})
	k.Elicit(true)
	res := tooltest.Call(t, mustTool(t, "apps_create"), k.Env, createArgs+`,"confirm":true}`)
	if !res.IsError {
		t.Fatal("409 reported as success")
	}
	data := tooltest.Decode[createPartial](t, res)
	for _, s := range []string{`the name "Hamnen" is already taken`, "SKU se.jvcr.hamnen was already used", "already uses this name, SKU or bundle id"} {
		if !strings.Contains(data.Message, s) {
			t.Errorf("message lacks %q: %s", s, data.Message)
		}
	}
	if len(data.Steps) != 1 || data.Steps[0].Outcome != outcomeError || len(k.ASC.Mutations()) != 0 {
		t.Errorf("steps = %+v (bundle already registered: no POST)", data.Steps)
	}
}

func TestAppsCreateNeedsRegisteredBundle(t *testing.T) {
	k := kit(t)
	createRoutes(k, false)
	b := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "apps_create"), k.Env, `{"name":"Hamnen","bundle_id":"se.jvcr.hamnen"}`), tool.ErrInvalidArgs.Code)
	if !strings.Contains(b.Message, "register_bundle_id") || len(b.Next) != 1 || b.Next[0].Tool != "signing_bundle_id_create" {
		t.Errorf("body = %+v", b)
	}
}

func TestAppsCreateBundleRegisteredThenAppUnknown(t *testing.T) {
	k := kit(t)
	createRoutes(k, false)
	k.ASC.Handle("POST", "/v1/bundleIds", 201, `{"data":{"type":"bundleIds","id":"B1","attributes":{"identifier":"se.jvcr.hamnen"}}}`)
	k.Web.HandleFixture("POST", "/v1/apps", tooltest.Fixture{Err: fmt.Errorf("POST: %w", tool.ErrOutcomeUnknown)})
	k.Elicit(true)
	res := tooltest.Call(t, mustTool(t, "apps_create"), k.Env, createArgs+`,"confirm":true}`)
	data := tooltest.Decode[createPartial](t, res)
	if !res.IsError || data.Error != tool.ErrOutcomeUnknown.Code ||
		!slices.Equal(data.Steps, []createStep{{stepRegisterBundle, outcomeDone}, {stepCreateApp, outcomeUnknown}}) {
		t.Errorf("partial = %+v", data)
	}
}

func TestAppsCreateValidation(t *testing.T) {
	k := kit(t)
	for _, args := range []string{
		`{"name":"","bundle_id":"se.jvcr.x"}`,
		`{"name":"An app name that is far too long for the store","bundle_id":"se.jvcr.x"}`,
		`{"name":"X","bundle_id":"se.jvcr.*"}`,
		`{"name":"X","bundle_id":"se.jvcr.x","sku":"bad sku"}`,
		`{"name":"X","bundle_id":"se.jvcr.x","platforms":["ANDROID"]}`,
	} {
		res := tooltest.Call(t, mustTool(t, "apps_create"), k.Env, args)
		if !res.IsError {
			t.Errorf("%s accepted", args)
		}
	}
}

func TestWebToolsetMeta(t *testing.T) {
	ro := tool.ProfileInfo{Name: "ro", KeyKind: tool.KeyTeam}
	for name, class := range map[string]tool.Class{"apps_create": tool.Destructive, "privacy_get": tool.Read, "privacy_plan": tool.Read, "privacy_apply": tool.Destructive} {
		m := mustTool(t, name).Meta()
		if m.Toolset != tool.Web || m.Class != class || len(m.Description) > tool.MaxDescription {
			t.Errorf("%s meta = %+v", name, m)
		}
		if listed := mustTool(t, name).ListedIn(ro); listed != (class == tool.Read) {
			t.Errorf("%s listed read-only = %v", name, listed)
		}
		if class == tool.Destructive && !m.RequireHuman {
			t.Errorf("%s does not require a human", name)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
