package xcodecloud

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const (
	bnTeam    = "0a1b2c3d-0000-1111-2222-333344445555"
	bnProduct = "9f8e7d6c-0000-1111-2222-333344445555"
	bnPath    = "/ci/api/teams/" + bnTeam + "/products/" + bnProduct + "/next-build-number"
	bnProdDoc = `{"data":[{"type":"ciProducts","id":"` + bnProduct + `","attributes":{"name":"Hjul","productType":"APP"},
	 "relationships":{"app":{"data":{"type":"apps","id":"A1"}}}}]}`
	bnBuildsDoc = `{"data":[
	 {"type":"builds","id":"b3","attributes":{"version":"64","uploadedDate":"2026-11-02T00:00:00Z"},"relationships":{"preReleaseVersion":{"data":{"type":"preReleaseVersions","id":"p1"}}}},
	 {"type":"builds","id":"b2","attributes":{"version":"66","uploadedDate":"2026-11-01T00:00:00Z"},"relationships":{"preReleaseVersion":{"data":{"type":"preReleaseVersions","id":"p2"}}}},
	 {"type":"builds","id":"b1","attributes":{"version":"x7","uploadedDate":"2026-10-01T00:00:00Z"}}],
	 "included":[{"type":"preReleaseVersions","id":"p1","attributes":{"version":"3.6","platform":"IOS"}},
	  {"type":"preReleaseVersions","id":"p2","attributes":{"version":"4.0","platform":"MAC_OS"}}]}`
)

// bnKit is a signed-in kit whose product counter starts at next and moves
// with every accepted PUT.
func bnKit(t *testing.T, next string) (*tooltest.Kit, *string) {
	t.Helper()
	k := kit(t).WebSession()
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, bnProdDoc)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts/{id}", http.StatusOK, `{"data":`+strings.TrimSuffix(strings.TrimPrefix(bnProdDoc, `{"data":[`), "]}")+`}`)
	k.ASC.Handle(http.MethodGet, "/v1/builds", http.StatusOK, bnBuildsDoc)
	k.Portal.HandleJSON(http.MethodGet, "/olympus/v1/session", `{"provider":{"providerId":4242424,"publicProviderId":"`+bnTeam+`"}}`)
	cur := &next
	k.Portal.Handle(http.MethodGet, bnPath, func(tooltest.PortalCall) (string, error) {
		return `{"next_build_number":` + *cur + `,"testflight_url":"https://example.invalid"}`, nil
	})
	for _, n := range []string{"67", "70", "60"} {
		k.Portal.Handle(http.MethodPut, bnPath+"?next_build_number="+n, func(tooltest.PortalCall) (string, error) {
			*cur = n
			return "", nil
		})
	}
	return k, cur
}

func bnPuts(k *tooltest.Kit) int {
	n := 0
	for _, c := range k.Portal.Calls() {
		if c.Method == http.MethodPut {
			n++
		}
	}
	return n
}

func TestBuildNumberRead(t *testing.T) {
	k, _ := bnKit(t, "66")
	k.ReadOnly()
	tl := lookup(t, "xcc_build_number")
	if !tl.ListedIn(tool.ProfileInfo{}) {
		t.Fatal("not listed read-only")
	}
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul"}`))
	out := res.Data.(buildNumberOut)
	if out.Next != 66 || out.AppID != "A1" || out.Product.ID != bnProduct || out.Scanned != 3 || out.Status != "" {
		t.Fatalf("out = %+v", out)
	}
	if h := out.Highest; h == nil || h.ID != "b2" || h.BuildNumber != "66" || h.Platform != "MAC_OS" || h.Version != "4.0" {
		t.Fatalf("highest = %+v", out.Highest)
	}
	// 66 is already uploaded: warn and suggest 67.
	if len(res.Meta.Warnings) != 1 || len(res.Next) != 1 || res.Next[0].Args["next"] != 67 {
		t.Fatalf("warnings %v next %+v", res.Meta.Warnings, res.Next)
	}
	q := k.ASC.Requests()[1].Query
	if q.Get("filter[app]") != "A1" || q.Get("sort") != "-uploadedDate" || q.Get("include") != "preReleaseVersion" {
		t.Fatalf("builds query = %v", q)
	}
	// Product id instead of app.
	res = tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"product":"`+bnProduct+`"}`))
	if out := res.Data.(buildNumberOut); out.AppID != "A1" || out.Next != 66 {
		t.Fatalf("by product = %+v", out)
	}
	// Setting needs writes.
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","next":67,"confirm":true}`), "writes_disabled")
	if bnPuts(k) != 0 {
		t.Fatal("PUT in read-only mode")
	}
}

