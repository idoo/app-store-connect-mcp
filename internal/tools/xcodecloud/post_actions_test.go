package xcodecloud

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const (
	paTeam    = "0a1b2c3d-0000-4000-8000-000000000001"
	paProduct = "0a1b2c3d-0000-4000-8000-000000000002"
	paWF      = "0a1b2c3d-0000-4000-8000-000000000003"
	paIOS     = "aaaaaaaa-0000-4000-8000-000000000001"
	paMac     = "aaaaaaaa-0000-4000-8000-000000000002"
	paDev     = "cccccccc-0000-4000-8000-000000000001"
	paQA      = "cccccccc-0000-4000-8000-000000000002"
	paExt     = "cccccccc-0000-4000-8000-000000000003"
	paPostIOS = "bbbbbbbb-0000-4000-8000-000000000001"
	paPostMac = "bbbbbbbb-0000-4000-8000-000000000002"
	paPath    = "/ci/api/teams/" + paTeam + "/products/" + paProduct + "/workflows-v15/" + paWF
	paAt      = "2026-11-03T06:41:56.139Z"
	paNotify  = `{"id":"bbbbbbbb-0000-4000-8000-000000000009","name":"Notify","type":"notification",` +
		`"notification_config":{"archive_action_id":"` + paIOS + `","emails":["dev@example.invalid"]}}`
	paIOSDest = `,"destination_config":{"destination_type":"any_iOS_device"}`
)

// paContent is a workflow's content with an iOS archive (distribution
// iosDist), a macOS archive, a test action and the post-actions; it holds
// a key the tool does not know and a secret environment variable.
func paContent(iosDist, posts string) string {
	return `{"name":"Release (Manual)","description":"","locked":false,"future_field":{"x":1},
	 "environment_variables":[{"name":"API_TOKEN","value":{"plaintext":null,"redacted_value":"********"},"is_secret":true}],
	 "actions":[
	  {"id":"` + paIOS + `","default_name":"Archive - iOS","action_type":"archive","scheme":"Demo","platform":{"name":"iOS"},` +
		`"is_required":true,"archive_config":{"distribution_type":"` + iosDist + `"}` + paIOSDest + `},
	  {"id":"` + paMac + `","default_name":"Archive - macOS","action_type":"archive","scheme":"Demo","platform":{"name":"macOS"},` +
		`"is_required":true,"archive_config":{"distribution_type":"testFlightInternalOnly"}},
	  {"id":"dddddddd-0000-4000-8000-000000000001","default_name":"Test - iOS","action_type":"test","platform":{"name":"iOS"}}],
	 "post_actions":` + posts + `}`
}

// paInternal is a TestFlight Internal Testing post-action with one tester
// and a key the tool does not know.
func paInternal(id, archive, platform, groups string) string {
	return `{"id":"` + id + `","name":"TestFlight Internal Testing - ` + platform + `","type":"testFlight_internal",` +
		`"deployment_config":{"testflight_deployment_ids":{"beta_group_ids":[` + groups + `],` +
		`"beta_tester_ids":["eeeeeeee-0000-4000-8000-000000000001"]},"archive_action_id":"` + archive + `"},"extra_key":true}`
}

// paState is the fake web API's copy of the workflow.
type paState struct {
	content string
	at      string
	puts    []string
	// onPut, when set, rewrites what Apple stores from a PUT body.
	onPut func(string) string
}

func (s *paState) doc() string {
	return `{"id":"` + paWF + `","content":` + s.content +
		`,"metadata":{"is_deleted":false,"last_modified_by":"someone","last_modified_at":"` + s.at + `"}}`
}

// paKit is a signed-in kit serving the workflow with content; a PUT
// replaces it and moves last_modified_at.
func paKit(t *testing.T, content string) (*tooltest.Kit, *paState) {
	t.Helper()
	k := kit(t).WebSession()
	paASC(k)
	k.Portal.HandleJSON(http.MethodGet, "/olympus/v1/session", `{"provider":{"providerId":4242424,"publicProviderId":"`+paTeam+`"}}`)
	st := &paState{content: content, at: paAt}
	k.Portal.Handle(http.MethodGet, paPath, func(tooltest.PortalCall) (string, error) { return st.doc(), nil })
	k.Portal.Handle(http.MethodPut, paPath, func(c tooltest.PortalCall) (string, error) {
		st.puts = append(st.puts, c.Body)
		body := c.Body
		if st.onPut != nil {
			body = st.onPut(body)
		}
		st.content, st.at = body, "2026-11-16T10:00:00.000Z"
		return st.doc(), nil
	})
	return k, st
}

