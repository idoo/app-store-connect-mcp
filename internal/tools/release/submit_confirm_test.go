package release

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// A first-time IAP that turns ready between preview and confirm would be
// auto-attached; the plan never listed it, so the confirm is stale.
func TestReleaseSubmitPlanStaleWhenProductTurnsReady(t *testing.T) {
	f := readyFixture()
	f.iapState = "MISSING_METADATA"
	k := writable(setup(t, f))
	submitHandlers(t, k)
	iapVersionRoute(t, k)
	var ready atomic.Bool
	k.ASC.HandleFunc("GET", "/v1/apps/{id}/inAppPurchasesV2", func(tool.Request) tooltest.Fixture {
		state := "MISSING_METADATA"
		if ready.Load() {
			state = "READY_TO_SUBMIT"
		}
		return tooltest.Fixture{Status: 200, Body: []byte(doc(t, []jres{{Type: "inAppPurchases", ID: "iap1", Attrs: map[string]any{"name": "Pro", "productId": "pro", "state": state}}}))}
	})
	tl := mustTool(t, "release_submit")
	prev := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul"}`)))
	id, _ := prev["plan_id"].(string)
	if id == "" {
		t.Fatalf("preview = %+v", prev)
	}
	ready.Store(true)
	k.Elicit(true)
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","plan_id":"`+id+`"}`), tool.ErrStalePlan.Code)
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("stale confirm sent %d mutations", n)
	}
}

// partialSubmit decodes the error data of a release_submit that stopped
// part-way.
type partialSubmit struct {
	tool.ErrorBody
	Partial submitOut `json:"partial"`
}

func TestReleaseSubmitPartialFailureReportsApplied(t *testing.T) {
	t.Run("item_rejected", func(t *testing.T) {
		k := writable(setup(t, readyFixture()))
		submitHandlers(t, k)
		iapVersionRoute(t, k)
		var n atomic.Int32
		k.ASC.HandleFunc("POST", "/v1/reviewSubmissionItems", func(tool.Request) tooltest.Fixture {
			if n.Add(1) == 1 {
				return tooltest.Fixture{Status: 201, Body: []byte(doc(t, jres{Type: "reviewSubmissionItems", ID: "new-item", Attrs: map[string]any{"state": "READY_FOR_REVIEW"}}))}
			}
			return tooltest.Fixture{Status: 409, Body: []byte(`{"errors":[{"status":"409","code":"STATE_ERROR","title":"cannot add"}]}`)}
		})
		res := submitConfirm(t, k)
		p := tooltest.Decode[partialSubmit](t, res)
		if !res.IsError || p.Partial.Submission.ID != "rs9" || !p.Partial.Created || p.Partial.FailedStep == nil || p.Partial.FailedStep.Outcome != "error" {
			t.Fatalf("partial = %s", tooltest.JSON(t, p))
		}
		if len(p.Partial.Items) != 2 || p.Partial.Items[0].Outcome != "done" || p.Partial.Items[1].Outcome != "error" {
			t.Fatalf("items = %s", tooltest.JSON(t, p.Partial.Items))
		}
	})
	t.Run("submit_5xx_outcome_unknown", func(t *testing.T) {
		k := writable(setup(t, readyFixture()))
		submitHandlers(t, k)
		iapVersionRoute(t, k)
		k.ASC.HandleFunc("PATCH", "/v1/reviewSubmissions/{id}", func(tool.Request) tooltest.Fixture {
			// The real client reports a 5xx on a mutation as outcome_unknown.
			return tooltest.Fixture{Err: fmt.Errorf("%w: %w", tool.ErrOutcomeUnknown, &tool.APIError{Status: http.StatusInternalServerError})}
		})
		res := submitConfirm(t, k)
		p := tooltest.Decode[partialSubmit](t, res)
		if p.Error != tool.ErrOutcomeUnknown.Code || p.Partial.FailedStep == nil || p.Partial.FailedStep.Outcome != "outcome_unknown" || p.Partial.Submission.ID != "rs9" {
			t.Fatalf("partial = %s", tooltest.JSON(t, p))
		}
		if n := len(k.ASC.RequestsTo("PATCH", "/v1/reviewSubmissions/{id}")); n != 1 {
			t.Fatalf("submit PATCH sent %d times, want 1 (no replay)", n)
		}
	})
}

// submitConfirm previews release_submit and confirms its plan, returning the
// confirm result whatever its status.
func submitConfirm(t *testing.T, k *tooltest.Kit) *tool.Result {
	t.Helper()
	tl := mustTool(t, "release_submit")
	prev := tooltest.Decode[map[string]any](t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul"}`)))
	id, _ := prev["plan_id"].(string)
	b, _ := json.Marshal(map[string]any{"app": "Hjul", "plan_id": id})
	k.Elicit(true)
	return tooltest.Call(t, tl, k.Env, string(b))
}

// Without a human in the loop (allow_no_elicit), confirm acts only on the
// previewed submission state: one that entered review since is refused.
func TestReleaseCancelConfirmRefusesChangedState(t *testing.T) {
	k := writable(setup(t, readyFixture()))
	k.Env.Profile.AllowNoElicit = true
	var state atomic.Value
	state.Store("WAITING_FOR_REVIEW")
	k.ASC.HandleFunc("GET", "/v1/reviewSubmissions?filter[app]=100", func(tool.Request) tooltest.Fixture {
		return tooltest.Fixture{Status: 200, Body: []byte(doc(t, []jres{{Type: "reviewSubmissions", ID: "rs1", Attrs: map[string]any{"platform": "IOS", "state": state.Load().(string)}}}))}
	})
	k.ASC.Handle("GET", "/v1/reviewSubmissions/{id}/items", 200, `{"data":[]}`)
	k.ASC.Handle("PATCH", "/v1/reviewSubmissions/{id}", 200, doc(t, jres{Type: "reviewSubmissions", ID: "rs1", Attrs: map[string]any{"state": "CANCELING"}}))
	tl := mustTool(t, "release_cancel")
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul"}`))
	state.Store("IN_REVIEW")
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","confirm":true}`), tool.ErrPreviewRequired.Code)
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("changed-state confirm sent %d mutations", n)
	}
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul"}`))
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","confirm":true}`))
	if n := len(k.ASC.Mutations()); n != 1 {
		t.Fatalf("mutations = %d, want 1", n)
	}
}
