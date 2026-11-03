package portalsigning

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const team = "ABCDE12345"

func mustTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tl
}

// kit is a signed-in Kit with two bundle IDs: B1 com.example.app (App Groups
// with G1, INCREASED_MEMORY_LIMIT) and B2 com.example.app.widgets (nothing).
func kit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t).WebSession()
	k.ASC.Handle("GET", "/v1/bundleIds", 200, `{"data":[
		{"type":"bundleIds","id":"B1","attributes":{"identifier":"com.example.app","seedId":"`+team+`"}},
		{"type":"bundleIds","id":"B2","attributes":{"identifier":"com.example.app.widgets","seedId":"`+team+`"}}]}`)
	k.ASC.Handle("GET", "/v1/bundleIds/B1", 200, `{"data":{"type":"bundleIds","id":"B1","attributes":{"identifier":"com.example.app","seedId":"`+team+`"}}}`)
	k.DevPortal.HandleJSON("GET", "v1/bundleIds/B1", `{"data":{"type":"bundleIds","id":"B1"},"included":[
		{"type":"bundleIdCapabilities","id":"B1_APP_GROUPS","attributes":{"enabled":true,"settings":[]},
		 "relationships":{"capability":{"data":{"type":"capabilities","id":"APP_GROUPS"}},"appGroups":{"data":[{"type":"appGroups","id":"G1"}]}}},
		{"type":"bundleIdCapabilities","id":"B1_INCREASED_MEMORY_LIMIT","attributes":{"enabled":true},
		 "relationships":{"capability":{"data":{"type":"capabilities","id":"INCREASED_MEMORY_LIMIT"}}}},
		{"type":"capabilities","id":"APP_GROUPS","attributes":{"name":"App Groups"}},
		{"type":"capabilities","id":"INCREASED_MEMORY_LIMIT","attributes":{"name":"Increased Memory Limit"}}]}`)
	k.DevPortal.HandleJSON("GET", "v1/bundleIds/B2", `{"data":{"type":"bundleIds","id":"B2"},"included":[]}`)
	catalog := `{"data":[
		{"type":"capabilities","id":"APP_GROUPS","attributes":{"name":"App Groups"}},
		{"type":"capabilities","id":"INCREASED_MEMORY_LIMIT","attributes":{"name":"Increased Memory Limit"}},
		{"type":"capabilities","id":"BACKGROUND_GPU_ACCESS","attributes":{"name":"Background GPU Access"}}]}`
	k.DevPortal.HandleJSON("GET", "v1/capabilities", catalog)
	k.DevPortal.HandleJSON("POST", pathListGroups, `{"applicationGroupList":[
		{"applicationGroup":"G1","name":"Old","identifier":"group.com.example.old","status":"current"}]}`)
	return k
}

func TestPortalGet(t *testing.T) {
	k := kit(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "signing_portal_get"), k.Env, `{"bundle_id":"com.example.app"}`))
	out := tooltest.Decode[portalGetOut](t, res)
	if out.TeamID != team || out.BundleID != "B1" || len(out.Capabilities) != 2 || out.Capabilities[1].ID != "INCREASED_MEMORY_LIMIT" ||
		out.Capabilities[1].Name != "Increased Memory Limit" || len(out.AppGroups) != 1 || out.AppGroups[0].Identifier != "group.com.example.old" ||
		len(out.Available) != 3 {
		t.Fatalf("out = %+v", out)
	}
	read := k.DevPortal.Calls()[0]
	if !strings.Contains(read.Body, `"teamId":"`+team+`"`) || !strings.Contains(read.Body, "bundleIdCapabilities.appGroups") {
		t.Errorf("read body = %s", read.Body)
	}
}

func TestAppGroupsListFilterAndTeamArg(t *testing.T) {
	k := kit(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "signing_app_groups_list"), k.Env, `{"filter":"OLD","team_id":"`+team+`"}`))
	out := tooltest.Decode[groupsListOut](t, res)
	if out.Count != 1 || out.AppGroups[0].ID != "G1" {
		t.Fatalf("out = %+v", out)
	}
	res = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "signing_app_groups_list"), k.Env, `{"filter":"nope"}`))
	if out := tooltest.Decode[groupsListOut](t, res); out.Count != 0 || out.TeamID != team {
		t.Fatalf("out = %+v", out)
	}
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "signing_app_groups_list"), k.Env, `{"team_id":"bad"}`), tool.ErrInvalidArgs.Code)
}

func TestExpiredSession(t *testing.T) {
	k := kit(t)
	k.DevPortal.Handle("GET", "v1/bundleIds/B1", func(tooltest.PortalCall) (string, error) { return "", tool.ErrWebSessionRequired })
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "signing_portal_get"), k.Env, `{"bundle_id":"B1"}`), tool.ErrWebSessionRequired.Code)
	// No web session at all: the stand-in answers.
	k2 := kit(t)
	k2.Env.DevPortal = nil
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "signing_app_groups_list"), k2.Env, `{"team_id":"`+team+`"}`), tool.ErrWebSessionRequired.Code)
}

