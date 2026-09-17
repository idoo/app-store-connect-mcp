package signing

import (
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

const profilesDoc = `{"data":[
 {"type":"profiles","id":"P1","attributes":{"name":"Hjul Dev","profileType":"IOS_APP_DEVELOPMENT","profileState":"ACTIVE","platform":"IOS","uuid":"U1","expirationDate":"2026-03-01T00:00:00.000+00:00"},
  "relationships":{"bundleId":{"data":{"type":"bundleIds","id":"B1"}},
   "devices":{"data":[{"type":"devices","id":"D1"},{"type":"devices","id":"D2"}]},
   "certificates":{"data":[{"type":"certificates","id":"C1"}]}}},
 {"type":"profiles","id":"P2","attributes":{"name":"Widget Store","profileType":"IOS_APP_STORE","profileState":"INVALID","platform":"IOS","uuid":"U2","expirationDate":"2026-12-01T00:00:00.000+00:00"},
  "relationships":{"bundleId":{"data":{"type":"bundleIds","id":"B2"}},"devices":{"data":[]},
   "certificates":{"data":[{"type":"certificates","id":"C3"}]}}}],
 "included":[
 {"type":"bundleIds","id":"B1","attributes":{"identifier":"se.jvcr.hjul","name":"Hjul","platform":"IOS"}},
 {"type":"bundleIds","id":"B2","attributes":{"identifier":"se.jvcr.hjul.widget","name":"Hjul Widget","platform":"IOS"}},
 {"type":"devices","id":"D1","attributes":{"udid":"u1"}},{"type":"devices","id":"D2","attributes":{"udid":"u2"}},
 {"type":"certificates","id":"C1","attributes":{"name":"Dev"}},{"type":"certificates","id":"C3","attributes":{"name":"Dist"}}]}`

func TestProfilesListCountsAndBundleFilter(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/profiles", http.StatusOK, profilesDoc)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "signing_profiles_list"), k.Env, `{"bundle_id":"se.jvcr.hjul","state":"ACTIVE"}`))
	wantLog(t, k, "GET /v1/profiles")
	q := k.ASC.Requests()[0].Query
	if q.Get("filter[profileState]") != "ACTIVE" || strings.Contains(q.Get("fields[profiles]"), "profileContent") || q.Get("limit[devices]") != "50" {
		t.Fatalf("query = %s", q.Encode())
	}
	out := tooltest.Decode[profilesListOut](t, res)
	if out.Returned != 1 {
		t.Fatalf("out = %+v", out)
	}
	p := out.Profiles[0]
	if p.ID != "P1" || p.BundleID != "se.jvcr.hjul" || p.DeviceCount != 2 || p.CertificateCount != 1 || p.DaysLeft == nil || *p.DaysLeft != 57 || p.Content != "" {
		t.Fatalf("profile = %+v", p)
	}
}

func TestProfilesDownloadSingle(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/profiles/{id}", http.StatusOK,
		`{"data":{"type":"profiles","id":"P1","attributes":{"name":"Hjul Dev","profileType":"IOS_APP_DEVELOPMENT","profileState":"ACTIVE","profileContent":"TUlNRQ=="},
		"relationships":{"bundleId":{"data":{"type":"bundleIds","id":"B1"}}}},
		"included":[{"type":"bundleIds","id":"B1","attributes":{"identifier":"se.jvcr.hjul"}}]}`)
	tl := mustTool(t, "signing_profiles_list")
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"id":"P1","download":true}`))
	wantLog(t, k, "GET /v1/profiles/P1")
	if !strings.Contains(k.ASC.Requests()[0].Query.Get("fields[profiles]"), "profileContent") {
		t.Fatal("content not requested")
	}
	out := tooltest.Decode[profilesListOut](t, res)
	if out.Returned != 1 || out.Profiles[0].Content != "TUlNRQ==" || out.Profiles[0].BundleID != "se.jvcr.hjul" {
		t.Fatalf("out = %+v", out)
	}
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"download":true}`), tool.ErrInvalidArgs.Code)
}

