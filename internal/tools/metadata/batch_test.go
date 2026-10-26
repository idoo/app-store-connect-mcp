package metadata

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const stateError409 = `{"errors":[{"status":"409","code":"STATE_ERROR","title":"wrong state","detail":"localization is locked"}]}`

// applyTwoPatches plans keyword+subtitle edits (one version and one app
// info PATCH) and applies them, returning the apply result.
func applyTwoPatches(t *testing.T, k *tooltest.Kit) *tool.Result {
	t.Helper()
	args := `{"app":"Hjul","listing":{"en-US":{"keywords":"bike,ride,cycle","subtitle":"Ride further"}}}`
	plan := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_plan"), k.Env, args)))
	id, _ := plan["plan_id"].(string)
	if id == "" {
		t.Fatalf("no plan: %v", plan)
	}
	return tooltest.Call(t, mustTool(t, "metadata_apply"), k.Env, strings.TrimSuffix(args, "}")+`,"plan_id":"`+id+`"}`)
}

func unknown500() tooltest.Fixture {
	return tooltest.Fixture{Err: fmt.Errorf("%w: %w", tool.ErrOutcomeUnknown, &tool.APIError{Status: http.StatusInternalServerError})}
}

func hasNext(next []tool.NextStep, name string) bool {
	return slices.ContainsFunc(next, func(n tool.NextStep) bool { return n.Tool == name })
}

func TestApplyAllFailIsOutcomeUnknownError(t *testing.T) {
	k := kit(t)
	listing(k)
	k.ASC.HandleFixture(http.MethodPatch, "/v1/appStoreVersionLocalizations/{id}", unknown500())
	k.ASC.HandleFixture(http.MethodPatch, "/v1/appInfoLocalizations/{id}", unknown500())
	body := tooltest.MustFail(t, applyTwoPatches(t, k), "outcome_unknown")
	if !hasNext(body.Next, "metadata_plan") {
		t.Errorf("next = %+v, want metadata_plan", body.Next)
	}
	if n := len(k.ASC.Mutations()); n != 2 {
		t.Errorf("mutations = %d, want 2", n)
	}
}

func TestApplyMixedIsPartialWithItemCode(t *testing.T) {
	k := kit(t)
	listing(k)
	k.ASC.Handle(http.MethodPatch, "/v1/appStoreVersionLocalizations/{id}", http.StatusOK,
		`{"data":{"type":"appStoreVersionLocalizations","id":"vl-en","attributes":{"locale":"en-US"}}}`)
	k.ASC.Handle(http.MethodPatch, "/v1/appInfoLocalizations/{id}", http.StatusConflict, stateError409)
	res := tooltest.MustOK(t, applyTwoPatches(t, k))
	out := decode(t, res)
	if out["status"] != "partial" || out["applied"] != 1.0 || out["failed"] != 1.0 {
		t.Fatalf("apply = %v", out)
	}
	var code any
	for _, it := range out["items"].([]any) {
		if m := it.(map[string]any); m["outcome"] == "error" {
			code = m["code"]
		}
	}
	if s, _ := code.(string); s == "" || s == "error" {
		t.Errorf("failed item code = %v", code)
	}
	if !hasNext(res.Next, "metadata_plan") {
		t.Errorf("next = %+v, want metadata_plan", res.Next)
	}
	if n := len(k.ASC.Mutations()); n != 2 {
		t.Errorf("mutations = %d, want 2", n)
	}
}

// appinfoKit sets up a copyright PATCH (version) and a content-rights PATCH
// (app), confirmed in one call.
func appinfoKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreVersions/{id}", http.StatusOK,
		`{"data":{"type":"appStoreVersions","id":"v2","attributes":{"copyright":"2025 Old"}}}`)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}", http.StatusOK,
		`{"data":{"type":"apps","id":"app1","attributes":{"contentRightsDeclaration":"USES_THIRD_PARTY_CONTENT"}}}`)
	return k
}

const appinfoArgs = `{"app":"Hjul","copyright":"2026 New","content_rights":"DOES_NOT_USE_THIRD_PARTY_CONTENT"}`

// confirmAppinfo previews appinfoArgs, then confirms with an approving human.
func confirmAppinfo(t *testing.T, k *tooltest.Kit) *tool.Result {
	t.Helper()
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, appinfoArgs))
	k.Elicit(true)
	return tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, strings.TrimSuffix(appinfoArgs, "}")+`,"confirm":true}`)
}

func TestAppinfoAllFailIsTypedError(t *testing.T) {
	k := appinfoKit(t)
	k.ASC.HandleFixture(http.MethodPatch, "/v1/appStoreVersions/{id}", unknown500())
	k.ASC.Handle(http.MethodPatch, "/v1/apps/{id}", http.StatusConflict, stateError409)
	body := tooltest.MustFail(t, confirmAppinfo(t, k), "outcome_unknown")
	if !hasNext(body.Next, "appinfo_update") {
		t.Errorf("next = %+v, want appinfo_update", body.Next)
	}
	for _, n := range body.Next {
		if n.Tool == "appinfo_update" && n.Args["confirm"] != nil {
			t.Errorf("replan args keep confirm: %v", n.Args)
		}
	}
	if n := len(k.ASC.Mutations()); n != 2 {
		t.Errorf("mutations = %d, want 2", n)
	}
}

func TestAppinfoMixedIsPartialWithItemCode(t *testing.T) {
	k := appinfoKit(t)
	k.ASC.Handle(http.MethodPatch, "/v1/appStoreVersions/{id}", http.StatusOK,
		`{"data":{"type":"appStoreVersions","id":"v2","attributes":{"copyright":"2026 New"}}}`)
	k.ASC.Handle(http.MethodPatch, "/v1/apps/{id}", http.StatusConflict, stateError409)
	out := decode(t, tooltest.MustOK(t, confirmAppinfo(t, k)))
	if out["status"] != "partial" {
		t.Fatalf("out = %v", out)
	}
	codes := 0
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		if m["outcome"] == "error" {
			if c, _ := m["code"].(string); c == "" || c == "error" {
				t.Errorf("failed item code = %v", m["code"])
			}
			codes++
		}
	}
	if codes != 1 {
		t.Errorf("failed items = %d, want 1", codes)
	}
}

func TestPullExportKeepsLocalEdits(t *testing.T) {
	k := kit(t)
	listing(k)
	edited := filepath.Join("metadata", "en-US", "description.txt")
	same := filepath.Join("metadata", "en-US", "keywords.txt")
	for p, body := range map[string]string{edited: "local edit", same: "bike,ride\n"} {
		if err := k.Files.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := decode(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "metadata_pull"), k.Env,
		`{"app":"Hjul","export_dir":"metadata"}`)))
	b, err := os.ReadFile(filepath.Join(k.Files.Root, edited))
	if err != nil || string(b) != "local edit" {
		t.Errorf("edited file = %q, %v", b, err)
	}
	conflicts := strs(out["conflicts"])
	written := strs(out["written"])
	if !slices.Equal(conflicts, []string{edited}) {
		t.Errorf("conflicts = %v", conflicts)
	}
	if slices.Contains(written, same) || slices.Contains(written, edited) {
		t.Errorf("written = %v contains an existing file", written)
	}
	if _, err := os.Stat(filepath.Join(k.Files.Root, "metadata", "de-DE", "description.txt")); err != nil {
		t.Errorf("missing file not written: %v", err)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Error("pull mutated")
	}
}

func TestApplyIsDestructive(t *testing.T) {
	a := mustTool(t, "metadata_apply").Meta().Annotations()
	if a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Errorf("metadata_apply annotations = %+v", a)
	}
}

func strs(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}
