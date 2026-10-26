package testflight

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/resolve"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// testApp is the app every test resolves.
var testApp = tool.App{ID: "app1", BundleID: "se.jvcr.hjul", Name: "Hjul", Platforms: []tool.Platform{tool.IOS}}

// groupResolver resolves apps from the fake data and beta groups with the real
// resolver, so group-name resolution runs against the FakeASC.
type groupResolver struct {
	*tooltest.FakeResolver
	real *resolve.Resolver
}

func (r groupResolver) Group(ctx context.Context, appID string, ref tool.GroupRef) (tool.Group, error) {
	return r.real.Group(ctx, appID, ref)
}

// newKit returns a kit whose FakeASC checks every request against the spec.
func newKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t)
	k.Resolve.Apps = []tool.App{testApp}
	k.Env.Resolve = groupResolver{FakeResolver: k.Resolve, real: resolve.New(k.ASC, resolve.Options{})}
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/betaGroups", http.StatusOK, `{"data":[
		{"type":"betaGroups","id":"g-int","attributes":{"name":"Team","isInternalGroup":true}},
		{"type":"betaGroups","id":"g-ext","attributes":{"name":"External Testers","isInternalGroup":false}}
	],"links":{}}`)
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

// testerDoc renders a betaTesters collection.
func testerDoc(ts ...[3]string) string {
	items := make([]string, 0, len(ts))
	for _, t := range ts { // id, email, groups (comma list)
		rel := ""
		if t[2] != "" {
			var ids []string
			for _, g := range strings.Split(t[2], ",") {
				ids = append(ids, `{"type":"betaGroups","id":"`+g+`"}`)
			}
			rel = `,"relationships":{"betaGroups":{"data":[` + strings.Join(ids, ",") + `]}}`
		}
		items = append(items, `{"type":"betaTesters","id":"`+t[0]+`","attributes":{"email":"`+t[1]+
			`","firstName":"Ann","lastName":"Lee","state":"INSTALLED","inviteType":"EMAIL"}`+rel+`}`)
	}
	return `{"data":[` + strings.Join(items, ",") + `],"links":{},"meta":{"paging":{"total":` +
		jsonInt(len(ts)) + `,"limit":200}}}`
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestGroupsList(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/betaGroups", http.StatusOK, `{"data":[
	 {"type":"betaGroups","id":"g-ext","attributes":{"name":"External Testers","isInternalGroup":false,
	   "publicLinkEnabled":true,"publicLink":"https://testflight.apple.com/join/abc","publicLinkLimitEnabled":true,
	   "publicLinkLimit":500,"feedbackEnabled":true,"createdDate":"2026-01-01T00:00:00Z"},
	   "relationships":{"betaTesters":{"meta":{"paging":{"total":42,"limit":1}},"data":[{"type":"betaTesters","id":"t1"}]},
	     "builds":{"meta":{"paging":{"total":3,"limit":1}},"data":[{"type":"builds","id":"b1"}]}}},
	 {"type":"betaGroups","id":"g-int","attributes":{"name":"Team","isInternalGroup":true,"hasAccessToAllBuilds":true,
	   "publicLinkEnabled":null,"feedbackEnabled":true},
	   "relationships":{"betaTesters":{"data":[]},"builds":{"data":[{"type":"builds","id":"b1"}]}}}
	],"links":{}}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_groups_list"), k.Env, `{"app":"Hjul","type":"all"}`))
	tooltest.AssertGolden(t, "testdata/groups_testers/golden/groups_list.json", map[string]any{
		"requests": k.ASC.Requests(), "data": res.Data,
	})
	got := tooltest.Decode[struct {
		Groups []groupOut `json:"groups"`
	}](t, res)
	if len(got.Groups) != 2 || *got.Groups[0].Testers != 42 || *got.Groups[0].Builds != 3 ||
		*got.Groups[0].PublicLinkLimit != 500 || *got.Groups[1].Testers != 0 || got.Groups[1].Builds != nil {
		t.Fatalf("groups = %s", tooltest.JSON(t, got.Groups))
	}
	if q := k.ASC.RequestsTo(http.MethodGet, "/v1/betaGroups")[0].Query; q.Get("filter[app]") != "app1" || q.Get("include") != "betaTesters,builds" {
		t.Fatalf("query = %v", q)
	}

	k2 := newKit(t)
	k2.ASC.Handle(http.MethodGet, "/v1/betaGroups", http.StatusOK, `{"data":[],"links":{}}`)
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_groups_list"), k2.Env, `{"type":"external"}`))
	if q := k2.ASC.Requests()[0].Query; q.Get("filter[isInternalGroup]") != "false" {
		t.Fatalf("query = %v", q)
	}
}