const oneBundle = `{"data":[{"type":"bundleIds","id":"B1","attributes":{"identifier":"se.jvcr.hjul","name":"Hjul","platform":"IOS"}}]}`

func TestProfileCreateAllCertsAndDevices(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, oneBundle)
	k.ASC.Handle(http.MethodGet, "/v1/profiles", http.StatusOK, `{"data":[]}`)
	k.ASC.Handle(http.MethodGet, "/v1/certificates", http.StatusOK, `{"data":[
	 {"type":"certificates","id":"C1","attributes":{"certificateType":"DEVELOPMENT","expirationDate":"2026-06-01T00:00:00Z"}},
	 {"type":"certificates","id":"C0","attributes":{"certificateType":"DEVELOPMENT","expirationDate":"2025-06-01T00:00:00Z"}}]}`)
	k.ASC.Handle(http.MethodGet, "/v1/devices", http.StatusOK, `{"data":[
	 {"type":"devices","id":"D1","attributes":{"udid":"u1"}},{"type":"devices","id":"D2","attributes":{"udid":"u2"}}]}`)
	k.ASC.Handle(http.MethodPost, "/v1/profiles", http.StatusCreated,
		`{"data":{"type":"profiles","id":"P9","attributes":{"name":"Hjul Dev 2","profileType":"IOS_APP_DEVELOPMENT","profileState":"ACTIVE","uuid":"U9","profileContent":"BIG"}}}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "signing_profile_create"), k.Env,
		`{"name":"Hjul Dev 2","profile_type":"IOS_APP_DEVELOPMENT","bundle_id":"se.jvcr.hjul","certificates":["all"],"devices":["all"]}`))
	wantLog(t, k, "GET /v1/bundleIds", "GET /v1/profiles", "GET /v1/certificates", "GET /v1/devices", "POST /v1/profiles")
	if q := k.ASC.Requests()[2].Query; q.Get("filter[certificateType]") != "DEVELOPMENT,IOS_DEVELOPMENT" {
		t.Fatalf("cert query = %s", q.Encode())
	}
	if q := k.ASC.Requests()[3].Query; q.Get("filter[status]") != "ENABLED" || q.Get("filter[platform]") != "IOS" {
		t.Fatalf("device query = %s", q.Encode())
	}
	data := bodyOf(t, k.ASC.Requests()[4])["data"].(map[string]any)
	attrs := data["attributes"].(map[string]any)
	rels := data["relationships"].(map[string]any)
	if attrs["name"] != "Hjul Dev 2" || attrs["profileType"] != "IOS_APP_DEVELOPMENT" {
		t.Fatalf("attrs = %v", attrs)
	}
	if got := tooltest.JSON(t, rels); got != `{"bundleId":{"data":{"id":"B1","type":"bundleIds"}},"certificates":{"data":[{"id":"C1","type":"certificates"}]},"devices":{"data":[{"id":"D1","type":"devices"},{"id":"D2","type":"devices"}]}}` {
		t.Fatalf("relationships = %s", got)
	}
	out := tooltest.Decode[profileCreateOut](t, res)
	if !out.Created || out.Profile.ID != "P9" || out.Profile.Content != "" || out.Profile.DeviceCount != 2 || out.Profile.BundleID != "se.jvcr.hjul" {
		t.Fatalf("out = %+v", out)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "signing_profiles_list" {
		t.Fatalf("next = %+v", res.Next)
	}
}

func TestProfileCreateExistingIsAlreadyAndValidation(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, oneBundle)
	k.ASC.Handle(http.MethodGet, "/v1/profiles", http.StatusOK, profilesDoc)
	tl := mustTool(t, "signing_profile_create")
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env,
		`{"name":"Hjul Dev","profile_type":"IOS_APP_DEVELOPMENT","bundle_id":"se.jvcr.hjul","certificates":["C1"],"devices":["D1"]}`))
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("existing profile must not be re-created")
	}
	if out := tooltest.Decode[map[string]any](t, res); out["already"] != true {
		t.Fatalf("out = %v", out)
	}
	k2 := newKit(t)
	for _, args := range []string{
		`{"name":"x","profile_type":"IOS_APP_STORE","bundle_id":"se.jvcr.hjul","certificates":["all"],"devices":["D1"]}`,
		`{"name":"x","profile_type":"IOS_APP_ADHOC","bundle_id":"se.jvcr.hjul","certificates":["all"]}`,
		`{"name":"x","profile_type":"IOS_APP_STORE","bundle_id":"se.jvcr.hjul","certificates":[]}`,
	} {
		tooltest.MustFail(t, tooltest.Call(t, tl, k2.Env, args), tool.ErrInvalidArgs.Code)
	}
	if len(k2.ASC.Requests()) != 0 {
		t.Fatal("no request expected")
	}
}

func TestDevicesRegisterIdempotentByUDID(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/devices", http.StatusOK,
		`{"data":[{"type":"devices","id":"D1","attributes":{"name":"Ivan iPhone","udid":"00008110-AAAA","platform":"IOS","status":"ENABLED"}}]}`)
	k.ASC.Handle(http.MethodPost, "/v1/devices", http.StatusCreated,
		`{"data":{"type":"devices","id":"D2","attributes":{"name":"iPad","udid":"00008120-BBBB","platform":"IOS","status":"ENABLED"}}}`)
	tl := mustTool(t, "signing_devices_register")
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env,
		`{"devices":[{"name":"Phone","udid":"00008110-aaaa"},{"name":"iPad","udid":"00008120-BBBB","platform":"IOS"}]}`))
	wantLog(t, k, "GET /v1/devices", "POST /v1/devices")
	if f := k.ASC.Requests()[0].Query.Get("filter[udid]"); f != "00008110-aaaa,00008120-BBBB" {
		t.Fatalf("filter = %q", f)
	}
	attrs := bodyOf(t, k.ASC.Requests()[1])["data"].(map[string]any)["attributes"].(map[string]any)
	if attrs["udid"] != "00008120-BBBB" || attrs["platform"] != "IOS" || attrs["name"] != "iPad" {
		t.Fatalf("attrs = %v", attrs)
	}
	out := tooltest.Decode[devicesRegisterOut](t, res)
	if out.Registered != 1 || out.Already != 1 || out.Failed != 0 || out.Items[0].ID != "D1" || out.Items[0].Action != devAlready || out.Items[1].ID != "D2" {
		t.Fatalf("out = %+v", out)
	}

	// Second run: everything known, no POST, already:true.
	k2 := newKit(t)
	k2.ASC.Handle(http.MethodGet, "/v1/devices", http.StatusOK,
		`{"data":[{"type":"devices","id":"D2","attributes":{"name":"iPad","udid":"00008120-BBBB","platform":"IOS","status":"DISABLED"}}]}`)
	res = tooltest.MustOK(t, tooltest.Call(t, tl, k2.Env, `{"devices":[{"name":"iPad","udid":"00008120-BBBB"}]}`))
	if len(k2.ASC.Mutations()) != 0 {
		t.Fatal("no POST expected")
	}
	if o := tooltest.Decode[map[string]any](t, res); o["already"] != true || len(res.Meta.Warnings) != 1 {
		t.Fatalf("out = %v warnings = %v", o, res.Meta.Warnings)
	}
	tooltest.MustFail(t, tooltest.Call(t, tl, k2.Env, `{"devices":[{"name":"a","udid":"X1"},{"name":"b","udid":"x1"}]}`), tool.ErrInvalidArgs.Code)
}

func TestDevicesRegisterConflictReadsBack(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/devices", http.StatusOK, `{"data":[]}`)
	k.ASC.Handle(http.MethodGet, "/v1/devices", http.StatusOK,
		`{"data":[{"type":"devices","id":"D7","attributes":{"name":"Mac","udid":"M-1","platform":"MAC_OS","status":"ENABLED"}}]}`)
	k.ASC.Handle(http.MethodPost, "/v1/devices", http.StatusConflict,
		`{"errors":[{"status":"409","code":"ENTITY_ERROR.ATTRIBUTE.INVALID","title":"invalid","detail":"A device with number 'M-1' already exists on this team."}]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "signing_devices_register"), k.Env, `{"devices":[{"name":"Mac","udid":"M-1","platform":"MAC_OS"}]}`))
	wantLog(t, k, "GET /v1/devices", "POST /v1/devices", "GET /v1/devices")
	if o := tooltest.Decode[map[string]any](t, res); o["already"] != true {
		t.Fatalf("out = %v", o)
	}
}

