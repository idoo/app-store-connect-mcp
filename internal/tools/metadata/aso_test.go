package metadata

import (
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func TestPullFieldsFilter(t *testing.T) {
	k := kit(t)
	listing(k)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_pull"), k.Env, `{"fields":["subtitle","keywords"]}`))
	locs := decode(t, res)["locales"].(map[string]any)
	if len(locs) != 2 {
		t.Fatalf("locales = %v", locs)
	}
	for code, v := range locs {
		m := v.(map[string]any)
		if _, ok := m["description"]; ok || len(m) == 0 || len(m) > 2 {
			t.Errorf("%s = %v; want only subtitle/keywords", code, m)
		}
	}
	if en := locs["en-US"].(map[string]any); en["subtitle"] != "Ride more" || en["keywords"] != "bike,ride" {
		t.Errorf("en-US = %v", en)
	}
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "metadata_pull"), k.Env, `{"fields":["title"]}`), "invalid_arguments")
}

func TestPlanLiveVersionPromoOnly(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appInfos", http.StatusOK,
		`{"data":[{"type":"appInfos","id":"info1","attributes":{"state":"READY_FOR_DISTRIBUTION"}}],"links":{}}`)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", http.StatusOK, versionLocs("Ride"))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env,
		`{"version":"live","listing":{"locales":{"en-US":{"name":"Hjul","subtitle":"New","description":"Changed","promotionalText":"Summer sale"}}}}`))
	out := decode(t, res)
	if out["status"] != statusPlanned {
		t.Fatalf("out = %v", out)
	}
	ch := out["changes"].([]any)
	if len(ch) != 1 {
		t.Fatalf("changes = %v", ch)
	}
	f := ch[0].(map[string]any)["fields"].([]any)
	if len(f) != 1 || f[0].(map[string]any)["field"] != "promotionalText" {
		t.Errorf("fields = %v", f)
	}
	if !strings.Contains(stringify(out["warnings"]), "field_locked_on_live") {
		t.Errorf("warnings = %v", out["warnings"])
	}
}

// Promotional text on the live version is public at once: applying it needs
// a human even with a valid plan_id, and nothing is written without one.
func TestApplyLivePromoNeedsHuman(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appInfos", http.StatusOK,
		`{"data":[{"type":"appInfos","id":"info1","attributes":{"state":"READY_FOR_DISTRIBUTION"}}],"links":{}}`)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", http.StatusOK, versionLocs("Ride"))
	args := `{"version":"live","listing":{"locales":{"en-US":{"promotionalText":"Summer sale"}}}}`
	plan := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, args)))
	id, _ := plan["plan_id"].(string)
	if id == "" {
		t.Fatalf("plan = %v", plan)
	}
	confirmed := strings.TrimSuffix(args, "}") + `,"plan_id":"` + id + `"}`
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "metadata_apply"), k.Env, confirmed), tool.ErrHumanRequired.Code)
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("live promo applied without a human: %d mutations", n)
	}
}

func TestPlanAppInfoInReviewIsNotEditable(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appInfos", http.StatusOK, `{"data":[
 {"type":"appInfos","id":"info1","attributes":{"state":"READY_FOR_DISTRIBUTION"}},
 {"type":"appInfos","id":"info3","attributes":{"state":"WAITING_FOR_REVIEW"}}],"links":{}}`)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", http.StatusOK, versionLocs("Ride"))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env,
		`{"listing":{"locales":{"en-US":{"subtitle":"New"}}}}`))
	out := decode(t, res)
	if out["status"] != statusInvalid || !strings.Contains(stringify(out["errors"]), "WAITING_FOR_REVIEW") {
		t.Errorf("out = %v", out)
	}
	if out["app_info"] != nil {
		t.Errorf("in-review app info treated as editable: %v", out["app_info"])
	}
}

func stringify(v any) string {
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case string:
			b.WriteString(x + " ")
		}
	}
	walk(v)
	return b.String()
}
