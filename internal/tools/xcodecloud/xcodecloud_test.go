package xcodecloud

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idoo/hamn/internal/spec"
	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

var (
	specOnce sync.Once
	specIdx  *spec.Index
	specErr  error
)

// kit returns a kit with the real spec index and app Hjul; every request is
// checked against the spec on cleanup.
func kit(t *testing.T) *tooltest.Kit {
	t.Helper()
	specOnce.Do(func() { specIdx, specErr = spec.Load() })
	if specErr != nil {
		t.Fatal(specErr)
	}
	k := tooltest.New(t)
	k.Env.Spec = specIdx
	k.Resolve.Apps = []tool.App{{ID: "A1", BundleID: "se.jvcr.hjul", Name: "Hjul"}}
	t.Cleanup(func() {
		for _, r := range k.ASC.Requests() {
			if _, ok := specIdx.Match(r.Method, r.Path); !ok {
				t.Errorf("%s %s is not a spec operation", r.Method, r.Path)
				continue
			}
			if err := specIdx.Validate(r); err != nil {
				t.Errorf("%s %s?%s violates the spec: %v", r.Method, r.Path, r.Query.Encode(), err)
			}
		}
	})
	return k
}

func lookup(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tl
}

func wantLog(t *testing.T, k *tooltest.Kit, want ...string) {
	t.Helper()
	reqs := k.ASC.Requests()
	got := make([]string, len(reqs))
	for i, r := range reqs {
		got[i] = r.Method + " " + r.Path
	}
	if g, w := strings.Join(got, "\n"), strings.Join(want, "\n"); g != w {
		t.Fatalf("requests:\n%s\nwant:\n%s", g, w)
	}
}

const (
	wfID    = "11111111-2222-3333-4444-555555555555"
	runID   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	repoID  = "R1"
	prodDoc = `{"data":[{"type":"ciProducts","id":"PR1","attributes":{"name":"Hjul","productType":"APP"},
	 "relationships":{"app":{"data":{"type":"apps","id":"A1"}}}}]}`
	workflowsDoc = `{"data":[
	 {"type":"ciWorkflows","id":"` + wfID + `","attributes":{"name":"Release","isEnabled":true,"clean":true,
	  "branchStartCondition":{"source":{"isAllMatch":false,"patterns":[{"pattern":"main","isPrefix":false},{"pattern":"release/","isPrefix":true}]}},
	  "pullRequestStartCondition":{"source":{"isAllMatch":true},"destination":{"patterns":[{"pattern":"main"}]}},
	  "manualBranchStartCondition":{"source":{"isAllMatch":true}},
	  "actions":[{"name":"Archive - iOS","actionType":"ARCHIVE"},{"name":"Test","actionType":"TEST"}]},
	  "relationships":{"repository":{"data":{"type":"scmRepositories","id":"R1"}}}},
	 {"type":"ciWorkflows","id":"99999999-2222-3333-4444-555555555555","attributes":{"name":"Nightly","isEnabled":false,
	  "scheduledStartCondition":{"source":{"isAllMatch":true},"schedule":{"frequency":"DAILY"}}},
	  "relationships":{"repository":{"data":{"type":"scmRepositories","id":"R1"}}}}],
	 "included":[{"type":"scmRepositories","id":"R1","attributes":{"ownerName":"idoo","repositoryName":"hjul"}}]}`
)

func TestWorkflowsList(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, prodDoc)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/{id}/workflows", http.StatusOK, workflowsDoc)
	res := tooltest.MustOK(t, tooltest.Call(t, lookup(t, "xcc_workflows_list"), k.Env, `{"app":"Hjul"}`))
	wantLog(t, k, "GET /v1/ciProducts", "GET /v1/ciProducts/PR1/workflows")
	if f := k.ASC.Requests()[0].Query.Get("filter[app]"); f != "A1" {
		t.Fatalf("filter[app] = %q", f)
	}
	if inc := k.ASC.Requests()[1].Query.Get("include"); inc != "repository" {
		t.Fatalf("include = %q", inc)
	}
	out := tooltest.Decode[workflowsListOut](t, res)
	if out.AppID != "A1" || len(out.Products) != 1 || len(out.Products[0].Workflows) != 2 {
		t.Fatalf("out = %+v", out)
	}
	w := out.Products[0].Workflows[0]
	if w.Name != "Release" || !w.Enabled || w.Repository == nil || w.Repository.Name != "idoo/hjul" || w.ProductID != "PR1" {
		t.Fatalf("workflow = %+v", w)
	}
	if got := strings.Join(w.StartConditions, " | "); got != "branch: main, release/* | pull_request: any → main | manual branch: any" {
		t.Fatalf("conditions = %s", got)
	}
	if got := strings.Join(w.Actions, ","); got != "ARCHIVE Archive - iOS,TEST Test" {
		t.Fatalf("actions = %s", got)
	}
	if n := out.Products[0].Workflows[1]; n.Enabled || n.StartConditions[0] != "scheduled: any (DAILY)" {
		t.Fatalf("nightly = %+v", n)
	}
}