// paASC serves the workflow, its product and the app's beta groups.
func paASC(k *tooltest.Kit) {
	k.Resolve.Groups = []tool.Group{
		{ID: paDev, Name: "Developer", Internal: true},
		{ID: paQA, Name: "QA", Internal: true},
		{ID: paExt, Name: "Public"},
	}
	k.ASC.Handle(http.MethodGet, "/v1/ciWorkflows/{id}", http.StatusOK, `{"data":{"type":"ciWorkflows","id":"`+paWF+
		`","attributes":{"name":"Release (Manual)","isEnabled":true},"relationships":{"product":{"data":{"type":"ciProducts","id":"`+paProduct+`"}}}}}`)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/{id}", http.StatusOK, `{"data":{"type":"ciProducts","id":"`+paProduct+
		`","attributes":{"name":"Hjul"},"relationships":{"app":{"data":{"type":"apps","id":"A1"}}}}}`)
}

type paPreview struct {
	Status  string           `json:"status"`
	Preview tool.PlanPreview `json:"preview"`
}

// paPosts decodes post_actions of a PUT body.
func paPosts(t *testing.T, body string) []map[string]any {
	t.Helper()
	var c struct {
		Posts []map[string]any `json:"post_actions"`
	}
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	return c.Posts
}

// paDeployment returns the group ids, tester ids and archive id of a
// decoded TestFlight post-action.
func paDeployment(t *testing.T, p map[string]any) (groups, testers []any, archive string) {
	t.Helper()
	d, _ := p["deployment_config"].(map[string]any)
	ids, _ := d["testflight_deployment_ids"].(map[string]any)
	groups, _ = ids["beta_group_ids"].([]any)
	testers, _ = ids["beta_tester_ids"].([]any)
	archive, _ = d["archive_action_id"].(string)
	if ids == nil || groups == nil || testers == nil {
		t.Fatalf("deployment_config = %v", d)
	}
	return groups, testers, archive
}

var paUUIDv4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestPostActionsRead(t *testing.T) {
	k, _ := paKit(t, paContent("testFlightExternalAndAppStore", `[`+paInternal(paPostIOS, paIOS, "iOS", `"`+paDev+`","cccccccc-0000-4000-8000-0000000000ff"`)+`,`+paNotify+`]`))
	res := tooltest.MustOK(t, tooltest.Call(t, lookup(t, "xcc_post_actions_set"), k.Env, `{"workflow":"`+paWF+`"}`))
	out := res.Data.(postActionsOut)
	if out.AppID != "A1" || out.ProductID != paProduct || out.Status != "" || out.Workflow.ID != paWF ||
		out.Workflow.Name != "Release (Manual)" || out.Workflow.Locked || out.Workflow.LastModifiedAt != paAt {
		t.Fatalf("out = %+v", out)
	}
	if len(out.Archives) != 2 || out.Archives[0] != (paArchive{ID: paIOS, Name: "Archive - iOS", Platform: "IOS", Distribution: "testFlightExternalAndAppStore"}) ||
		out.Archives[1].Platform != "MAC_OS" {
		t.Fatalf("archives = %+v", out.Archives)
	}
	if len(out.PostActions) != 2 {
		t.Fatalf("post_actions = %+v", out.PostActions)
	}
	p := out.PostActions[0]
	if p.ID != paPostIOS || p.Type != "testFlight_internal" || p.ArchiveActionID != paIOS || p.Platform != "IOS" ||
		p.Testers == nil || *p.Testers != 1 || len(p.Groups) != 2 ||
		p.Groups[0] != (paGroup{ID: paDev, Name: "Developer"}) || p.Groups[1] != (paGroup{ID: "cccccccc-0000-4000-8000-0000000000ff"}) {
		t.Fatalf("internal = %+v", p)
	}
	if n := out.PostActions[1]; n.Type != "notification" || n.Platform != "IOS" || n.Groups != nil || n.Testers != nil {
		t.Fatalf("notification = %+v", n)
	}
	if j := tooltest.JSON(t, res.Data); !strings.Contains(j, `"name":{"untrusted":"Release (Manual)"}`) {
		t.Fatalf("workflow name not untrusted: %s", j)
	}
	if bnPuts(k) != 0 {
		t.Fatal("read wrote")
	}
}

