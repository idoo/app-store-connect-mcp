package metadata

import (
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// liveOnlyKit is an app with only a live version and only a live app info.
func liveOnlyKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := kit(t)
	delete(k.Resolve.Versions, tool.VersionEditable)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appInfos", http.StatusOK,
		`{"data":[{"type":"appInfos","id":"info1","attributes":{"state":"READY_FOR_DISTRIBUTION"}}],"links":{}}`)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", http.StatusOK, versionLocs("Ride"))
	k.ASC.Handle(http.MethodGet, "/v1/appInfos/{id}/appInfoLocalizations", http.StatusOK, infoLocs)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appStoreVersions", http.StatusOK, `{"data":[]}`)
	return k
}

// MF-2: the tool's own export of a live-only app plans cleanly, and a
// promo-text-only edit plans against the live version.
func TestPlanLiveOnlyExportRoundTrip(t *testing.T) {
	k := liveOnlyKit(t)
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_pull"), k.Env, `{"app":"Hjul","export_dir":"fastlane/metadata"}`))

	same := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, `{"app":"Hjul","fastlane_dir":"fastlane/metadata"}`)))
	if same["status"] != "no_changes" || same["errors"] != nil {
		t.Fatalf("unchanged export: %v", same)
	}

	if err := k.Files.WriteFile("fastlane/metadata/en-US/promotional_text.txt", []byte("Summer sale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	promo := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, `{"app":"Hjul","version":"live","fastlane_dir":"fastlane/metadata"}`)))
	if promo["status"] != statusPlanned || promo["plan_id"] == nil {
		t.Fatalf("promo edit: %v", promo)
	}
	if ch := mustJSON(t, promo["changes"]); !strings.Contains(ch, "promotionalText") || strings.Count(ch, `"field"`) != 1 {
		t.Errorf("changes = %s", ch)
	}
	if w := stringify(promo["warnings"]); strings.Contains(w, "field_locked_on_live") {
		t.Errorf("unchanged fields reported as locked: %v", promo["warnings"])
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Error("plan mutated")
	}
}

// MF-2: a real app-info change on a live-only app is still refused.
func TestPlanLiveOnlyAppInfoChangeRefused(t *testing.T) {
	k := liveOnlyKit(t)
	out := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env,
		`{"app":"Hjul","listing":{"locales":{"en-US":{"name":"Hjul","subtitle":"Ride further"}}}}`)))
	errs := stringify(out["errors"])
	if out["status"] != statusInvalid || !strings.Contains(errs, "level_unavailable") || !strings.Contains(errs, "subtitle") {
		t.Errorf("out = %v", out)
	}
	if strings.Count(mustJSON(t, out["errors"]), `"code"`) != 1 {
		t.Errorf("unchanged name reported: %v", out["errors"])
	}
}