func TestBuildNumberSet(t *testing.T) {
	k, cur := bnKit(t, "66")
	tl := lookup(t, "xcc_build_number")
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","next":67}`))
	if bnPuts(k) != 0 || *cur != "66" {
		t.Fatal("preview wrote")
	}
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","next":67,"confirm":true}`))
	out := res.Data.(buildNumberOut)
	if bnPuts(k) != 1 || out.Status != "set" || out.Next != 67 || out.Previous == nil || *out.Previous != 66 {
		t.Fatalf("out = %+v puts %d", out, bnPuts(k))
	}
	// Same value: nothing to do.
	res = tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","next":67,"confirm":true}`))
	if out := res.Data.(buildNumberOut); out.Status != "already" || bnPuts(k) != 1 {
		t.Fatalf("already = %+v", out)
	}
}

// A confirm whose counter moved since the preview needs a new preview.
func TestBuildNumberConfirmBoundToCurrent(t *testing.T) {
	k, cur := bnKit(t, "66")
	tl := lookup(t, "xcc_build_number")
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","next":70}`))
	*cur = "68"
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","next":70,"confirm":true}`), tool.ErrPreviewRequired.Code)
	if bnPuts(k) != 0 {
		t.Fatal("stale confirm wrote")
	}
}

func TestBuildNumberRefusesLowering(t *testing.T) {
	k, _ := bnKit(t, "68")
	tl := lookup(t, "xcc_build_number")
	for _, args := range []string{`{"app":"Hjul","next":67,"confirm":true}`, `{"app":"Hjul","next":60}`} {
		b := tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, args), tool.ErrInvalidArgs.Code)
		if !strings.Contains(b.Message, "68") {
			t.Errorf("%s: %s", args, b.Message)
		}
	}
	// Above the current value but not above the highest upload.
	k2, _ := bnKit(t, "50")
	b := tooltest.MustFail(t, tooltest.Call(t, tl, k2.Env, `{"app":"Hjul","next":60}`), tool.ErrInvalidArgs.Code)
	if !strings.Contains(b.Message, "uploaded build 66") {
		t.Errorf("message = %s", b.Message)
	}
	// allow_lower needs a human.
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","next":60,"allow_lower":true,"confirm":true}`), "human_approval_required")
	if bnPuts(k) != 0 || bnPuts(k2) != 0 {
		t.Fatal("refused lowering wrote")
	}
	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","next":60,"allow_lower":true,"confirm":true}`))
	if out := res.Data.(buildNumberOut); out.Next != 60 || out.Status != "set" {
		t.Fatalf("out = %+v", out)
	}
}

func TestBuildNumberErrors(t *testing.T) {
	tl := lookup(t, "xcc_build_number")
	// No web session.
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, bnProdDoc)
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul"}`), "web_session_required")

	k, _ = bnKit(t, "66")
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","product":"`+bnProduct+`"}`), tool.ErrInvalidArgs.Code)
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"product":"PR1"}`), tool.ErrInvalidArgs.Code)

	// Product of another team.
	k, _ = bnKit(t, "66")
	k.Portal.HandleError(http.MethodGet, bnPath, http.StatusNotFound)
	b := tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul"}`), "not_found")
	if !strings.Contains(b.Message, bnProduct) {
		t.Errorf("message = %s", b.Message)
	}

	// No product, several products.
	k = kit(t).WebSession()
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, `{"data":[]}`)
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul"}`), "not_found")
	k = kit(t).WebSession()
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, `{"data":[
	 {"type":"ciProducts","id":"`+bnProduct+`","attributes":{"name":"Hjul"}},
	 {"type":"ciProducts","id":"`+bnTeam+`","attributes":{"name":"Sikt"}}]}`)
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{}`), "ambiguous")

	// Session without a team public id.
	k = kit(t).WebSession()
	k.ASC.Handle(http.MethodGet, "/v1/ciProducts", http.StatusOK, bnProdDoc)
	k.Portal.HandleJSON(http.MethodGet, "/olympus/v1/session", `{"provider":{"providerId":1}}`)
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul"}`), "web_session_required")
}

// A failed PUT that may have landed suggests reading the value back.
func TestBuildNumberPutFailureSuggestsRead(t *testing.T) {
	k, _ := bnKit(t, "66")
	k.Portal.Handle(http.MethodPut, bnPath+"?next_build_number=67", func(tooltest.PortalCall) (string, error) {
		return "", errors.New("connection reset")
	})
	tl := lookup(t, "xcc_build_number")
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"app":"Hjul","next":67}`))
	res := tooltest.Call(t, tl, k.Env, `{"app":"Hjul","next":67,"confirm":true}`)
	if !res.IsError || !strings.Contains(tooltest.JSON(t, res), `"xcc_build_number"`) {
		t.Fatalf("res = %s", tooltest.JSON(t, res))
	}
}