const applyArgs = `"bundle_ids":["com.example.app","com.example.app.widgets"],"app_groups":["group.com.example.shared","group.com.example.old"],"capabilities":["background gpu","Increased Memory Limit"]`

type previewOut struct {
	Status string   `json:"status"`
	PlanID string   `json:"plan_id"`
	Plan   applyOut `json:"plan"`
}

func planApply(t *testing.T, k *tooltest.Kit, args string) previewOut {
	t.Helper()
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "signing_portal_apply"), k.Env, "{"+args+"}"))
	p := tooltest.Decode[previewOut](t, res)
	if p.PlanID == "" || len(res.Next) != 1 || res.Next[0].Args["plan_id"] != p.PlanID {
		t.Fatalf("preview = %+v next = %+v", p, res.Next)
	}
	return p
}

func ops(steps []step) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Op + ":" + s.BundleID + ":" + s.Target + ":" + s.Outcome
	}
	return out
}

func TestApplyPreviewDiff(t *testing.T) {
	k := kit(t)
	p := planApply(t, k, applyArgs)
	want := []string{
		"create_group::group.com.example.shared:",
		"assign_groups:B1:APP_GROUPS:", "assign_groups:B2:APP_GROUPS:",
		"enable:B1:BACKGROUND_GPU_ACCESS:", "enable:B2:BACKGROUND_GPU_ACCESS:", "enable:B2:INCREASED_MEMORY_LIMIT:",
	}
	if got := ops(p.Plan.Steps); !slices.Equal(got, want) {
		t.Fatalf("steps = %v", got)
	}
	b1 := p.Plan.Bundles[0]
	if !slices.Equal(b1.AddGroups, []string{"group.com.example.shared"}) || !slices.Equal(b1.AlreadyGroups, []string{"group.com.example.old"}) ||
		!slices.Equal(b1.AlreadyEnabled, []string{"INCREASED_MEMORY_LIMIT"}) {
		t.Errorf("B1 plan = %+v", b1)
	}
	// Full set: existing + new.
	if g := p.Plan.Steps[1].Groups; !slices.Equal(g, []string{"group.com.example.old", "group.com.example.shared"}) {
		t.Errorf("B1 groups = %v", g)
	}
	if g := p.Plan.Steps[2].Groups; !slices.Equal(g, []string{"group.com.example.shared", "group.com.example.old"}) {
		t.Errorf("B2 groups = %v", g)
	}
	for _, c := range k.DevPortal.Calls() {
		if c.Method != "GET" && c.Path != pathListGroups {
			t.Errorf("preview wrote %s %s", c.Method, c.Path)
		}
	}
	// Ambiguous or unknown capability names are refused.
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "signing_portal_apply"), k.Env, `{"bundle_ids":["B1"],"capabilities":["o"]}`), tool.ErrInvalidArgs.Code)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "signing_portal_apply"), k.Env, `{"bundle_ids":["B1"],"capabilities":["Siri"]}`), tool.ErrInvalidArgs.Code)
}

func applyRoutes(k *tooltest.Kit) {
	k.DevPortal.HandleJSON("POST", pathAddGroup, `{"applicationGroup":{"applicationGroup":"G2","identifier":"group.com.example.shared"},"resultCode":0}`)
	k.DevPortal.HandleJSON("PATCH", "v1/bundleIds/B1", `{}`)
	k.DevPortal.HandleJSON("PATCH", "v1/bundleIds/B2", `{}`)
}

