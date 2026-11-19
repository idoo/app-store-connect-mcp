package xcodecloud

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const (
	udTeam        = "0a1b2c3d-aaaa-bbbb-cccc-333344445555"
	udProduct     = "7e6d5c4b-aaaa-bbbb-cccc-333344445555"
	udProductsDoc = `{"data":[{"type":"ciProducts","id":"` + udProduct + `","attributes":{"name":"Hjul"}}]}`
	// One run that worked the day before (an action of ten minutes), then a
	// newer one that Xcode Cloud never started.
	udRunsDoc = `{"data":[
	 {"type":"ciBuildRuns","id":"run-dead","attributes":{"number":8,"createdDate":"2026-01-02T01:00:00.000Z",
	  "executionProgress":"COMPLETE","completionStatus":"FAILED"}},
	 {"type":"ciBuildRuns","id":"run-ok","attributes":{"number":7,"createdDate":"2026-01-01T10:00:00.000Z",
	  "startedDate":"2026-01-01T10:00:05.000Z","finishedDate":"2026-01-01T10:11:00.000Z",
	  "executionProgress":"COMPLETE","completionStatus":"SUCCEEDED"}}]}`
	udActionsDoc = `{"data":[{"type":"ciBuildActions","id":"act-1","attributes":{
	 "startedDate":"2026-01-01T10:00:10.000Z","finishedDate":"2026-01-01T10:10:10.000Z"}}]}`
)

func udHasNext(res *tool.Result, name string) bool {
	for _, n := range res.Next {
		if n.Tool == name {
			return true
		}
	}
	return false
}

// Without a web session the default source is the estimate, and only then
// is signing in suggested; asking for Apple's numbers never gets an estimate.
func TestUsageWithoutWebSession(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, udProductsDoc)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/{id}/buildRuns", http.StatusOK, udRunsDoc)
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}/actions", http.StatusOK, udActionsDoc)
	tl := lookup(t, "xcc_usage_report")

	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{}`))
	out := res.Data.(usageOut)
	if out.Source != usageRuns || out.Plan != nil || out.Minutes != 10 || out.Builds != 1 || !out.NotTakingRuns || out.DeadRunsTotal != 1 {
		t.Fatalf("out = %+v", out)
	}
	if !udHasNext(res, "web_login") {
		t.Fatalf("next = %+v", res.Next)
	}

	res = tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"source":"runs"}`))
	if udHasNext(res, "web_login") {
		t.Fatalf("the estimate was asked for, next = %+v", res.Next)
	}

	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"source":"apple"}`), tool.ErrWebSessionRequired.Code)
}

// With a web session the numbers are Apple's, no run is read, and a plan
// with nothing left is warned about.
func TestUsageWithWebSession(t *testing.T) {
	k := kit(t).WebSession()
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, udProductsDoc)
	k.Portal.HandleJSON(http.MethodGet, "/olympus/v1/session", `{"provider":{"providerId":4242424,"publicProviderId":"`+udTeam+`"}}`)
	base := "/ci/api/teams/" + udTeam + "/usage"
	plan := func(available int) {
		k.Portal.HandleJSON(http.MethodGet, base+"/summary", fmt.Sprintf(
			`{"plan":{"name":"Starter","available":%d,"used":%d,"total":1500,"reset_date":"2026-01-14","reset_date_time":"2026-01-14T00:00:00Z"}}`,
			available, 1500-available))
	}
	days := `{"usage":[{"date":"2026-01-02","minutes":40,"number_of_builds":3}],
	 "product_usage":[{"product_id":"` + strings.ToUpper(udProduct) + `","usage_in_minutes":40,"usage_in_seconds":2400,"number_of_builds":3}]}`
	for _, q := range []string{"?start=2026-01-02&end=2026-01-02", "?end=2026-01-02&start=2026-01-02"} {
		k.Portal.HandleJSON(http.MethodGet, base+"/days"+q, days)
	}
	tl := lookup(t, "xcc_usage_report")

	plan(0)
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"days":1}`))
	out := res.Data.(usageOut)
	if out.Source != usageApple || out.Plan == nil || !out.Plan.Exhausted || out.Plan.TotalMinutes != 1500 || out.Minutes != 40 ||
		len(out.Products) != 1 || out.Products[0].Name != "Hjul" {
		t.Fatalf("out = %+v plan = %+v", out, out.Plan)
	}
	if len(res.Meta.Warnings) != 1 || udHasNext(res, "web_login") {
		t.Fatalf("warnings %v next %+v", res.Meta.Warnings, res.Next)
	}

	plan(600)
	res = tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"days":1}`))
	if out := res.Data.(usageOut); out.Plan.Exhausted || out.Plan.AvailableMinutes != 600 || len(res.Meta.Warnings) != 0 {
		t.Fatalf("plan = %+v warnings %v", out.Plan, res.Meta.Warnings)
	}

	for _, r := range k.ASC.Requests() {
		if strings.Contains(r.Path, "buildRuns") {
			t.Fatalf("runs read with a web session: %s", r.Path)
		}
	}
}

func TestUsageArgs(t *testing.T) {
	k := kit(t)
	tl := lookup(t, "xcc_usage_report")
	for _, args := range []string{
		`{"app":"Hjul","product":"` + udProduct + `"}`,
		`{"days":32}`,
		`{"source":"guess"}`,
	} {
		tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, args), tool.ErrInvalidArgs.Code)
	}
}

// A run that Xcode Cloud never started points at the usage; a run that
// started and failed is its own failure.
func TestBuildStatusRunThatNeverStarted(t *testing.T) {
	tl := lookup(t, "xcc_build_status")
	dead := runDoc("COMPLETE", `"FAILED"`)
	started := strings.Replace(dead, `"number":42`, `"number":42,"startedDate":"2026-01-01T10:00:00Z"`, 1)
	for _, c := range []struct {
		doc  string
		want bool
	}{{dead, true}, {started, false}} {
		k := kit(t)
		k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}", http.StatusOK, c.doc)
		k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}/actions", http.StatusOK, `{"data":[]}`)
		res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"run":"`+runID+`"}`))
		if got := udHasNext(res, "xcc_usage_report"); got != c.want {
			t.Fatalf("xcc_usage_report suggested = %v, want %v (next %+v)", got, c.want, res.Next)
		}
	}
}