func TestPostActionsSetNew(t *testing.T) {
	content := paContent("testFlightExternalAndAppStore", `[]`)
	k, st := paKit(t, content)
	tl := lookup(t, "xcc_post_actions_set")
	const args = `{"workflow":"` + paWF + `","internal_groups":["Developer"]`
	prev := tooltest.Decode[paPreview](t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`}`)))
	if prev.Status != tool.ErrNeedsConfirm.Code ||
		prev.Preview.Summary != `set TestFlight Internal Testing of workflow "Release (Manual)": Archive - iOS → Developer; Archive - macOS → Developer` ||
		!slices.Equal(prev.Preview.Affected, []string{"Xcode Cloud workflow " + paWF}) || len(prev.Preview.Warnings) != 0 {
		t.Fatalf("preview = %+v", prev)
	}
	if bnPuts(k) != 0 {
		t.Fatal("preview wrote")
	}
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`,"confirm":true}`))
	if bnPuts(k) != 1 || len(st.puts) != 1 {
		t.Fatalf("puts = %d", bnPuts(k))
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(st.puts[0]), &after); err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("keys %d, want %d", len(after), len(before))
	}
	for key, v := range before {
		if key == "post_actions" {
			continue
		}
		var a, b bytes.Buffer
		if err := json.Compact(&a, v); err != nil {
			t.Fatal(err)
		}
		if err := json.Compact(&b, after[key]); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if a.String() != b.String() {
			t.Fatalf("%s changed: %s → %s", key, a.String(), b.String())
		}
	}
	posts := paPosts(t, st.puts[0])
	if len(posts) != 2 {
		t.Fatalf("post_actions = %v", posts)
	}
	for i, want := range []struct{ archive, name string }{{paIOS, "TestFlight Internal Testing - iOS"}, {paMac, "TestFlight Internal Testing - macOS"}} {
		p := posts[i]
		groups, testers, archive := paDeployment(t, p)
		id, _ := p["id"].(string)
		if !paUUIDv4.MatchString(id) || p["name"] != want.name || p["type"] != "testFlight_internal" || archive != want.archive ||
			len(groups) != 1 || groups[0] != paDev || len(testers) != 0 || len(p) != 4 {
			t.Fatalf("post_actions[%d] = %v", i, p)
		}
	}
	if posts[0]["id"] == posts[1]["id"] {
		t.Fatal("ids repeat")
	}
	out := res.Data.(postActionsOut)
	if out.Status != "set" || out.Workflow.LastModifiedAt == paAt || len(out.PostActions) != 2 ||
		out.PostActions[1].Platform != "MAC_OS" || out.PostActions[1].Groups[0].Name != "Developer" {
		t.Fatalf("out = %+v", out)
	}
	if len(res.Meta.Warnings) != 0 {
		t.Fatalf("warnings = %v", res.Meta.Warnings)
	}
}

func TestPostActionsSetReplacesGroups(t *testing.T) {
	k, st := paKit(t, paContent("testFlightInternalOnly", `[`+paInternal(paPostIOS, paIOS, "iOS", `"`+paQA+`"`)+`,`+paNotify+`]`))
	tl := lookup(t, "xcc_post_actions_set")
	const args = `{"workflow":"` + paWF + `","internal_groups":["Developer"]`
	prev := tooltest.Decode[paPreview](t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`}`)))
	if !slices.Contains(prev.Preview.Warnings, `replaces the current groups of "Archive - iOS": QA`) {
		t.Fatalf("preview = %+v", prev)
	}
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`,"confirm":true}`))
	if len(st.puts) != 1 {
		t.Fatalf("puts = %d", len(st.puts))
	}
	posts := paPosts(t, st.puts[0])
	if len(posts) != 3 || posts[1]["type"] != "notification" || posts[2]["deployment_config"].(map[string]any)["archive_action_id"] != paMac {
		t.Fatalf("post_actions = %v", posts)
	}
	p := posts[0]
	groups, testers, archive := paDeployment(t, p)
	if p["id"] != paPostIOS || p["name"] != "TestFlight Internal Testing - iOS" || p["extra_key"] != true || archive != paIOS ||
		len(groups) != 1 || groups[0] != paDev || len(testers) != 1 || testers[0] != "eeeeeeee-0000-4000-8000-000000000001" {
		t.Fatalf("kept post-action = %v", p)
	}
}

