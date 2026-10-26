package core

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/asctest"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

type coreArtifacts struct{ got map[string][]byte }

func (a *coreArtifacts) Put(_ context.Context, name, mimeType string, b []byte) (tool.Link, error) {
	a.got[name] = b
	return tool.Link{URI: "app-store-connect-mcp://artifacts/" + name, Name: name, MIMEType: mimeType, Size: int64(len(b))}, nil
}

// bigRefData is one appAssetLibraryRefData resource larger than the result cap.
func bigRefData() string {
	specs := make([]string, 2000)
	for i := range specs {
		specs[i] = `{"id":"spec-` + strings.Repeat("x", 20) + `","width":1290,"height":2796}`
	}
	return `{"data":[{"type":"appAssetLibraryRefData","id":"r1","attributes":{"specs":[` + strings.Join(specs, ",") +
		`],"features":[{"a":1},{"b":2}],"version":"1"}}],"links":{},"meta":{"paging":{"total":1}}}`
}

// B1: a single resource over the cap is summarised and stored as an
// artifact, never returned as data:[] with total:1.
func TestAPIGetOversizedSingleResource(t *testing.T) {
	k := kit(t)
	srv := asctest.New(t)
	srv.Handle(http.MethodGet, "/v1/appAssetLibraryRefData", 200, bigRefData())
	k.Env.ASC = srv.NewClient(t)
	art := &coreArtifacts{got: map[string][]byte{}}
	k.Env.Artifacts = art
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v1/appAssetLibraryRefData"}`))
	out := data(t, res)
	if _, ok := out["data"]; ok {
		t.Fatalf("data still inline: %v", out["data"] != nil)
	}
	sum, _ := out["data_summary"].(map[string]any)
	attrs, _ := sum["attributes"].(map[string]any)
	if sum["id"] != "r1" || tooltest.JSON(t, attrs["specs"]) != `{"items":2000}` || tooltest.JSON(t, attrs["features"]) != `{"items":2}` || attrs["version"] != "string" {
		t.Fatalf("summary = %s", tooltest.JSON(t, sum))
	}
	if len(res.Links) != 1 || out["data_resource"] != res.Links[0].URI {
		t.Fatalf("links = %+v, data_resource = %v", res.Links, out["data_resource"])
	}
	full := art.got["appAssetLibraryRefData-r1.json"]
	var back map[string]any
	if err := json.Unmarshal(full, &back); err != nil || len(back["specs"].([]any)) != 2000 {
		t.Fatalf("artifact = %d bytes, err %v", len(full), err)
	}
	if out["returned"] != float64(1) {
		t.Fatalf("returned = %v", out["returned"])
	}
	if b := tooltest.JSON(t, out); len(b) > singleInlineMax {
		t.Fatalf("result still %d bytes", len(b))
	}
}

// B1: without an artifact store the summary still comes back with a fields hint.
func TestAPIGetOversizedSingleResourceNoArtifacts(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/appAssetLibraryRefData", 200, bigRefData())
	k.Env.Artifacts = nil
	out := data(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v1/appAssetLibraryRefData"}`)))
	if out["data_summary"] == nil || !strings.Contains(out["data_hint"].(string), "fields[appAssetLibraryRefData]") {
		t.Fatalf("out = %v", out)
	}
}

// B4/B5: query.limit above Apple's maximum and a negative limit are
// invalid_args before any request.
func TestAPIGetRejectsBadLimits(t *testing.T) {
	for _, args := range []string{
		`{"path":"/v1/apps","query":{"limit":"1000"}}`,
		`{"path":"/v1/apps","query":{"limit":"201"}}`,
		`{"path":"/v1/apps","limit":-5}`,
		`{"path":"/v1/apps","limit":5001}`,
	} {
		k := kit(t)
		res := tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, args)
		tooltest.MustFail(t, res, "invalid_arguments")
		if n := len(k.ASC.Requests()); n != 0 {
			t.Errorf("%s: %d Apple requests", args, n)
		}
	}
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps", 200, `{"data":[],"links":{}}`)
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v1/apps","query":{"limit":"200"}}`))
}
