package xcodecloud

import (
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// archiveRunDoc is the run reread for its workflow actions and product.
const archiveRunDoc = `{"data":{"type":"ciBuildRuns","id":"` + runID + `","attributes":{"number":68},
 "relationships":{"workflow":{"data":{"type":"ciWorkflows","id":"` + wfID + `"}},"product":{"data":{"type":"ciProducts","id":"PR1"}}}},
 "included":[{"type":"ciWorkflows","id":"` + wfID + `","attributes":{"actions":[
  {"name":"Archive - macOS","actionType":"ARCHIVE","platform":"MACOS"},
  {"name":"Archive - iOS","actionType":"ARCHIVE","platform":"IOS"}]}},
  {"type":"ciProducts","id":"PR1","attributes":{"name":"Hjul"}}]}`

const uploadsDoc = `{"data":[
 {"type":"buildUploads","id":"U-mac","attributes":{"cfBundleShortVersionString":"4.0","cfBundleVersion":"68","platform":"MAC_OS",
  "createdDate":"2026-11-02T23:29:56-08:00","uploadedDate":"2026-11-02T23:34:17-08:00","state":{"state":"COMPLETE","errors":[],"warnings":[],"infos":[]}}},
 {"type":"buildUploads","id":"U-ios","attributes":{"cfBundleShortVersionString":"4.0","cfBundleVersion":"68","platform":"IOS",
  "createdDate":"2026-11-02T23:28:47-08:00","uploadedDate":"2026-11-02T23:32:38-08:00","state":{"state":"FAILED","errors":[
   {"code":"90626","description":"Invalid Siri Support. App Intent description cannot contain \"apple\""},
   {"code":90208,"description":"Invalid Bundle."}],"warnings":[{"code":"W1","description":"Missing icon."}],"infos":[]}}},
 {"type":"buildUploads","id":"U-tv","attributes":{"cfBundleVersion":"68","platform":"TV_OS","createdDate":"2026-11-01T00:00:00Z","state":{"state":"COMPLETE"}}}]}`

func archiveKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}", http.StatusOK, `{"data":{"type":"ciBuildRuns","id":"`+runID+`",
	 "attributes":{"number":68,"executionProgress":"COMPLETE","completionStatus":"FAILED"}}}`)
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}", http.StatusOK, archiveRunDoc)
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}/actions", http.StatusOK, `{"data":[
	 {"type":"ciBuildActions","id":"AI","attributes":{"name":"Archive - iOS","actionType":"ARCHIVE","executionProgress":"COMPLETE","completionStatus":"FAILED"}},
	 {"type":"ciBuildActions","id":"AM","attributes":{"name":"Archive - macOS","actionType":"ARCHIVE","executionProgress":"COMPLETE","completionStatus":"SUCCEEDED"}}]}`)
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildActions/{id}/artifacts", http.StatusOK, `{"data":[]}`)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/{id}/app", http.StatusOK, `{"data":{"type":"apps","id":"A1"}}`)
	return k
}

func TestBuildStatusReportsUploadErrors(t *testing.T) {
	k := archiveKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/buildUploads", http.StatusOK, uploadsDoc)
	res := tooltest.MustOK(t, tooltest.Call(t, lookup(t, "xcc_build_status"), k.Env, `{"run":"`+runID+`"}`))
	wantLog(t, k, "GET /v1/ciBuildRuns/"+runID, "GET /v1/ciBuildRuns/"+runID+"/actions",
		"GET /v1/ciBuildActions/AI/artifacts", "GET /v1/ciBuildActions/AM/artifacts",
		"GET /v1/ciBuildRuns/"+runID, "GET /v1/ciProducts/PR1/app", "GET /v1/apps/A1/buildUploads")
	reread := k.ASC.RequestsTo(http.MethodGet, "/v1/ciBuildRuns/{id}")[1].Query
	if reread.Get("include") != "product,workflow" || reread.Get("fields[ciWorkflows]") != "actions" {
		t.Fatalf("run reread query = %v", reread)
	}
	if q := k.ASC.RequestsTo(http.MethodGet, "/v1/apps/{id}/buildUploads")[0].Query; q.Get("filter[cfBundleVersion]") != "68" {
		t.Fatalf("uploads query = %v", q)
	}
	out := res.Data.(buildStatusOut)
	ios, mac := out.Actions[0], out.Actions[1]
	if len(ios.Uploads) != 1 || ios.Uploads[0].ID != "U-ios" || ios.Uploads[0].State != "FAILED" || ios.Uploads[0].Platform != tool.IOS {
		t.Fatalf("iOS uploads = %+v", ios.Uploads)
	}
	if e := ios.Uploads[0].Errors; len(e) != 2 || e[0].Code != "90626" || e[1].Code != "90208" || len(ios.Uploads[0].Warnings) != 1 {
		t.Fatalf("iOS errors = %+v", ios.Uploads[0])
	}
	if len(mac.Uploads) != 1 || mac.Uploads[0].ID != "U-mac" || mac.Uploads[0].State != "COMPLETE" {
		t.Fatalf("macOS uploads = %+v", mac.Uploads)
	}
	if len(out.OtherUploads) != 1 || out.OtherUploads[0].ID != "U-tv" {
		t.Fatalf("other uploads = %+v", out.OtherUploads)
	}
	if len(res.Meta.Warnings) != 1 || !strings.Contains(res.Meta.Warnings[0], "IOS upload of build 68 (90626, 90208)") {
		t.Fatalf("warnings = %v", res.Meta.Warnings)
	}
	j := tooltest.JSON(t, res.Data)
	for _, want := range []string{`"description":{"untrusted":"Invalid Siri Support.`, `"code":"90208"`} {
		if !strings.Contains(j, want) {
			t.Fatalf("missing %s in %s", want, j)
		}
	}
	if strings.Contains(res.Meta.Warnings[0], "Siri") {
		t.Fatal("untrusted description copied into a warning")
	}
}

