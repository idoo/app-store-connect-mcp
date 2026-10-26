package screenshots

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// failSetCreate answers POST /v1/appScreenshotSets for the localization ids
// in fail with their fixture; others create the set as usual.
func failSetCreate(t *testing.T, k *tooltest.Kit, f *fake, fail map[string]tooltest.Fixture) {
	k.ASC.HandleFunc(http.MethodPost, "/v1/appScreenshotSets", func(req tool.Request) tooltest.Fixture {
		f.mu.Lock()
		defer f.mu.Unlock()
		d := body(req)
		loc := d["relationships"].(map[string]any)["appStoreVersionLocalization"].(map[string]any)["data"].(map[string]any)["id"].(string)
		if fx, ok := fail[loc]; ok {
			return fx
		}
		s := &fakeSet{id: f.id("set"), display: d["attributes"].(map[string]any)["screenshotDisplayType"].(string)}
		f.sets[loc] = append(f.sets[loc], s)
		return tooltest.Fixture{Status: http.StatusCreated, Body: doc(t, s.res())}
	})
}

var setConflict = tooltest.Fixture{
	Status: http.StatusConflict,
	Body:   []byte(`{"errors":[{"status":"409","code":"STATE_ERROR","title":"wrong state","detail":"version is locked"}]}`),
}

func twoNewSets(t *testing.T, k *tooltest.Kit) {
	write(t, k, "shots/en-US/01.png", pngImage(t, 1290, 2796, false, 1))
	write(t, k, "shots/de-DE/01.png", pngImage(t, 1290, 2796, false, 2))
}

// withPlan runs screenshots_plan with args and returns args plus its plan_id.
func withPlan(t *testing.T, k *tooltest.Kit, args string) string {
	t.Helper()
	p := tooltest.Decode[planResult](t, tooltest.MustOK(t, call(t, k, "screenshots_plan", args)))
	if p.PlanID == "" {
		t.Fatalf("plan %s has no plan_id: %+v", args, p)
	}
	return strings.TrimSuffix(args, "}") + fmt.Sprintf(`,"plan_id":%q}`, p.PlanID)
}

// CD-6: an additive sync without plan_id is refused before any request.
func TestSyncWithoutPlanIDRefused(t *testing.T) {
	k, _ := newKit(t)
	twoNewSets(t, k)
	for _, args := range []string{`{"dir":"shots","mode":"add"}`, `{"dir":"shots","mode":"replace"}`, `{"dir":"shots","plan_id":""}`} {
		b := tooltest.MustFail(t, call(t, k, "screenshots_sync", args), "invalid_arguments")
		if !strings.Contains(b.Message, "run screenshots_plan first") || len(b.Next) != 1 || b.Next[0].Tool != "screenshots_plan" {
			t.Errorf("%s: body = %+v", args, b)
		}
	}
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("mutations = %v", mutations(k))
	}
	if len(k.Fetch.Calls) != 0 {
		t.Fatalf("uploads = %v", k.Fetch.Calls)
	}
}

func TestSyncAllFailIsOutcomeUnknownFirst(t *testing.T) {
	k, f := newKit(t)
	twoNewSets(t, k)
	failSetCreate(t, k, f, map[string]tooltest.Fixture{
		"loc-en": {Err: fmt.Errorf("%w: %w", tool.ErrOutcomeUnknown, &tool.APIError{Status: http.StatusInternalServerError})},
		"loc-de": setConflict,
	})
	res := call(t, k, "screenshots_sync", withPlan(t, k, `{"dir":"shots","mode":"add"}`))
	if !res.IsError {
		t.Fatalf("want error, got %s", tooltest.JSON(t, res.Data))
	}
	if b := tooltest.Decode[tool.ErrorBody](t, res); b.Error != "outcome_unknown" {
		t.Errorf("error = %q, want outcome_unknown", b.Error)
	}
	if len(res.Next) == 0 || res.Next[0].Tool != "screenshots_plan" {
		t.Errorf("next = %+v, want screenshots_plan", res.Next)
	}
	if n := len(k.ASC.Mutations()); n != 2 {
		t.Errorf("mutations = %v", mutations(k))
	}
}

func TestSyncMixedIsPartialWithItemCode(t *testing.T) {
	k, f := newKit(t)
	twoNewSets(t, k)
	failSetCreate(t, k, f, map[string]tooltest.Fixture{"loc-de": setConflict})
	res := tooltest.MustOK(t, call(t, k, "screenshots_sync", withPlan(t, k, `{"dir":"shots","mode":"add"}`)))
	out := tooltest.Decode[syncOut](t, res)
	// The failed set creation plus its not-attempted upload.
	if out.Status != "partial" || out.Uploaded != 1 || out.Failed != 2 {
		t.Fatalf("out = %+v", out)
	}
	for _, it := range out.Items {
		if it.Action == actionFailed && (it.Code == "" || it.Code == "error") {
			t.Errorf("failed item code = %q", it.Code)
		}
	}
	if len(res.Next) == 0 || res.Next[0].Tool != "screenshots_plan" {
		t.Errorf("next = %+v", res.Next)
	}
}

func TestSyncIsDestructive(t *testing.T) {
	tl, _ := tool.Lookup("screenshots_sync")
	if a := tl.Meta().Annotations(); a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Errorf("screenshots_sync annotations = %+v", a)
	}
}