func TestApplyCreatesAssignsEnables(t *testing.T) {
	k := kit(t)
	applyRoutes(k)
	p := planApply(t, k, applyArgs)
	asked := k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "signing_portal_apply"), k.Env, "{"+applyArgs+`,"plan_id":"`+p.PlanID+`"}`))
	if len(*asked) != 1 {
		t.Errorf("human asked %d times (creating a group needs approval)", len(*asked))
	}
	out := tooltest.Decode[applyOut](t, res)
	for _, s := range out.Steps {
		if s.Outcome != outcomeDone {
			t.Errorf("step %+v", s)
		}
	}
	if out.Status != "applied" || len(res.Next) != 2 || res.Next[0].Tool != "signing_profiles_list" || len(res.Meta.Warnings) == 0 {
		t.Errorf("out = %+v next = %+v", out, res.Next)
	}
	var create, assign string
	for _, c := range k.DevPortal.Calls() {
		switch {
		case c.Path == pathAddGroup:
			create = c.Body
		case c.Method == "PATCH" && c.Path == "v1/bundleIds/B1" && assign == "":
			assign = c.Body
		}
	}
	if !strings.Contains(create, `"identifier":["group.com.example.shared"]`) || !strings.Contains(create, `"name":["com example shared"]`) {
		t.Errorf("create body = %s", create)
	}
	var body struct {
		Data struct {
			Attributes    map[string]any `json:"attributes"`
			Relationships struct {
				Caps struct {
					Data []struct {
						Relationships struct {
							Capability struct{ Data jaLink }   `json:"capability"`
							Groups     struct{ Data []jaLink } `json:"appGroups"`
						} `json:"relationships"`
					} `json:"data"`
				} `json:"bundleIdCapabilities"`
			} `json:"relationships"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(assign), &body); err != nil {
		t.Fatal(err)
	}
	rel := body.Data.Relationships.Caps.Data[0].Relationships
	if body.Data.Attributes["teamId"] != team || rel.Capability.Data.ID != capAppGroups ||
		len(rel.Groups.Data) != 2 || rel.Groups.Data[0].ID != "G1" || rel.Groups.Data[1].ID != "G2" {
		t.Errorf("assign body = %s", assign)
	}
}

func TestApplyEnableOnlyNeedsNoHuman(t *testing.T) {
	k := kit(t)
	applyRoutes(k)
	args := `"bundle_ids":["com.example.app"],"capabilities":["BACKGROUND_GPU_ACCESS"]`
	p := planApply(t, k, args)
	// No elicitation: a plan-only gate applies with the plan_id alone.
	res := tooltest.Call(t, mustTool(t, "signing_portal_apply"), k.Env, "{"+args+`,"plan_id":"`+p.PlanID+`"}`)
	out := tooltest.Decode[applyOut](t, tooltest.MustOK(t, res))
	if !slices.Equal(ops(out.Steps), []string{"enable:B1:BACKGROUND_GPU_ACCESS:done"}) {
		t.Errorf("steps=%v", ops(out.Steps))
	}
}

func TestApplyDisableNeedsHuman(t *testing.T) {
	k := kit(t)
	applyRoutes(k)
	args := `"bundle_ids":["com.example.app"],"disable":["increased memory limit"]`
	p := planApply(t, k, args)
	k.Elicit(false)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "signing_portal_apply"), k.Env, "{"+args+`,"plan_id":"`+p.PlanID+`"}`), tool.ErrDeclined.Code)
	for _, c := range k.DevPortal.Calls() {
		if c.Method == "PATCH" {
			t.Fatal("declined disable was sent")
		}
	}
}

func TestApplyPartialNeverReplays(t *testing.T) {
	k := kit(t)
	applyRoutes(k)
	k.DevPortal.HandleError("PATCH", "v1/bundleIds/B2", 503, "Service Unavailable")
	p := planApply(t, k, applyArgs)
	k.Elicit(true)
	res := tooltest.Call(t, mustTool(t, "signing_portal_apply"), k.Env, "{"+applyArgs+`,"plan_id":"`+p.PlanID+`"}`)
	if !res.IsError {
		t.Fatal("partial failure reported as success")
	}
	part := tooltest.Decode[applyPartial](t, res)
	want := []string{
		"create_group::group.com.example.shared:done",
		"assign_groups:B1:APP_GROUPS:done", "assign_groups:B2:APP_GROUPS:outcome_unknown",
		"enable:B1:BACKGROUND_GPU_ACCESS:not_attempted", "enable:B2:BACKGROUND_GPU_ACCESS:not_attempted", "enable:B2:INCREASED_MEMORY_LIMIT:not_attempted",
	}
	if got := ops(part.Partial.Steps); part.Partial.Status != "partial" || !slices.Equal(got, want) {
		t.Fatalf("steps = %v", got)
	}
	n := 0
	for _, c := range k.DevPortal.Calls() {
		if c.Method == "PATCH" && c.Path == "v1/bundleIds/B2" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("B2 PATCH sent %d times", n)
	}
	if res.Next[len(res.Next)-1].Tool != "signing_portal_get" {
		t.Errorf("next = %+v", res.Next)
	}
}

func TestApplyStalePlanRefused(t *testing.T) {
	k := kit(t)
	applyRoutes(k)
	args := `"bundle_ids":["com.example.app"],"capabilities":["BACKGROUND_GPU_ACCESS"]`
	p := planApply(t, k, args)
	// Someone enabled it meanwhile.
	k.DevPortal.HandleJSON("GET", "v1/bundleIds/B1", `{"data":{"type":"bundleIds","id":"B1"},"included":[
		{"type":"bundleIdCapabilities","id":"B1_X","relationships":{"capability":{"data":{"type":"capabilities","id":"PUSH_NOTIFICATIONS"}}}}]}`)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "signing_portal_apply"), k.Env, "{"+args+`,"plan_id":"`+p.PlanID+`"}`), tool.ErrStalePlan.Code)
}

func TestOutcomeOf(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{&tool.PortalError{Status: 502}, outcomeUnknown},
		{&tool.PortalError{Status: 409}, outcomeError},
		{tool.ErrWebSessionRequired, outcomeError},
		{tool.ErrOutcomeUnknown, outcomeUnknown},
	} {
		if got := outcomeOf(c.err); got != c.want {
			t.Errorf("outcomeOf(%v) = %s", c.err, got)
		}
	}
}
