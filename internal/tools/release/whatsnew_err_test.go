package release

import (
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// A failed What's New locale keeps its API error class: the result carries
// the 409's code and hint, not a flattened string.
func TestReleasePrepareWhatsNewKeepsErrorType(t *testing.T) {
	k := setup(t, readyFixture())
	_, sres := call(t, k, `{"app":"Hjul","desired":{"build":"latest","whats_new":{"*":"New sails."},"release_type":"MANUAL","phased":true,"uses_non_exempt_encryption":false}}`)
	if len(sres.Next) == 0 || sres.Next[0].Tool != "release_prepare" {
		t.Fatalf("status next = %+v", sres.Next)
	}
	writable(k)
	k.ASC.Handle("PATCH", "/v1/appStoreVersions/{id}/relationships/build", 204, "")
	k.ASC.Handle("GET", "/v1/builds/{id}", 200, doc(t, jres{Type: "builds", ID: "b2", Attrs: map[string]any{"version": "43", "processingState": "VALID", "usesNonExemptEncryption": nil}}))
	k.ASC.Handle("PATCH", "/v1/builds/{id}", 200, doc(t, jres{Type: "builds", ID: "b2", Attrs: map[string]any{"usesNonExemptEncryption": false}}))
	k.ASC.Handle("PATCH", "/v1/appStoreVersions/{id}", 200, doc(t, jres{Type: "appStoreVersions", ID: "v1", Attrs: map[string]any{"releaseType": "MANUAL"}}))
	k.ASC.Handle("POST", "/v1/appStoreVersionPhasedReleases", 201, doc(t, jres{Type: "appStoreVersionPhasedReleases", ID: "pr1", Attrs: map[string]any{"phasedReleaseState": "INACTIVE"}}))
	const hint = "fetch the version state; it may be with App Review"
	k.ASC.HandleFunc("PATCH", "/v1/appStoreVersionLocalizations/{id}", func(r tool.Request) tooltest.Fixture {
		id := strings.Split(r.Path, "/")[3]
		if id == "loc0" {
			return tooltest.Fixture{Err: &tool.APIError{Status: 409, Code: "STATE_ERROR", Detail: "not editable", Hint: hint}}
		}
		return tooltest.Fixture{Status: 200, Body: []byte(doc(t, jres{Type: "appStoreVersionLocalizations", ID: id, Attrs: map[string]any{"whatsNew": "New sails."}}))}
	})

	res := tooltest.Call(t, mustTool(t, "release_prepare"), k.Env, argsJSON(t, sres.Next[0].Args))
	body := tooltest.MustFail(t, res, "api_error")
	if body.Code != "STATE_ERROR" {
		t.Errorf("code = %q, want STATE_ERROR", body.Code)
	}
	if body.Hint != hint {
		t.Errorf("hint = %q, want %q", body.Hint, hint)
	}
	if !strings.Contains(body.Message, "whats_new: 1 of 2 locales failed") {
		t.Errorf("message = %q", body.Message)
	}
}
