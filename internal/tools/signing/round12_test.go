package signing

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// An empty 201 for a device POST is registered when the read-back finds it.
func TestDevicesRegisterEmptyBodyReadsBack(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/devices", http.StatusOK, `{"data":[]}`)
	k.ASC.Handle(http.MethodGet, "/v1/devices", http.StatusOK, `{"data":[]}`)
	k.ASC.Handle(http.MethodGet, "/v1/devices", http.StatusOK,
		`{"data":[{"type":"devices","id":"D7","attributes":{"name":"Mac","udid":"M-1","platform":"MAC_OS","status":"ENABLED"}}]}`)
	k.ASC.Handle(http.MethodPost, "/v1/devices", http.StatusCreated, ``)
	k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "signing_devices_register"), k.Env,
		`{"devices":[{"name":"Mac","udid":"M-1","platform":"MAC_OS"}],"confirm":true}`))
	if j := tooltest.JSON(t, res); !strings.Contains(j, `"action":"`+devRegistered+`"`) || !strings.Contains(j, `"D7"`) {
		t.Fatalf("out = %s", j)
	}
	if n := len(k.ASC.Mutations()); n != 1 {
		t.Fatalf("mutations = %d", n)
	}
}

// A profile whose included certificates hit the 50 cap is paged in full,
// so a certificate past index 50 still counts.
func TestRevokePreviewPagesTruncatedCertificates(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/certificates/{id}", http.StatusOK, certOne)
	rel := make([]string, certRelLimit)
	for i := range rel {
		rel[i] = fmt.Sprintf(`{"type":"certificates","id":"X%d"}`, i)
	}
	k.ASC.Handle(http.MethodGet, "/v1/profiles", http.StatusOK, `{"data":[
	 {"type":"profiles","id":"P1","attributes":{"name":"Team Dev","profileState":"ACTIVE"},
	  "relationships":{"certificates":{"data":[`+strings.Join(rel, ",")+`]}}},
	 {"type":"profiles","id":"P2","attributes":{"name":"Other","profileState":"ACTIVE"},
	  "relationships":{"certificates":{"data":[{"type":"certificates","id":"X1"}]}}}]}`)
	k.ASC.Handle(http.MethodGet, "/v1/profiles/{id}/relationships/certificates", http.StatusOK,
		`{"data":[{"type":"certificates","id":"X0"},{"type":"certificates","id":"C1"}]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "signing_revoke"), k.Env, `{"kind":"certificate","id":"C1"}`))
	if j := tooltest.JSON(t, res); !strings.Contains(j, "invalidates 1 profile(s)") || !strings.Contains(j, "Team Dev") {
		t.Fatalf("preview = %s", j)
	}
	if n := len(k.ASC.RequestsTo(http.MethodGet, "/v1/profiles/{id}/relationships/certificates")); n != 1 {
		t.Fatalf("relationship reads = %d, want 1 (only the truncated profile)", n)
	}
}

// A confirmed re-run after a DELETE that applied finds the object gone:
// already, not not_found. A preview of a missing id stays not_found.
func TestRevokeConfirmedRerunGoneIsAlready(t *testing.T) {
	for _, kind := range []string{"certificate", "profile"} {
		k := newKit(t)
		k.ASC.Handle(http.MethodGet, "/v1/"+kind+"s/{id}", http.StatusNotFound,
			`{"errors":[{"status":"404","code":"NOT_FOUND","title":"not found"}]}`)
		tl := mustTool(t, "signing_revoke")
		res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"kind":"`+kind+`","id":"Z1","confirm":true}`))
		if o := tooltest.Decode[map[string]any](t, res); o["already"] != true {
			t.Fatalf("%s: out = %s", kind, tooltest.JSON(t, o))
		}
		fail := tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"kind":"`+kind+`","id":"Z1"}`), "not_found")
		if j := tooltest.JSON(t, fail); !strings.Contains(j, "earlier attempt") {
			t.Fatalf("%s: no hint in %s", kind, j)
		}
		if n := len(k.ASC.Mutations()); n != 0 {
			t.Fatalf("%s: mutations = %d", kind, n)
		}
	}
}
