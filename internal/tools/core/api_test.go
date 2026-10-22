package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func TestAPISearchRanking(t *testing.T) {
	k := kit(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_search"), k.Env, `{"query":"list apps","limit":5}`))
	out := tooltest.Decode[struct {
		Operations []map[string]any `json:"operations"`
	}](t, res)
	if len(out.Operations) == 0 || out.Operations[0]["operation_id"] != "apps_getCollection" {
		t.Fatalf("top hit = %+v, want operation_id apps_getCollection", out.Operations)
	}
	if _, ok := out.Operations[0]["id"]; ok {
		t.Errorf("search hit still carries id: %v", out.Operations[0])
	}
	describe := tooltest.Call(t, mustTool(t, "asc_api_describe"), k.Env,
		`{"operation_id":"`+out.Operations[0]["operation_id"].(string)+`"}`)
	tooltest.MustOK(t, describe)
	if len(out.Operations) > 5 {
		t.Errorf("limit ignored: %d hits", len(out.Operations))
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "asc_api_describe" {
		t.Errorf("next = %+v", res.Next)
	}
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "asc_api_search"), k.Env, `{"query":"  "}`), "invalid_arguments")
}

func TestAPISearchReadOnlyHidesWrites(t *testing.T) {
	k := kit(t).ReadOnly()
	tl := mustTool(t, "asc_api_search")
	methods := func(args string) []string {
		out := tooltest.Decode[struct {
			Operations []struct{ Method string } `json:"operations"`
		}](t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args)))
		var ms []string
		for _, h := range out.Operations {
			ms = append(ms, h.Method)
		}
		return ms
	}
	if ms := methods(`{"query":"delete beta tester"}`); len(ms) == 0 || slices.ContainsFunc(ms, func(m string) bool { return m != "GET" }) {
		t.Errorf("read-only methods = %v, want only GET", ms)
	}
	if ms := methods(`{"query":"delete beta tester","include_writes":true}`); !slices.Contains(ms, "DELETE") {
		t.Errorf("include_writes methods = %v, want a DELETE", ms)
	}
}

func TestAPIDescribe(t *testing.T) {
	k := kit(t)
	tl := mustTool(t, "asc_api_describe")
	byID := tooltest.Decode[tool.OpDescription](t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"operation_id":"appStoreVersions_createInstance"}`)))
	if byID.BodySkeleton == nil || byID.Method != "POST" {
		t.Errorf("describe = %+v", byID)
	}
	byPath := tooltest.Decode[tool.OpDescription](t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"method":"get","path":"/v1/apps/123"}`)))
	if byPath.ID != "apps_getInstance" {
		t.Errorf("method+path resolved to %q", byPath.ID)
	}
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"operation_id":"nope_nothing"}`), "invalid_arguments")
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"method":"GET"}`), "invalid_arguments")
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"method":"GET","path":"/v1/notAThing"}`), "invalid_arguments")
}

func TestAPIGetValidationFailureSkipsApple(t *testing.T) {
	k := kit(t)
	res := tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v1/apps","query":{"filter[bogus]":"x"}}`)
	if !res.IsError {
		t.Fatalf("want error, got %+v", res.Data)
	}
	body := data(t, res)
	if probs, _ := body["problems"].([]any); len(probs) == 0 || body["skeleton"] == nil {
		t.Errorf("body = %v, want problems and skeleton", body)
	}
	if len(k.ASC.Requests()) != 0 {
		t.Error("Apple was called for an invalid request")
	}
}

func TestAPIGetCollectionPaginates(t *testing.T) {
	page1 := `{"data":[{"type":"apps","id":"1","attributes":{"name":"A"}}],
	  "links":{"next":"https://api.appstoreconnect.apple.com/v1/apps?cursor=Mg&limit=1"},"meta":{"paging":{"total":2,"limit":1}}}`
	page2 := `{"data":[{"type":"apps","id":"2","attributes":{"name":"B"}}],"links":{},"meta":{"paging":{"total":2,"limit":1}}}`
	t.Run("all pages", func(t *testing.T) {
		k := kit(t)
		k.ASC.Handle("GET", "/v1/apps", 200, page1).Handle("GET", "/v1/apps", 200, page2)
		res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env,
			`{"path":"/v1/apps?limit=1","limit":5,"fields":{"apps":"name"}}`))
		out := data(t, res)
		if out["operation"] != "apps_getCollection" || out["returned"] != float64(2) || out["next_cursor"] != nil {
			t.Errorf("out = %v", out)
		}
		if q := k.ASC.Requests()[0].Query; q.Get("fields[apps]") != "name" || q.Get("limit") != "1" {
			t.Errorf("first query = %v", q)
		}
	})
	t.Run("limit cursor", func(t *testing.T) {
		k := kit(t)
		k.ASC.Handle("GET", "/v1/apps", 200, page1)
		res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v1/apps","query":{"limit":"1"},"limit":1}`))
		if out := data(t, res); out["returned"] != float64(1) || out["next_cursor"] == nil || out["total"] != float64(2) {
			t.Errorf("out = %v", out)
		}
	})
}

