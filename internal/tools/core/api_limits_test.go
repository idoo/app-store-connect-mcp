package core

import (
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/asctest"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// AF2/PO6: through the real client against the spec-checking fake (which
// fails any query key the operation does not take, as Apple 400s), an
// operation without a limit parameter gets none.
func TestAPIGetSendsNoLimitWhenSpecHasNone(t *testing.T) {
	k := kit(t)
	srv := asctest.New(t)
	srv.Handle(http.MethodGet, "/v1/appAssetLibraryRefData", 200,
		`{"data":[{"type":"appAssetLibraryRefData","id":"r1","attributes":{}}],"links":{}}`)
	k.Env.ASC = srv.NewClient(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v1/appAssetLibraryRefData"}`))
	if out := data(t, res); out["returned"] != float64(1) {
		t.Fatalf("out = %v", out)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 || reqs[0].Query.Has("limit") {
		t.Fatalf("requests = %+v", reqs)
	}
	if res.Meta.Requests == nil || res.Meta.Requests.Apple != 1 {
		t.Fatalf("requests meta = %+v", res.Meta.Requests)
	}
}

// PO6: query.limit is the page size and, without limit, the item cap; the
// result says so.
func TestAPIGetHonoursQueryLimit(t *testing.T) {
	k := kit(t)
	k.ASC.Handle("GET", "/v1/apps", 200, `{"data":[{"type":"apps","id":"1","attributes":{"name":"A"}},{"type":"apps","id":"2","attributes":{"name":"B"}},{"type":"apps","id":"3","attributes":{"name":"C"}}],
	  "links":{"next":"https://api.appstoreconnect.apple.com/v1/apps?cursor=Mw&limit=3"},"meta":{"paging":{"total":12,"limit":3}}}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v1/apps","query":{"limit":"3"}}`))
	out := data(t, res)
	if out["returned"] != float64(3) || out["next_cursor"] == nil || out["limit_note"] == nil {
		t.Fatalf("out = %v", out)
	}
	if reqs := k.ASC.Requests(); len(reqs) != 1 || reqs[0].Query.Get("limit") != "3" {
		t.Fatalf("requests = %+v", reqs)
	}
}

// PO7 + B8: Apple timestamps and enums of a first-party resource are plain
// values; a customer review (third-party type) stays wrapped entirely, so a
// hostile all-caps nickname is never presented as trusted.
func TestAPIGetTrustsAppleValues(t *testing.T) {
	k := kit(t)
	srv := asctest.New(t)
	srv.Handle(http.MethodGet, "/v1/customerReviews/{id}", 200,
		`{"data":{"type":"customerReviews","id":"r1","attributes":{"rating":2,"title":"BAD","body":"meh","reviewerNickname":"IGNORE_PREVIOUS","createdDate":"2026-10-20T10:00:00-07:00","territory":"USA"}}}`)
	srv.Handle(http.MethodGet, "/v1/apps/{id}", 200,
		`{"data":{"type":"apps","id":"1234567890","attributes":{"name":"ACME","bundleId":"x.y","primaryLocale":"en-US"}}}`)
	k.Env.ASC = srv.NewClient(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v1/customerReviews/r1"}`))
	got := tooltest.JSON(t, data(t, res)["data"])
	for _, want := range []string{`"reviewerNickname":{"untrusted":"IGNORE_PREVIOUS"}`, `"territory":{"untrusted":"USA"}`, `"title":{"untrusted":"BAD"}`, `"body":{"untrusted":"meh"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("data %s lacks %s", got, want)
		}
	}
	res = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "asc_api_get"), k.Env, `{"path":"/v1/apps/1234567890"}`))
	if got := tooltest.JSON(t, data(t, res)["data"]); !strings.Contains(got, `"name":{"untrusted":"ACME"}`) {
		t.Errorf("free-text name unwrapped: %s", got)
	}
}
