package testflight

import (
	"net/http"
	"slices"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// A confirm acts on the builds the preview showed: when "latest" resolves to a
// newer build between preview and confirm, the confirm is refused instead of
// expiring a build nobody previewed.
func TestBuildsExpireConfirmBindsResolvedBuilds(t *testing.T) {
	doc := `{"data":[
		{"type":"builds","id":"b0","attributes":{"version":"41","expired":false}},
		{"type":"builds","id":"b1","attributes":{"version":"42","expired":false}}
	],"links":{}}`
	k := newKit(t)
	k.Env.Profile.AllowNoElicit = true
	k.Resolve.Builds = map[tool.BuildRef]tool.Build{"latest": {ID: "b0", Number: "41"}}
	k.ASC.Handle(http.MethodGet, "/v1/builds", http.StatusOK, doc)
	k.ASC.Handle(http.MethodPatch, "/v1/builds/{id}", http.StatusOK, `{"data":{"type":"builds","id":"b1","attributes":{"expired":true}}}`)
	be := mustTool(t, "builds_expire")

	tooltest.MustOK(t, tooltest.Call(t, be, k.Env, `{"builds":["latest"]}`))
	k.Resolve.Builds["latest"] = tool.Build{ID: "b1", Number: "42"} // a newer upload lands
	tooltest.MustFail(t, tooltest.Call(t, be, k.Env, `{"builds":["latest"],"confirm":true}`), "preview_required")
	if m := k.ASC.Mutations(); len(m) != 0 {
		t.Fatalf("confirm of a changed target wrote: %v", paths(m))
	}

	// A fresh preview of the new target confirms it.
	tooltest.MustOK(t, tooltest.Call(t, be, k.Env, `{"builds":["latest"]}`))
	tooltest.MustOK(t, tooltest.Call(t, be, k.Env, `{"builds":["latest"],"confirm":true}`))
	if m := paths(k.ASC.Mutations()); !slices.Equal(m, []string{"PATCH /v1/builds/b1"}) {
		t.Fatalf("mutations = %v", m)
	}
}