func TestAPIGetSingleAndUnlisted(t *testing.T) {
	k := kit(t)
	k.ASC.SkipSpecCheck = true // exercises unsafe_unlisted
	k.ASC.Handle("GET", "/v1/apps/{id}", 200, `{"data":{"type":"apps","id":"7","attributes":{"name":"Hjul"}}}`)
	out := data(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v1/apps/7"}`)))
	if d, _ := out["data"].(map[string]any); d["id"] != "7" || out["operation"] != "apps_getInstance" {
		t.Errorf("out = %v", out)
	}

	res := tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v9/brandNewThings"}`)
	if !res.IsError || !strings.Contains(data(t, res)["hint"].(string), "unsafe_unlisted") {
		t.Errorf("unlisted = %+v", res.Data)
	}
	if n := len(k.ASC.Requests()); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	k.ASC.Handle("GET", "/v9/brandNewThings", 200, `{"data":[{"type":"brandNewThings","id":"x"}],"links":{}}`)
	out = data(t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v9/brandNewThings","unsafe_unlisted":true}`)))
	if out["operation"] != "unlisted" || out["data"] == nil {
		t.Errorf("unsafe out = %v", out)
	}
}

func TestAPIRequestRejectsGet(t *testing.T) {
	k := kit(t)
	body := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "asc_api_request"), k.Env, `{"method":"GET","path":"/v1/apps"}`), "invalid_arguments")
	if len(body.Next) != 1 || body.Next[0].Tool != "asc_api_get" {
		t.Errorf("next = %+v", body.Next)
	}
	if len(k.ASC.Requests()) != 0 {
		t.Error("Apple called")
	}
}

func TestAPIRequestSensitiveNeedsConfirm(t *testing.T) {
	tests := []struct{ name, args, method, pattern string }{
		{"delete", `{"method":"DELETE","path":"/v1/betaTesters/t1"}`, "DELETE", "/v1/betaTesters/{id}"},
		{"expire build", `{"method":"PATCH","path":"/v1/builds/b1","body":{"data":{"type":"builds","id":"b1","attributes":{"expired":true}}}}`, "PATCH", "/v1/builds/{id}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := kit(t)
			asked := k.Elicit(true)
			k.ASC.Handle(tt.method, tt.pattern, 204, "")
			tl := mustTool(t, "asc_api_request")
			preview := data(t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, tt.args)))
			if preview["status"] != "needs_confirm" || len(k.ASC.Mutations()) != 0 {
				t.Fatalf("preview = %v, mutations = %d", preview, len(k.ASC.Mutations()))
			}
			confirmed := strings.Replace(tt.args, "{", `{"confirm":true,`, 1)
			out := data(t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, confirmed)))
			if len(k.ASC.Mutations()) != 1 || out["status"] != float64(204) || len(*asked) != 1 {
				t.Errorf("out = %v mutations = %d asked = %d", out, len(k.ASC.Mutations()), len(*asked))
			}
		})
	}
}

// TestAPIRequestAlwaysNeedsHuman locks RedTeam#5: every mutation, sensitive
// or not, is refused after confirm:true until a human approves; the
// "sensitive" classification only changes preview text.
func TestAPIRequestAlwaysNeedsHuman(t *testing.T) {
	tests := []struct{ name, args, method, pattern string }{
		{"delete", `{"method":"DELETE","path":"/v1/betaTesters/t1"}`, "DELETE", "/v1/betaTesters/{id}"},
		{"expire build", `{"method":"PATCH","path":"/v1/builds/b1","body":{"data":{"type":"builds","id":"b1","attributes":{"expired":true}}}}`, "PATCH", "/v1/builds/{id}"},
		{"plain patch", `{"method":"PATCH","path":"/v1/appStoreVersionLocalizations/L1","body":{"data":{"type":"appStoreVersionLocalizations","id":"L1","attributes":{"whatsNew":"Fixes"}}}}`, "PATCH", "/v1/appStoreVersionLocalizations/{id}"},
		{"untracked public attr", `{"method":"PATCH","path":"/v1/apps/7","body":{"data":{"type":"apps","id":"7","attributes":{"contentRightsDeclaration":"USES_THIRD_PARTY_CONTENT"}}}}`, "PATCH", "/v1/apps/{id}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := kit(t)
			k.ASC.Handle(tt.method, tt.pattern, 204, "")
			confirmed := strings.Replace(tt.args, "{", `{"confirm":true,`, 1)
			tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "asc_api_request"), k.Env, confirmed), "human_approval_required")
			if n := len(k.ASC.Mutations()); n != 0 {
				t.Fatalf("%d mutations without a human", n)
			}
		})
	}
}