const certOne = `{"data":{"type":"certificates","id":"C1","attributes":{"name":"Apple Development","certificateType":"DEVELOPMENT","displayName":"Ivan","serialNumber":"S1","expirationDate":"2026-06-01T00:00:00Z"}}}`

func TestRevokeCertificateNeedsHuman(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/certificates/{id}", http.StatusOK, certOne)
	k.ASC.Handle(http.MethodGet, "/v1/profiles", http.StatusOK, profilesDoc)
	tl := mustTool(t, "signing_revoke")

	// Preview: no mutation, names the cert and the affected profile.
	res := tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"kind":"certificate","id":"C1"}`))
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("preview mutated")
	}
	prev := tooltest.Decode[map[string]any](t, res)
	if prev["status"] != tool.ErrNeedsConfirm.Code {
		t.Fatalf("preview = %v", prev)
	}
	p := prev["preview"].(map[string]any)
	if s := p["summary"].(string); !strings.Contains(s, "Ivan") || !strings.Contains(s, "DEVELOPMENT") || !strings.Contains(s, "invalidates 1 profile") {
		t.Fatalf("summary = %s", s)
	}
	if aff := tooltest.JSON(t, p["affected"]); aff != `["certificate C1","profile Hjul Dev"]` {
		t.Fatalf("affected = %s", aff)
	}

	// confirm:true without elicitation is refused.
	tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{"kind":"certificate","id":"C1","confirm":true}`), tool.ErrHumanRequired.Code)
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("refused call mutated")
	}

	// Human approves: DELETE.
	asked := k.Elicit(true)
	k.ASC.Handle(http.MethodDelete, "/v1/certificates/{id}", http.StatusNoContent, ``)
	res = tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, `{"kind":"certificate","id":"C1","confirm":true}`))
	if m := k.ASC.Mutations(); len(m) != 1 || m[0].Method != http.MethodDelete || m[0].Path != "/v1/certificates/C1" {
		t.Fatalf("mutations = %s", requestLog(m))
	}
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "revoke certificate C1") {
		t.Fatalf("asked = %v", *asked)
	}
	if out := tooltest.Decode[revokeOut](t, res); out.Revoked.ID != "C1" || *out.Revoked.AffectedCount != 1 {
		t.Fatalf("out = %+v", out)
	}
}

func TestRevokeProfileDeclined(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/profiles/{id}", http.StatusOK,
		`{"data":{"type":"profiles","id":"P2","attributes":{"name":"Widget Store","profileType":"IOS_APP_STORE","profileState":"INVALID","expirationDate":"2026-12-01T00:00:00Z"}}}`)
	k.Elicit(false)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "signing_revoke"), k.Env, `{"kind":"profile","id":"P2","confirm":true}`), tool.ErrDeclined.Code)
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("declined call mutated")
	}
	k2 := newKit(t)
	k2.ASC.Handle(http.MethodGet, "/v1/certificates/{id}", http.StatusNotFound,
		`{"errors":[{"status":"404","code":"NOT_FOUND","title":"not found"}]}`)
	res := tooltest.Call(t, mustTool(t, "signing_revoke"), k2.Env, `{"kind":"certificate","id":"CX"}`)
	if !res.IsError || len(res.Next) == 0 || res.Next[0].Tool != "signing_certificates_list" {
		t.Fatalf("res = %s", tooltest.JSON(t, res.Data))
	}
}