func TestPostActionsAlready(t *testing.T) {
	both := `"` + paQA + `","` + paDev + `"`
	k, _ := paKit(t, paContent("testFlightInternalOnly", `[`+paInternal(paPostIOS, paIOS, "iOS", both)+`,`+paInternal(paPostMac, paMac, "macOS", both)+`]`))
	res := tooltest.MustOK(t, tooltest.Call(t, lookup(t, "xcc_post_actions_set"), k.Env,
		`{"workflow":"`+paWF+`","internal_groups":["Developer","QA","developer"]}`))
	if out := res.Data.(postActionsOut); out.Status != "already" || len(out.PostActions) != 2 {
		t.Fatalf("out = %+v", out)
	}
	if bnPuts(k) != 0 {
		t.Fatal("already wrote")
	}
}

func TestPostActionsRemove(t *testing.T) {
	k, st := paKit(t, paContent("testFlightInternalOnly",
		`[`+paInternal(paPostIOS, paIOS, "iOS", `"`+paDev+`"`)+`,`+paNotify+`,`+paInternal(paPostMac, paMac, "macOS", `"`+paQA+`"`)+`]`))
	tl := lookup(t, "xcc_post_actions_set")
	const args = `{"workflow":"` + paWF + `","remove_internal":true`
	prev := tooltest.Decode[paPreview](t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`}`)))
	if len(prev.Preview.Warnings) != 2 || !strings.HasPrefix(prev.Preview.Summary, "remove TestFlight Internal Testing") {
		t.Fatalf("preview = %+v", prev)
	}
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`,"confirm":true}`))
	if posts := paPosts(t, st.puts[0]); len(posts) != 1 || posts[0]["type"] != "notification" {
		t.Fatalf("post_actions = %v", posts)
	}
	if out := res.Data.(postActionsOut); out.Status != "removed" || len(out.PostActions) != 1 {
		t.Fatalf("out = %+v", out)
	}
	// Nothing left to remove.
	res = tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`}`))
	if out := res.Data.(postActionsOut); out.Status != "already" || bnPuts(k) != 1 {
		t.Fatalf("out = %+v puts %d", out, bnPuts(k))
	}
}

func TestPostActionsPlatforms(t *testing.T) {
	k, st := paKit(t, paContent("testFlightInternalOnly", `[]`))
	tl := lookup(t, "xcc_post_actions_set")
	const args = `{"workflow":"` + paWF + `","internal_groups":["Developer"],"platforms":["IOS"]`
	prev := tooltest.Decode[paPreview](t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`}`)))
	if strings.Contains(prev.Preview.Summary, "macOS") {
		t.Fatalf("summary = %s", prev.Preview.Summary)
	}
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`,"confirm":true}`))
	posts := paPosts(t, st.puts[0])
	if len(posts) != 1 {
		t.Fatalf("post_actions = %v", posts)
	}
	if _, _, archive := paDeployment(t, posts[0]); archive != paIOS {
		t.Fatalf("archive = %s", archive)
	}
}