func TestAPIPathRejectsEscapesAndDotSegments(t *testing.T) {
	k := kit(t)
	k.Elicit(true)
	for _, p := range []string{
		"/v1/betaTesters/%2e%2e%2fusers%2fX",
		"/v1/betaTesters/%2E%2E",
		"/v1/apps/7%2Fbuilds",
		"/v1/apps/./7",
		"/v1/apps/../users",
	} {
		get := tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"`+p+`"}`)
		tooltest.MustFail(t, get, "invalid_arguments")
		del := tooltest.Call(t, mustTool(t, "asc_api_request"), k.Env, `{"method":"DELETE","confirm":true,"path":"`+p+`"}`)
		tooltest.MustFail(t, del, "invalid_arguments")
	}
	if n := len(k.ASC.Requests()); n != 0 {
		t.Fatalf("%d requests reached Apple", n)
	}
}

func TestAPIRequestPlainWriteNeedsHuman(t *testing.T) {
	k := kit(t)
	k.ASC.Handle("PATCH", "/v1/appStoreVersionLocalizations/{id}", 200,
		`{"data":{"type":"appStoreVersionLocalizations","id":"L1","attributes":{"whatsNew":"Fixes"}}}`)
	tl := mustTool(t, "asc_api_request")
	args := `{"method":"PATCH","path":"/v1/appStoreVersionLocalizations/L1","body":{"data":{"type":"appStoreVersionLocalizations","id":"L1","attributes":{"whatsNew":"Fixes"}}}}`
	preview := data(t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args)))
	if preview["status"] != "needs_confirm" || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("plain write without confirm = %v", preview)
	}
	confirmed := strings.Replace(args, "{", `{"confirm":true,`, 1)
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, confirmed), "human_approval_required")
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("mutated without a human")
	}
	k.Elicit(true)
	out := data(t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, confirmed)))
	if out["operation"] != "appStoreVersionLocalizations_updateInstance" || len(k.ASC.Mutations()) != 1 {
		t.Errorf("out = %v", out)
	}
	if m := k.ASC.Mutations()[0]; !m.Write || m.Body == nil {
		t.Errorf("mutation = %+v", m)
	}
}

func TestAPIGetResolvesAppRef(t *testing.T) {
	k := kit(t)
	k.Resolve.Apps = []tool.App{{ID: "7", BundleID: "se.jvcr.hjul", Name: "Hjul"}}
	k.ASC.Handle("GET", "/v1/apps/{id}", 200, `{"data":{"type":"apps","id":"7","attributes":{"name":"Hjul"}}}`)
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v1/apps/{app}"}`))
	reqs := k.ASC.Requests()
	if len(reqs) != 1 || strings.Contains(reqs[0].Path, "{app}") {
		t.Errorf("requests = %+v", reqs)
	}
}

func TestAPIRequestInvalidBodyNotSent(t *testing.T) {
	k := kit(t)
	res := tooltest.Call(t, mustTool(t, "asc_api_request"), k.Env,
		`{"method":"POST","path":"/v1/appStoreVersionLocalizations","body":{"data":{"type":"appStoreVersionLocalizations","attributes":{}}}}`)
	if !res.IsError || data(t, res)["skeleton"] == nil {
		t.Errorf("res = %+v", res.Data)
	}
	if len(k.ASC.Requests()) != 0 {
		t.Error("Apple called with an invalid body")
	}
}

func TestAPIRequestUnlisted(t *testing.T) {
	k := kit(t)
	k.ASC.SkipSpecCheck = true // exercises unsafe_unlisted
	tl := mustTool(t, "asc_api_request")
	res := tooltest.Call(t, tl, k.Env, `{"method":"POST","path":"/v1/brandNewThings","body":{"data":{"type":"brandNewThings"}}}`)
	if !res.IsError {
		t.Fatalf("unlisted op accepted: %+v", res.Data)
	}
	k.Elicit(true)
	k.ASC.Handle("POST", "/v1/brandNewThings", 201, `{"data":{"type":"brandNewThings","id":"n1"}}`)
	args := `{"method":"POST","path":"/v1/brandNewThings","unsafe_unlisted":true,"body":{"data":{"type":"brandNewThings"}}}`
	preview := data(t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, args)))
	if preview["status"] != "needs_confirm" || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("unlisted without confirm = %v", preview)
	}
	out := data(t, tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, strings.Replace(args, "{", `{"confirm":true,`, 1))))
	if out["operation"] != "unlisted" || len(k.ASC.Mutations()) != 1 {
		t.Errorf("out = %v", out)
	}
}

func TestAPIRequestWithheldReadOnly(t *testing.T) {
	k := kit(t).ReadOnly()
	tl := mustTool(t, "asc_api_request")
	if tl.ListedIn(k.Env.Profile) {
		t.Error("asc_api_request listed in read-only mode")
	}
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"method":"DELETE","path":"/v1/betaTesters/t1","confirm":true}`), "writes_disabled")
	if !mustTool(t, "asc_api_get").ListedIn(k.Env.Profile) {
		t.Error("asc_api_get withheld in read-only mode")
	}
}