func TestBuildStartByBranchName(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, prodDoc)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/{id}/workflows", http.StatusOK, workflowsDoc)
	k.ASC.Handle(http.MethodGet, "/v1/scmRepositories/{id}/gitReferences", http.StatusOK, `{"data":[
	 {"type":"scmGitReferences","id":"G0","attributes":{"name":"main","canonicalName":"refs/tags/main","kind":"TAG","isDeleted":false}},
	 {"type":"scmGitReferences","id":"G9","attributes":{"name":"main","canonicalName":"refs/heads/main","kind":"BRANCH","isDeleted":true}},
	 {"type":"scmGitReferences","id":"G1","attributes":{"name":"main","canonicalName":"refs/heads/main","kind":"BRANCH","isDeleted":false}}]}`)
	k.ASC.Handle(http.MethodPost, "/v1/ciBuildRuns", http.StatusCreated,
		`{"data":{"type":"ciBuildRuns","id":"`+runID+`","attributes":{"number":42,"executionProgress":"PENDING"}}}`)
	res := tooltest.MustOK(t, tooltest.Call(t, lookup(t, "xcc_build_start"), k.Env, `{"workflow":"release","branch":"main","clean":false}`))
	wantLog(t, k, "GET /v1/ciProducts", "GET /v1/ciProducts/PR1/workflows", "GET /v1/scmRepositories/R1/gitReferences", "POST /v1/ciBuildRuns")
	b, err := json.Marshal(k.ASC.Requests()[3].Body)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"data":{"attributes":{"clean":false},"relationships":{"sourceBranchOrTag":{"data":{"id":"G1","type":"scmGitReferences"}},` +
		`"workflow":{"data":{"id":"` + wfID + `","type":"ciWorkflows"}}},"type":"ciBuildRuns"}}`
	if string(b) != want {
		t.Fatalf("body = %s", b)
	}
	out := tooltest.Decode[buildStartOut](t, res)
	if out.Run.ID != runID || out.Run.Number != 42 || out.Source == nil || out.Source.ID != "G1" || out.Workflow.Name != "Release" {
		t.Fatalf("out = %+v", out)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "xcc_build_status" {
		t.Fatalf("next = %+v", res.Next)
	}
}

func TestBuildStartRefusals(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, prodDoc)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/{id}/workflows", http.StatusOK, workflowsDoc)
	k.ASC.Handle(http.MethodGet, "/v1/scmRepositories/{id}/gitReferences", http.StatusOK, `{"data":[]}`)
	tl := lookup(t, "xcc_build_start")
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"workflow":"Release","branch":"a","tag":"b"}`), tool.ErrInvalidArgs.Code)
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"workflow":"Nightly"}`), tool.ErrInvalidArgs.Code)
	res := tooltest.Call(t, tl, k.Env, `{"workflow":"Release","tag":"v9"}`)
	if !res.IsError {
		t.Fatalf("unknown tag accepted: %s", tooltest.JSON(t, res.Data))
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("no build may start")
	}
}

func runDoc(progress, status string) string {
	return `{"data":{"type":"ciBuildRuns","id":"` + runID + `","attributes":{"number":42,"executionProgress":"` + progress +
		`","completionStatus":` + status + `,"issueCounts":{"errors":0,"warnings":3,"analyzerWarnings":0,"testFailures":1},
		"sourceCommit":{"commitSha":"abc123","message":"Fix crash"}},
		"relationships":{"workflow":{"data":{"type":"ciWorkflows","id":"` + wfID + `"}}}}}`
}

func TestBuildStatusWaitCompletes(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}", http.StatusOK, runDoc("RUNNING", "null"))
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}", http.StatusOK, runDoc("COMPLETE", `"FAILED"`))
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}/actions", http.StatusOK, `{"data":[
	 {"type":"ciBuildActions","id":"AC1","attributes":{"name":"Test","actionType":"TEST","executionProgress":"COMPLETE","completionStatus":"FAILED","isRequiredToPass":true,
	  "issueCounts":{"errors":0,"warnings":0,"analyzerWarnings":0,"testFailures":1}}},
	 {"type":"ciBuildActions","id":"AC2","attributes":{"name":"Archive","actionType":"ARCHIVE","executionProgress":"COMPLETE","completionStatus":"SUCCEEDED"}}]}`)
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildActions/{id}/artifacts", http.StatusOK, `{"data":[
	 {"type":"ciArtifacts","id":"F1","attributes":{"fileName":"Hjul.xcarchive.zip","fileType":"ARCHIVE","fileSize":1234,"downloadUrl":"https://download.example.apple.com/a?sig=SECRET"}}]}`)
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildActions/{id}/testResults", http.StatusOK, `{"data":[
	 {"type":"ciTestResults","id":"T1","attributes":{"className":"HjulTests","name":"testSpin()","status":"SUCCESS"}},
	 {"type":"ciTestResults","id":"T2","attributes":{"className":"HjulTests","name":"testCrash()","status":"FAILURE","message":"XCTAssertEqual failed"}}]}`)
	var progress []string
	k.Env.Progress = func(_, _ float64, msg string) { progress = append(progress, msg) }
	start := k.Clock.Now()
	res := tooltest.MustOK(t, tooltest.Call(t, lookup(t, "xcc_build_status"), k.Env, `{"run":"`+runID+`","wait":true}`))
	wantLog(t, k,
		"GET /v1/ciBuildRuns/"+runID, "GET /v1/ciBuildRuns/"+runID, "GET /v1/ciBuildRuns/"+runID+"/actions",
		"GET /v1/ciBuildActions/AC1/artifacts", "GET /v1/ciBuildActions/AC1/testResults", "GET /v1/ciBuildActions/AC2/artifacts")
	if d := k.Clock.Now().Sub(start); d != 15*time.Second || len(progress) != 1 || progress[0] != "build run #42: RUNNING" {
		t.Fatalf("slept %v, progress %v", d, progress)
	}
	out := tooltest.Decode[map[string]any](t, res)
	if out["status"] == "still_processing" {
		t.Fatalf("out = %v", out)
	}
	j := tooltest.JSON(t, res.Data)
	for _, want := range []string{
		`"completion_status":"FAILED"`, `"host":"download.example.apple.com"`, `"commit_message":{"untrusted":"Fix crash"}`,
		`"by_status":{"FAILURE":1,"SUCCESS":1}`, `"message":{"untrusted":"XCTAssertEqual failed"}`, `"test_failures":1`,
	} {
		if !strings.Contains(j, want) {
			t.Fatalf("missing %s in %s", want, j)
		}
	}
	if strings.Contains(j, "SECRET") {
		t.Fatalf("signed URL leaked: %s", j)
	}
}

func TestBuildStatusLatestTimesOut(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciWorkflows/{id}", http.StatusOK,
		`{"data":{"type":"ciWorkflows","id":"`+wfID+`","attributes":{"name":"Release","isEnabled":true}}}`)
	k.ASC.Handle(http.MethodGet, "/v1/ciWorkflows/{id}/buildRuns", http.StatusOK,
		`{"data":[{"type":"ciBuildRuns","id":"`+runID+`","attributes":{"number":42,"executionProgress":"RUNNING"}}]}`)
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}", http.StatusOK, runDoc("RUNNING", "null"))
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}/actions", http.StatusOK, `{"data":[
	 {"type":"ciBuildActions","id":"AC1","attributes":{"name":"Test","actionType":"TEST","executionProgress":"RUNNING"}}]}`)
	start := k.Clock.Now()
	res := tooltest.MustOK(t, tooltest.Call(t, lookup(t, "xcc_build_status"), k.Env,
		`{"workflow":"`+wfID+`","wait":true,"wait_seconds":40,"include_urls":true}`))
	if q := k.ASC.RequestsTo(http.MethodGet, "/v1/ciWorkflows/{id}/buildRuns")[0].Query; q.Get("sort") != "-number" || q.Get("limit") != "1" {
		t.Fatalf("latest query = %s", q.Encode())
	}
	if d := k.Clock.Now().Sub(start); d != 40*time.Second {
		t.Fatalf("waited %v", d)
	}
	if polls := len(k.ASC.RequestsTo(http.MethodGet, "/v1/ciBuildRuns/{id}")); polls != 4 {
		t.Fatalf("polls = %d", polls)
	}
	out := tooltest.Decode[map[string]any](t, res)
	if out["status"] != "still_processing" || !strings.Contains(out["resume_hint"].(string), runID) {
		t.Fatalf("out = %v", out)
	}
	tooltest.MustFail(t, tooltest.Call(t, lookup(t, "xcc_build_status"), k.Env, `{"wait_seconds":301,"run":"x"}`), tool.ErrInvalidArgs.Code)
	tooltest.MustFail(t, tooltest.Call(t, lookup(t, "xcc_build_status"), k.Env, `{}`), tool.ErrInvalidArgs.Code)
}

func TestBuildStatusNoWaitSingleRead(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}", http.StatusOK, runDoc("RUNNING", "null"))
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}/actions", http.StatusOK, `{"data":[]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, lookup(t, "xcc_build_status"), k.Env, `{"run":"`+runID+`"}`))
	wantLog(t, k, "GET /v1/ciBuildRuns/"+runID, "GET /v1/ciBuildRuns/"+runID+"/actions")
	if out := tooltest.Decode[buildStatusOut](t, res); out.Run.ExecutionProgress != "RUNNING" || out.Run.Number != 42 {
		t.Fatalf("out = %+v", out)
	}
}