func TestPostActionsRefusals(t *testing.T) {
	ok := paContent("testFlightInternalOnly", `[]`)
	tl := lookup(t, "xcc_post_actions_set")
	const wf = `{"workflow":"` + paWF + `",`
	for _, c := range []struct{ name, content, args string }{
		{"locked", strings.Replace(ok, `"locked":false`, `"locked":true`, 1), wf + `"internal_groups":["Developer"],"confirm":true}`},
		{"distribution none", paContent("none", `[]`), wf + `"internal_groups":["Developer"],"confirm":true}`},
		{"external group", ok, wf + `"internal_groups":["Public"],"confirm":true}`},
		{"both", ok, wf + `"internal_groups":["Developer"],"remove_internal":true,"confirm":true}`},
		{"platforms alone", ok, wf + `"platforms":["IOS"]}`},
		{"unknown platform", ok, wf + `"internal_groups":["Developer"],"platforms":["ANDROID"],"confirm":true}`},
		{"platform without archive", ok, wf + `"internal_groups":["Developer"],"platforms":["TV_OS"],"confirm":true}`},
	} {
		k, _ := paKit(t, c.content)
		tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, c.args), tool.ErrInvalidArgs.Code)
		if bnPuts(k) != 0 {
			t.Errorf("%s: wrote", c.name)
		}
	}
}

// A confirm after someone saved the workflow since the preview needs a new
// preview.
func TestPostActionsConfirmBoundToLastSave(t *testing.T) {
	k, st := paKit(t, paContent("testFlightInternalOnly", `[]`))
	tl := lookup(t, "xcc_post_actions_set")
	const args = `{"workflow":"` + paWF + `","internal_groups":["Developer"]`
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`}`))
	st.at = "2026-11-10T00:00:00.000Z"
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, args+`,"confirm":true}`), tool.ErrPreviewRequired.Code)
	if bnPuts(k) != 0 {
		t.Fatal("stale confirm wrote")
	}
}

func TestPostActionsReadOnly(t *testing.T) {
	k, _ := paKit(t, paContent("testFlightInternalOnly", `[]`))
	k.ReadOnly()
	tl := lookup(t, "xcc_post_actions_set")
	if !tl.ListedIn(tool.ProfileInfo{}) {
		t.Fatal("not listed read-only")
	}
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"workflow":"`+paWF+`"}`))
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"workflow":"`+paWF+`","internal_groups":["Developer"],"confirm":true}`), "writes_disabled")
	if bnPuts(k) != 0 {
		t.Fatal("PUT in read-only mode")
	}
}

func TestPostActionsErrors(t *testing.T) {
	tl := lookup(t, "xcc_post_actions_set")
	// Workflow of another team.
	k, _ := paKit(t, paContent("testFlightInternalOnly", `[]`))
	k.Portal.HandleError(http.MethodGet, paPath, http.StatusNotFound)
	b := tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"workflow":"`+paWF+`"}`), "not_found")
	if j := tooltest.JSON(t, b); !strings.Contains(j, paWF) || !strings.Contains(j, "web session's team") {
		t.Errorf("not found = %s", j)
	}

	// A PUT that may have landed suggests reading the workflow back.
	k, _ = paKit(t, paContent("testFlightInternalOnly", `[]`))
	k.Portal.Handle(http.MethodPut, paPath, func(tooltest.PortalCall) (string, error) {
		return "", errors.New("connection reset")
	})
	const args = `{"workflow":"` + paWF + `","internal_groups":["Developer"]`
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`}`))
	res := tooltest.Call(t, tl, k.Env, args+`,"confirm":true}`)
	if !res.IsError || !strings.Contains(tooltest.JSON(t, res), `"xcc_post_actions_set"`) {
		t.Fatalf("res = %s", tooltest.JSON(t, res))
	}

	// No web session.
	k = kit(t)
	paASC(k)
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"workflow":"`+paWF+`"}`), "web_session_required")
}

// Apple normalises other parts of the workflow on save: still set, with a
// warning naming them.
func TestPostActionsReadBackDrift(t *testing.T) {
	k, st := paKit(t, paContent("testFlightInternalOnly", `[]`))
	st.onPut = func(body string) string { return strings.Replace(body, paIOSDest, "", 1) }
	tl := lookup(t, "xcc_post_actions_set")
	const args = `{"workflow":"` + paWF + `","internal_groups":["Developer"]`
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`}`))
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args+`,"confirm":true}`))
	if out := res.Data.(postActionsOut); out.Status != "set" {
		t.Fatalf("out = %+v", out)
	}
	if !slices.Contains(res.Meta.Warnings, "Apple changed other parts of the workflow on save: actions") || len(res.Meta.Warnings) != 1 {
		t.Fatalf("warnings = %v", res.Meta.Warnings)
	}
}