// A failing upload read is a warning; the run status still returns.
func TestBuildStatusUploadReadFailureWarns(t *testing.T) {
	k := archiveKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/{id}/buildUploads", http.StatusForbidden,
		`{"errors":[{"status":"403","code":"FORBIDDEN_ERROR","title":"no"}]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, lookup(t, "xcc_build_status"), k.Env, `{"run":"`+runID+`"}`))
	if len(res.Meta.Warnings) != 1 || !strings.Contains(res.Meta.Warnings[0], "could not read the App Store Connect uploads of build 68") {
		t.Fatalf("warnings = %v", res.Meta.Warnings)
	}
	if out := res.Data.(buildStatusOut); out.Actions[0].CompletionStatus != "FAILED" {
		t.Fatalf("out = %+v", out)
	}
}

// Runs without a completed archive read no uploads.
func TestBuildStatusNoArchiveNoUploadCalls(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}", http.StatusOK, runDoc("COMPLETE", `"SUCCEEDED"`))
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildRuns/{id}/actions", http.StatusOK, `{"data":[
	 {"type":"ciBuildActions","id":"AB","attributes":{"name":"Build","actionType":"BUILD","executionProgress":"COMPLETE","completionStatus":"SUCCEEDED"}},
	 {"type":"ciBuildActions","id":"AR","attributes":{"name":"Archive","actionType":"ARCHIVE","executionProgress":"RUNNING"}}]}`)
	k.ASC.Handle(http.MethodGet, "/v1/ciBuildActions/{id}/artifacts", http.StatusOK, `{"data":[]}`)
	tooltest.MustOK(t, tooltest.Call(t, lookup(t, "xcc_build_status"), k.Env, `{"run":"`+runID+`"}`))
	wantLog(t, k, "GET /v1/ciBuildRuns/"+runID, "GET /v1/ciBuildRuns/"+runID+"/actions", "GET /v1/ciBuildActions/AB/artifacts")
}
