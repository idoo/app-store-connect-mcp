package metadata

import (
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// R7 AO2: with no editable version metadata_plan previews the full diff
// against the live version (limits still checked) but issues no plan_id, and
// metadata_apply refuses to act on it.
func TestPlanPreviewsAgainstLiveWithoutEditable(t *testing.T) {
	k := kit(t)
	delete(k.Resolve.Versions, tool.VersionEditable)
	listing(k)
	args := `{"app":"Hjul","listing":{"en-US":{"keywords":"bike,ride,cycle"}}}`
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, args))
	out := decode(t, res)
	if out["applicable"] != false || out["reason"] != "no editable version; create one with release_prepare" || out["plan_id"] != nil {
		t.Fatalf("out = %v", out)
	}
	if v := out["version"].(map[string]any); v["id"] != "v1" {
		t.Errorf("version = %v, want live v1", v)
	}
	if !strings.Contains(mustJSON(t, out["changes"]), `"keywords"`) {
		t.Errorf("changes = %v", out["changes"])
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "release_prepare" {
		t.Errorf("next = %+v", res.Next)
	}

	long := `{"app":"Hjul","listing":{"en-US":{"subtitle":"` + strings.Repeat("x", 31) + `"}}}`
	inv := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, long)))
	if inv["applicable"] != false || !strings.Contains(mustJSON(t, inv["errors"]), "too_long") || inv["plan_id"] != nil {
		t.Errorf("limits not checked in preview: %v", inv)
	}

	confirmed := strings.TrimSuffix(args, "}") + `,"plan_id":"forged"}`
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "metadata_apply"), k.Env, confirmed), "not_found")
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("preview applied: %d mutations", n)
	}
	if n := len(k.ASC.RequestsTo(http.MethodPatch, "/v1/appStoreVersionLocalizations/{id}")); n != 0 {
		t.Fatalf("patched live version %d times", n)
	}
}