func TestTestersListByGroupName(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/betaTesters", http.StatusOK, testerDoc(
		[3]string{"t1", "ann@example.com", "g-ext,g-other"},
		[3]string{"t2", "bob@example.com", "g-ext"},
	))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_testers_list"), k.Env,
		`{"group":"external testers","state":"INSTALLED"}`))
	tooltest.AssertGolden(t, "testdata/groups_testers/golden/testers_list.json", map[string]any{
		"requests": k.ASC.Requests(), "data": res.Data,
	})
	q := k.ASC.RequestsTo(http.MethodGet, "/v1/betaTesters")[0].Query
	if q.Get("filter[betaGroups]") != "g-ext" || q.Has("filter[apps]") {
		t.Fatalf("query = %v", q)
	}
	body := tooltest.JSON(t, res.Data)
	for _, want := range []string{`"email":{"untrusted":"ann@example.com"}`, `"groups":["External Testers"]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "g-other") {
		t.Fatalf("groups of other apps leaked: %s", body)
	}

	// Unknown group name: not found with a groups_list next step.
	k2 := newKit(t)
	eb := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "testflight_testers_list"), k2.Env, `{"group":"Nope"}`), "not_found")
	if len(eb.Next) == 0 || eb.Next[0].Tool != "testflight_groups_list" {
		t.Fatalf("next = %+v", eb.Next)
	}
}

func TestTestersAddIdempotent(t *testing.T) {
	k := newKit(t)
	// Lookup: members of the group, then team-wide testers.
	k.ASC.HandleFunc(http.MethodGet, "/v1/betaTesters", func(r tool.Request) tooltest.Fixture {
		emails := strings.Split(r.Query.Get("filter[email]"), ",")
		if r.Query.Get("filter[betaGroups]") == "g-ext" {
			var ts [][3]string
			if slices.Contains(emails, "member@example.com") {
				ts = append(ts, [3]string{"t-mem", "member@example.com", ""})
			}
			if slices.Contains(emails, "racy@example.com") && len(k.ASC.RequestsTo(http.MethodPost, "/v1/betaTesters")) > 1 {
				ts = append(ts, [3]string{"t-racy", "racy@example.com", ""})
			}
			return tooltest.Fixture{Body: []byte(testerDoc(ts...))}
		}
		if slices.Contains(emails, "existing@example.com") {
			return tooltest.Fixture{Body: []byte(testerDoc([3]string{"t-old", "Existing@Example.com", ""}))}
		}
		return tooltest.Fixture{Body: []byte(testerDoc())}
	})
	k.ASC.HandleFunc(http.MethodPost, "/v1/betaTesters", func(r tool.Request) tooltest.Fixture {
		b, _ := json.Marshal(r.Body)
		if strings.Contains(string(b), "racy@example.com") {
			return tooltest.Fixture{Status: http.StatusConflict, Body: []byte(`{"errors":[{"status":"409","code":"ENTITY_ERROR.RELATIONSHIP.INVALID","title":"exists","detail":"tester already exists"}]}`)}
		}
		return tooltest.Fixture{Status: http.StatusCreated, Body: []byte(`{"data":{"type":"betaTesters","id":"t-new","attributes":{"email":"new@example.com"}}}`)}
	})
	k.ASC.Handle(http.MethodPost, "/v1/betaGroups/{id}/relationships/betaTesters", http.StatusNoContent, "")

	args := `{"group":"External Testers","testers":[
		{"email":"new@example.com","first_name":"Nia"},{"email":"existing@example.com"},{"email":"racy@example.com"},
		{"email":"member@example.com"},{"email":"MEMBER@example.com"},{"email":"not-an-email"}]`
	// External group: a preview first, nothing invited.
	prev := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_testers_add"), k.Env, args+`}`))
	if !strings.Contains(tooltest.JSON(t, prev.Data), `"needs_confirm"`) || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("external add without confirm = %s, mutations %d", tooltest.JSON(t, prev.Data), len(k.ASC.Mutations()))
	}
	asked := k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_testers_add"), k.Env, args+`,"confirm":true}`))
	if len(*asked) != 1 || res.Meta.ApprovedBy != tool.ApprovedHuman {
		t.Fatalf("asked = %v, approved_by = %q", *asked, res.Meta.ApprovedBy)
	}
	tooltest.AssertGolden(t, "testdata/groups_testers/golden/testers_add.json", map[string]any{
		"requests": k.ASC.Requests(), "data": res.Data,
	})
	got := tooltest.Decode[struct {
		Testers []addOutcome `json:"testers"`
		Added   int          `json:"added"`
		Already int          `json:"already"`
		Failed  int          `json:"failed"`
	}](t, res)
	want := map[string]string{
		"new@example.com": "added", "existing@example.com": "added", "racy@example.com": "already",
		"member@example.com": "already", "not-an-email": "error",
	}
	if len(got.Testers) != len(want) || got.Added != 2 || got.Already != 2 || got.Failed != 1 {
		t.Fatalf("result = %s", tooltest.JSON(t, res.Data))
	}
	for _, o := range got.Testers {
		if want[o.Email] != o.Outcome {
			t.Errorf("%s: outcome %s, want %s", o.Email, o.Outcome, want[o.Email])
		}
	}
	links := k.ASC.RequestsTo(http.MethodPost, "/v1/betaGroups/{id}/relationships/betaTesters")
	if len(links) != 1 || !strings.Contains(tooltest.JSON(t, links[0].Body), `"t-old"`) {
		t.Fatalf("links = %s", tooltest.JSON(t, links))
	}
	if n := len(k.ASC.RequestsTo(http.MethodPost, "/v1/betaTesters")); n != 2 {
		t.Fatalf("creates = %d, want 2 (new + racy)", n)
	}

	// Repeat with only members: already, no mutation.
	k2 := newKit(t)
	k2.ASC.Handle(http.MethodGet, "/v1/betaTesters", http.StatusOK, testerDoc([3]string{"t-mem", "member@example.com", ""}))
	res = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_testers_add"), k2.Env,
		`{"group":"g-ext","testers":[{"email":"member@example.com"}]}`))
	if d := tooltest.Decode[map[string]any](t, res); d["already"] != true || len(k2.ASC.Mutations()) != 0 {
		t.Fatalf("repeat = %s, mutations %d", tooltest.JSON(t, d), len(k2.ASC.Mutations()))
	}
}

func TestTestersAddGuards(t *testing.T) {
	k := newKit(t).ReadOnly()
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "testflight_testers_add"), k.Env,
		`{"group":"g-ext","testers":[{"email":"a@example.com"}]}`), "writes_disabled")

	k2 := newKit(t)
	many := make([]string, 101)
	for i := range many {
		many[i] = `{"email":"u` + jsonInt(i) + `@example.com"}`
	}
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "testflight_testers_add"), k2.Env,
		`{"group":"g-ext","testers":[`+strings.Join(many, ",")+`]}`), "invalid_arguments")
}

func TestTestersRemove(t *testing.T) {
	setup := func(k *tooltest.Kit) {
		k.ASC.HandleFunc(http.MethodGet, "/v1/betaTesters", func(r tool.Request) tooltest.Fixture {
			if r.Query.Has("filter[id]") {
				return tooltest.Fixture{Body: []byte(testerDoc([3]string{"t2", "bob@example.com", ""}))}
			}
			return tooltest.Fixture{Body: []byte(testerDoc([3]string{"t1", "ann@example.com", ""}))}
		})
	}
	args := `{"group":"External Testers","testers":["Ann@example.com","t2","ghost@example.com"]}`

	// Preview: lists the matched emails, mutates nothing.
	k := newKit(t)
	setup(k)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_testers_remove"), k.Env, args))
	prev := tooltest.Decode[struct {
		Status  string           `json:"status"`
		Preview tool.PlanPreview `json:"preview"`
	}](t, res)
	if prev.Status != "needs_confirm" || !slices.Equal(prev.Preview.Affected, []string{"Ann@example.com", "t2"}) {
		t.Fatalf("preview = %s", tooltest.JSON(t, res.Data))
	}
	if !strings.Contains(prev.Preview.Summary, "External Testers") || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("summary %q, mutations %d", prev.Preview.Summary, len(k.ASC.Mutations()))
	}
	if q := k.ASC.RequestsTo(http.MethodGet, "/v1/betaTesters")[0].Query; q.Get("filter[betaGroups]") != "g-ext" {
		t.Fatalf("lookup query = %v", q)
	}

	// Confirmed: one DELETE of the group linkage, approved by the human.
	k2 := newKit(t)
	setup(k2)
	asked := k2.Elicit(true)
	k2.ASC.Handle(http.MethodDelete, "/v1/betaGroups/{id}/relationships/betaTesters", http.StatusNoContent, "")
	args = strings.Replace(args, `{`, `{"confirm":true,`, 1)
	res = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_testers_remove"), k2.Env, args))
	tooltest.AssertGolden(t, "testdata/groups_testers/golden/testers_remove.json", map[string]any{
		"requests": k2.ASC.Requests(), "data": res.Data,
	})
	dels := k2.ASC.Mutations()
	if len(dels) != 1 || dels[0].Path != "/v1/betaGroups/g-ext/relationships/betaTesters" ||
		tooltest.JSON(t, dels[0].Body) != `{"data":[{"id":"t1","type":"betaTesters"},{"id":"t2","type":"betaTesters"}]}` {
		t.Fatalf("mutations = %s", tooltest.JSON(t, dels))
	}
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "Ann@example.com") {
		t.Fatalf("asked = %v", *asked)
	}

	// Declined: nothing removed.
	k3 := newKit(t)
	setup(k3)
	k3.Elicit(false)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "testflight_testers_remove"), k3.Env, args), "declined")
	if len(k3.ASC.Mutations()) != 0 {
		t.Fatal("declined call mutated")
	}

	// App scope uses the app linkage and the apps filter.
	k4 := newKit(t)
	setup(k4)
	// RequireHuman: the agent's confirm alone is not enough without elicitation.
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "testflight_testers_remove"), k4.Env,
		`{"scope":"app","testers":["ann@example.com"],"confirm":true}`), "human_approval_required")
	k4.Elicit(true)
	k4.ASC.Handle(http.MethodDelete, "/v1/apps/{id}/relationships/betaTesters", http.StatusNoContent, "")
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_testers_remove"), k4.Env,
		`{"scope":"app","testers":["ann@example.com"],"confirm":true}`))
	if m := k4.ASC.Mutations(); len(m) != 1 || m[0].Path != "/v1/apps/app1/relationships/betaTesters" {
		t.Fatalf("mutations = %s", tooltest.JSON(t, m))
	}
	if q := k4.ASC.Requests()[0].Query; q.Get("filter[apps]") != "app1" {
		t.Fatalf("lookup query = %v", q)
	}

	// Group scope needs a group.
	k5 := newKit(t)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "testflight_testers_remove"), k5.Env,
		`{"testers":["ann@example.com"]}`), "invalid_arguments")
}
