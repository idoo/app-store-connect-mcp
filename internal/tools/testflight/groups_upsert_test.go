package testflight

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// extGroupDoc is a betaGroups collection with one external group "Beta".
const extGroupDoc = `{"data":[{"type":"betaGroups","id":"g-beta","attributes":{"name":"Beta","isInternalGroup":false,
	"publicLinkEnabled":false,"publicLinkLimitEnabled":false,"publicLinkLimit":null,"feedbackEnabled":true}}],"links":{}}`

func upsertCall(t *testing.T, k *tooltest.Kit, args string) *tool.Result {
	t.Helper()
	return tooltest.Call(t, mustTool(t, "testflight_groups_upsert"), k.Env, args)
}

func TestGroupsUpsertCreates(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/betaGroups", http.StatusOK, `{"data":[],"links":{}}`)
	k.ASC.Handle(http.MethodPost, "/v1/betaGroups", http.StatusCreated,
		`{"data":{"type":"betaGroups","id":"g-new","attributes":{"name":"Friends","isInternalGroup":false,"feedbackEnabled":false}}}`)
	res := tooltest.MustOK(t, upsertCall(t, k, `{"name":"Friends","feedback_enabled":false}`))
	got := tooltest.Decode[groupUpsertOut](t, res)
	if got.Outcome != outcomeCreated || got.Group.ID != "g-new" {
		t.Fatalf("result = %s", tooltest.JSON(t, res.Data))
	}
	m := k.ASC.Mutations()
	if len(m) != 1 || m[0].Method != http.MethodPost || !strings.Contains(string(tooltest.JSON(t, m[0].Body)), `"feedbackEnabled":false`) {
		t.Fatalf("mutations = %v", paths(m))
	}
}

func TestGroupsUpsertExistingPreviewsThenUpdates(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/betaGroups", http.StatusOK, extGroupDoc)
	k.ASC.Handle(http.MethodPatch, "/v1/betaGroups/{id}", http.StatusOK,
		`{"data":{"type":"betaGroups","id":"g-beta","attributes":{"name":"Beta","feedbackEnabled":false}}}`)
	args := `{"name":"beta","feedback_enabled":false,"public_link_limit":500}`
	prev := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, upsertCall(t, k, args)))
	if prev["status"] != tool.ErrNeedsConfirm.Code || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("preview = %+v", prev)
	}
	p := prev["preview"].(map[string]any)
	if cur := p["current"].(map[string]any); cur["feedbackEnabled"] != true || cur["publicLinkLimitEnabled"] != false {
		t.Fatalf("current = %+v", cur)
	}
	if in := p["intended"].(map[string]any); in["feedbackEnabled"] != false || in["publicLinkLimit"] != float64(500) {
		t.Fatalf("intended = %+v", in)
	}
	res := tooltest.MustOK(t, upsertCall(t, k, strings.Replace(args, "{", `{"confirm":true,`, 1)))
	if got := tooltest.Decode[groupUpsertOut](t, res); got.Outcome != outcomeUpdated || len(got.Changed) != 3 {
		t.Fatalf("result = %s", tooltest.JSON(t, res.Data))
	}
	if m := paths(k.ASC.Mutations()); !slices.Equal(m, []string{"PATCH /v1/betaGroups/g-beta"}) {
		t.Fatalf("mutations = %v", m)
	}
}

func TestGroupsUpsertPublicLinkNeedsHuman(t *testing.T) {
	for _, tc := range []struct{ name, groups string }{
		{"update", extGroupDoc},
		{"create", `{"data":[],"links":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := newKit(t)
			k.ASC.Handle(http.MethodGet, "/v1/betaGroups", http.StatusOK, tc.groups)
			args := `{"name":"Beta","public_link_enabled":true,"confirm":true}`
			k.Elicit(false)
			tooltest.MustFail(t, upsertCall(t, k, args), "declined")
			if len(k.ASC.Mutations()) != 0 {
				t.Fatalf("declined call mutated: %v", paths(k.ASC.Mutations()))
			}
			k.ASC.Handle(http.MethodPatch, "/v1/betaGroups/{id}", http.StatusOK,
				`{"data":{"type":"betaGroups","id":"g-beta","attributes":{"name":"Beta","publicLinkEnabled":true}}}`)
			k.ASC.Handle(http.MethodPost, "/v1/betaGroups", http.StatusCreated,
				`{"data":{"type":"betaGroups","id":"g-beta","attributes":{"name":"Beta","publicLinkEnabled":true}}}`)
			asked := k.Elicit(true)
			res := tooltest.MustOK(t, upsertCall(t, k, args))
			if len(*asked) != 1 || res.Meta.ApprovedBy != tool.ApprovedHuman || len(k.ASC.Mutations()) != 1 {
				t.Fatalf("asked = %v, approved_by = %q, mutations = %v", *asked, res.Meta.ApprovedBy, paths(k.ASC.Mutations()))
			}
		})
	}
}

func TestGroupsUpsertRepeatIsAlready(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/betaGroups", http.StatusOK, extGroupDoc)
	res := tooltest.MustOK(t, upsertCall(t, k, `{"name":"Beta","feedback_enabled":true,"public_link_enabled":false,"public_link_limit":0}`))
	if d := tooltest.Decode[map[string]any](t, res); d["already"] != true || d["outcome"] != outcomeAlready || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("result = %s", tooltest.JSON(t, res.Data))
	}
}

func TestGroupsUpsertRejectsBadArgs(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/betaGroups", http.StatusOK,
		`{"data":[{"type":"betaGroups","id":"g-int","attributes":{"name":"Team","isInternalGroup":true}}],"links":{}}`)
	for _, args := range []string{
		`{"name":"Beta","public_link_limit":10001}`,
		`{"name":"Beta","public_link_limit":-1}`,
		`{"name":"  "}`,
		`{"name":"Team","public_link_enabled":true}`,
	} {
		tooltest.MustFail(t, upsertCall(t, k, args), tool.ErrInvalidArgs.Code)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("invalid call mutated")
	}
}
