package metadata

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// lesomaKit is an iOS+macOS app whose app info is locked by a macOS version
// in review while the iOS version is being prepared.
func lesomaKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := kit(t)
	app := testApp
	app.Platforms = []tool.Platform{tool.IOS, tool.MacOS}
	k.Resolve.Apps = []tool.App{app}
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appInfos", http.StatusOK, `{"data":[
 {"type":"appInfos","id":"info1","attributes":{"state":"READY_FOR_DISTRIBUTION"}},
 {"type":"appInfos","id":"info3","attributes":{"state":"WAITING_FOR_REVIEW"}}],"links":{}}`)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/appStoreVersions", http.StatusOK, `{"data":[
 {"type":"appStoreVersions","id":"v2","attributes":{"platform":"IOS","versionString":"1.1","appVersionState":"PREPARE_FOR_SUBMISSION"}},
 {"type":"appStoreVersions","id":"m36","attributes":{"platform":"MAC_OS","versionString":"3.6","appVersionState":"WAITING_FOR_REVIEW"}}],"links":{}}`)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", http.StatusOK, versionLocs("Ride"))
	k.ASC.Handle(http.MethodGet, "/v1/appInfos/{id}/appInfoLocalizations", http.StatusOK, infoLocs)
	return k
}

func TestPlanLockedAppInfoRoundTripIsPlanned(t *testing.T) {
	k := lesomaKit(t)
	args := `{"listing":{"locales":{"en-US":{"name":"Hjul","subtitle":"Ride more","whatsNew":"Faster sync"},"de-DE":{"name":"Hjul","subtitle":"Mehr fahren"}}}}`
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, args))
	out := decode(t, res)
	if out["status"] != statusPlanned || out["plan_id"] == nil {
		t.Fatalf("out = %v", out)
	}
	changes := out["changes"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["level"] != "version" {
		t.Errorf("changes = %v, want one version update", changes)
	}
	if w := strings.Join(res.Meta.Warnings, "\n"); !strings.Contains(w, "MAC_OS also has 3.6 (WAITING_FOR_REVIEW)") {
		t.Errorf("warnings = %q, want the other-platform note", w)
	}
	if len(res.Next) != 1 || res.Next[0].Args["listing"] != nil {
		t.Errorf("next = %+v, want no echoed listing", res.Next)
	}
}

func TestPlanLockedAppInfoChangedFieldNamesBlocker(t *testing.T) {
	k := lesomaKit(t)
	out := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env,
		`{"listing":{"locales":{"en-US":{"subtitle":"Ride further"}}}}`)))
	errs := stringify(out["errors"])
	if out["status"] != statusInvalid || !strings.Contains(errs, "WAITING_FOR_REVIEW because MAC_OS 3.6 is in review; subtitle can change") {
		t.Fatalf("out = %v", out)
	}
}

func TestPlanLockedAppInfoRejectsNewLocales(t *testing.T) {
	k := lesomaKit(t)
	locales := map[string]any{}
	for _, code := range applemeta.Locales() {
		if code == "en-US" || code == "de-DE" || len(locales) == 37 {
			continue
		}
		locales[code] = map[string]any{"description": "Text", "keywords": "a,b"}
	}
	b, err := json.Marshal(map[string]any{"listing": map[string]any{"locales": locales}})
	if err != nil {
		t.Fatal(err)
	}
	out := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, string(b))))
	counts := out["counts"].(map[string]any)
	if out["status"] != statusInvalid || counts["creates"] != 0.0 || len(out["errors"].([]any)) != 37 {
		t.Fatalf("status = %v counts = %v errors = %d", out["status"], counts, len(out["errors"].([]any)))
	}
	if !strings.Contains(stringify(out["errors"]), "while app info is WAITING_FOR_REVIEW; wait for MAC_OS 3.6 review") {
		t.Errorf("errors = %v", out["errors"])
	}
}

func TestPlanThirtySevenLocalesTextStaysSmall(t *testing.T) {
	k := kit(t)
	listing(k)
	locales := map[string]any{}
	for _, code := range applemeta.Locales() {
		if len(locales) == 37 {
			break
		}
		locales[code] = map[string]any{"name": "Hjul", "description": strings.Repeat("Ride your bike every day. ", 150), "keywords": "bike,ride"}
	}
	b, err := json.Marshal(map[string]any{"listing": map[string]any{"locales": locales}})
	if err != nil {
		t.Fatal(err)
	}
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, string(b)))
	if res.Data.(planOut).Status != statusPlanned {
		t.Fatalf("status = %v", res.Data.(planOut).Status)
	}
	text, err := json.Marshal(struct {
		Data any             `json:"data"`
		Next []tool.NextStep `json:"next"`
	}{res.Data, res.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(text) >= 70<<10 {
		t.Errorf("plan text = %d bytes, want < 70 KB", len(text))
	}
	if slices.ContainsFunc(res.Next, func(n tool.NextStep) bool { return n.Args["listing"] != nil }) {
		t.Error("next echoes the inline listing")
	}
}

func TestPullWarnsAboutOtherPlatform(t *testing.T) {
	k := lesomaKit(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_pull"), k.Env, `{}`))
	if w := strings.Join(res.Meta.Warnings, "\n"); !strings.Contains(w, "MAC_OS also has 3.6") {
		t.Errorf("warnings = %q", w)
	}
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/apps/{id}/appStoreVersions")); n != 1 {
		t.Errorf("version lookups = %d, want 1", n)
	}
}
