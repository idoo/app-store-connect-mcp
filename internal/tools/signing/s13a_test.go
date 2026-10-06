package signing

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

// s13aKit returns a kit whose FakeASC checks every request against the spec.
func s13aKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	return tooltest.New(t)
}

func s13aTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tl
}

func s13aLog(reqs []tool.Request) string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Method + " " + r.Path
	}
	return strings.Join(out, "\n")
}

func s13aWantLog(t *testing.T, k *tooltest.Kit, want ...string) {
	t.Helper()
	if got := s13aLog(k.ASC.Requests()); got != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func s13aBodyOf(t *testing.T, r tool.Request) map[string]any {
	t.Helper()
	b, err := json.Marshal(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const s13aBundleDoc = `{"data":[
 {"type":"bundleIds","id":"B1","attributes":{"identifier":"se.jvcr.hjul","name":"Hjul","platform":"IOS","seedId":"TEAM1"},
  "relationships":{"bundleIdCapabilities":{"data":[{"type":"bundleIdCapabilities","id":"B1_PUSH"},{"type":"bundleIdCapabilities","id":"B1_ICLOUD"}]}}},
 {"type":"bundleIds","id":"B2","attributes":{"identifier":"se.jvcr.hjul.widget","name":"Hjul Widget","platform":"IOS","seedId":"TEAM1"},
  "relationships":{"bundleIdCapabilities":{"data":[]}}}],
 "included":[
 {"type":"bundleIdCapabilities","id":"B1_PUSH","attributes":{"capabilityType":"PUSH_NOTIFICATIONS"}},
 {"type":"bundleIdCapabilities","id":"B1_ICLOUD","attributes":{"capabilityType":"ICLOUD","settings":[
   {"key":"ICLOUD_VERSION","options":[{"key":"XCODE_5","enabled":false},{"key":"XCODE_6","enabled":true}]}]}}]}`

const s13aEmptyDoc = `{"data":[]}`

func TestBundleIDsListWithCapabilities(t *testing.T) {
	k := s13aKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, s13aBundleDoc)
	res := tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_bundle_ids_list"), k.Env, `{"platform":"IOS","query":"WIDGET"}`))
	s13aWantLog(t, k, "GET /v1/bundleIds")
	q := k.ASC.Requests()[0].Query
	if q.Get("include") != "bundleIdCapabilities" || q.Get("filter[platform]") != "IOS" || q.Get("limit[bundleIdCapabilities]") != "50" {
		t.Fatalf("query = %s", q.Encode())
	}
	out := tooltest.Decode[bundleIDsListOut](t, res)
	if out.Returned != 1 || out.BundleIDs[0].Identifier != "se.jvcr.hjul.widget" || len(out.BundleIDs[0].Capabilities) != 0 {
		t.Fatalf("query filter: %+v", out)
	}

	k2 := s13aKit(t)
	k2.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, s13aBundleDoc)
	out = tooltest.Decode[bundleIDsListOut](t, tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_bundle_ids_list"), k2.Env, ``)))
	if out.Returned != 2 {
		t.Fatalf("returned = %d", out.Returned)
	}
	caps := out.BundleIDs[0].Capabilities
	if len(caps) != 2 || caps[0].Type != "ICLOUD" || caps[1].Type != "PUSH_NOTIFICATIONS" {
		t.Fatalf("capabilities = %+v", caps)
	}
	if s := caps[0].Settings; len(s) != 1 || s[0].Key != "ICLOUD_VERSION" || len(s[0].Enabled) != 1 || s[0].Enabled[0] != "XCODE_6" {
		t.Fatalf("settings = %+v", s)
	}
}

func TestBundleIDCreateNew(t *testing.T) {
	k := s13aKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, s13aBundleDoc) // prefix match, no exact hit
	k.ASC.Handle(http.MethodPost, "/v1/bundleIds", http.StatusCreated,
		`{"data":{"type":"bundleIds","id":"B9","attributes":{"identifier":"se.jvcr.hjul.watch","name":"Hjul Watch","platform":"IOS","seedId":"TEAM1"}}}`)
	res := tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_bundle_id_create"), k.Env,
		`{"identifier":"se.jvcr.hjul.watch","name":"Hjul Watch","platform":"IOS"}`))
	s13aWantLog(t, k, "GET /v1/bundleIds", "POST /v1/bundleIds")
	if f := k.ASC.Requests()[0].Query.Get("filter[identifier]"); f != "se.jvcr.hjul.watch" {
		t.Fatalf("filter = %q", f)
	}
	attrs := s13aBodyOf(t, k.ASC.Requests()[1])["data"].(map[string]any)["attributes"].(map[string]any)
	if attrs["identifier"] != "se.jvcr.hjul.watch" || attrs["platform"] != "IOS" || attrs["name"] != "Hjul Watch" {
		t.Fatalf("attrs = %v", attrs)
	}
	out := tooltest.Decode[bundleIDCreateOut](t, res)
	if !out.Created || out.BundleID.ID != "B9" {
		t.Fatalf("out = %+v", out)
	}
}

func TestBundleIDCreateExistingIsAlready(t *testing.T) {
	k := s13aKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, s13aBundleDoc)
	res := tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_bundle_id_create"), k.Env,
		`{"identifier":"se.jvcr.hjul","name":"Hjul","platform":"MAC_OS"}`))
	if len(k.ASC.Mutations()) != 0 {
		t.Fatalf("mutations = %v", s13aLog(k.ASC.Mutations()))
	}
	out := tooltest.Decode[map[string]any](t, res)
	if out["already"] != true || out["result"].(map[string]any)["bundle_id"].(map[string]any)["id"] != "B1" {
		t.Fatalf("out = %v", out)
	}
	if len(res.Meta.Warnings) != 1 || !strings.Contains(res.Meta.Warnings[0], "platform IOS") {
		t.Fatalf("warnings = %v", res.Meta.Warnings)
	}
}

func TestBundleIDCreateConflictReadsBack(t *testing.T) {
	k := s13aKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, s13aEmptyDoc)
	k.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, s13aBundleDoc)
	k.ASC.Handle(http.MethodPost, "/v1/bundleIds", http.StatusConflict,
		`{"errors":[{"status":"409","code":"ENTITY_ERROR.ATTRIBUTE.INVALID","title":"An attribute value is invalid.","detail":"An App ID with Identifier 'se.jvcr.hjul' is not available."}]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_bundle_id_create"), k.Env,
		`{"identifier":"se.jvcr.hjul","name":"Hjul","platform":"IOS"}`))
	s13aWantLog(t, k, "GET /v1/bundleIds", "POST /v1/bundleIds", "GET /v1/bundleIds")
	if out := tooltest.Decode[map[string]any](t, res); out["already"] != true {
		t.Fatalf("out = %v", out)
	}
}

func TestBundleIDCreateRejectsBadIdentifier(t *testing.T) {
	k := s13aKit(t)
	tooltest.MustFail(t, tooltest.Call(t, s13aTool(t, "signing_bundle_id_create"), k.Env,
		`{"identifier":"se jvcr/hjul","name":"x","platform":"IOS"}`), tool.ErrInvalidArgs.Code)
	if len(k.ASC.Requests()) != 0 {
		t.Fatal("no request expected")
	}
}

func TestCapabilitiesEnableIdempotentAndAdditive(t *testing.T) {
	k := s13aKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, s13aBundleDoc)
	k.ASC.Handle(http.MethodPost, "/v1/bundleIdCapabilities", http.StatusCreated,
		`{"data":{"type":"bundleIdCapabilities","id":"B1_GROUPS","attributes":{"capabilityType":"APP_GROUPS"}}}`)
	res := tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_capabilities_set"), k.Env, `{"bundle_id":"se.jvcr.hjul",
		"enable":[{"type":"PUSH_NOTIFICATIONS"},{"type":"APP_GROUPS"},
		{"type":"ICLOUD","settings":[{"key":"ICLOUD_VERSION","options":[{"key":"XCODE_6"}]}]}]}`))
	s13aWantLog(t, k, "GET /v1/bundleIds", "POST /v1/bundleIdCapabilities")
	data := s13aBodyOf(t, k.ASC.Requests()[1])["data"].(map[string]any)
	if data["attributes"].(map[string]any)["capabilityType"] != "APP_GROUPS" {
		t.Fatalf("body = %v", data)
	}
	if rel := data["relationships"].(map[string]any)["bundleId"].(map[string]any)["data"].(map[string]any); rel["id"] != "B1" {
		t.Fatalf("rel = %v", rel)
	}
	out := tooltest.Decode[capsSetOut](t, res)
	acts := map[string]string{}
	for _, it := range out.Items {
		acts[it.Type] = it.Action
	}
	if acts["PUSH_NOTIFICATIONS"] != capAlreadyEnabled || acts["ICLOUD"] != capAlreadyEnabled || acts["APP_GROUPS"] != capEnabled || out.Failed != 0 {
		t.Fatalf("items = %+v", out.Items)
	}

	// Everything already enabled: no write at all.
	k2 := s13aKit(t)
	k2.ASC.Handle(http.MethodGet, "/v1/bundleIds/{id}", http.StatusOK,
		`{"data":{"type":"bundleIds","id":"B1","attributes":{"identifier":"se.jvcr.hjul","name":"Hjul","platform":"IOS"},
		"relationships":{"bundleIdCapabilities":{"data":[{"type":"bundleIdCapabilities","id":"B1_PUSH"}]}}},
		"included":[{"type":"bundleIdCapabilities","id":"B1_PUSH","attributes":{"capabilityType":"PUSH_NOTIFICATIONS"}}]}`)
	res = tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_capabilities_set"), k2.Env, `{"bundle_id":"B1","enable":[{"type":"PUSH_NOTIFICATIONS"}],"disable":["MAPS"]}`))
	s13aWantLog(t, k2, "GET /v1/bundleIds/B1")
	if out := tooltest.Decode[map[string]any](t, res); out["already"] != true {
		t.Fatalf("out = %v", out)
	}
}

func TestCapabilitiesDisableRequiresConfirm(t *testing.T) {
	k := s13aKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, s13aBundleDoc)
	args := `{"bundle_id":"se.jvcr.hjul","enable":[{"type":"MAPS"}],"disable":["PUSH_NOTIFICATIONS"]}`
	res := tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_capabilities_set"), k.Env, args))
	if len(k.ASC.Mutations()) != 0 {
		t.Fatalf("preview mutated: %s", s13aLog(k.ASC.Mutations()))
	}
	if out := tooltest.Decode[map[string]any](t, res); out["status"] != tool.ErrNeedsConfirm.Code {
		t.Fatalf("preview = %v", out)
	}

	k.ASC.Handle(http.MethodPost, "/v1/bundleIdCapabilities", http.StatusCreated,
		`{"data":{"type":"bundleIdCapabilities","id":"B1_MAPS","attributes":{"capabilityType":"MAPS"}}}`)
	k.ASC.Handle(http.MethodDelete, "/v1/bundleIdCapabilities/{id}", http.StatusNoContent, ``)
	res = tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_capabilities_set"), k.Env,
		strings.Replace(args, "{", `{"confirm":true,`, 1)))
	s13aWantLog(t, k, "GET /v1/bundleIds", "GET /v1/bundleIds", "POST /v1/bundleIdCapabilities", "DELETE /v1/bundleIdCapabilities/B1_PUSH")
	out := tooltest.Decode[capsSetOut](t, res)
	if out.Failed != 0 || len(out.Items) != 2 || out.Items[0].Action != capEnabled || out.Items[1].Action != capDisabled {
		t.Fatalf("items = %+v", out.Items)
	}
}

func TestCapabilitiesSettingsChangeRequiresConfirm(t *testing.T) {
	k := s13aKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, s13aBundleDoc)
	k.ASC.Handle(http.MethodPatch, "/v1/bundleIdCapabilities/{id}", http.StatusOK,
		`{"data":{"type":"bundleIdCapabilities","id":"B1_ICLOUD","attributes":{"capabilityType":"ICLOUD"}}}`)
	args := `{"bundle_id":"se.jvcr.hjul","enable":[{"type":"ICLOUD","settings":[{"key":"ICLOUD_VERSION","options":[{"key":"XCODE_5"}]}]}]}`
	tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_capabilities_set"), k.Env, args))
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("settings overwrite must preview first")
	}
	res := tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_capabilities_set"), k.Env, strings.Replace(args, "{", `{"confirm":true,`, 1)))
	s13aWantLog(t, k, "GET /v1/bundleIds", "GET /v1/bundleIds", "PATCH /v1/bundleIdCapabilities/B1_ICLOUD")
	if out := tooltest.Decode[capsSetOut](t, res); out.Items[0].Action != capUpdated {
		t.Fatalf("items = %+v", out.Items)
	}
}

func TestCapabilitiesValidation(t *testing.T) {
	k := s13aKit(t)
	tl := s13aTool(t, "signing_capabilities_set")
	for _, args := range []string{
		`{"bundle_id":"se.jvcr.hjul"}`,
		`{"bundle_id":"se.jvcr.hjul","enable":[{"type":"MAPS"}],"disable":["MAPS"]}`,
	} {
		tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, args), tool.ErrInvalidArgs.Code)
	}
	if len(k.ASC.Requests()) != 0 {
		t.Fatal("no request expected")
	}
}

func TestCapabilitiesUnknownBundle(t *testing.T) {
	k := s13aKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/bundleIds", http.StatusOK, s13aEmptyDoc)
	res := tooltest.Call(t, s13aTool(t, "signing_capabilities_set"), k.Env, `{"bundle_id":"se.jvcr.nope","enable":[{"type":"MAPS"}]}`)
	if !res.IsError || len(res.Next) == 0 || res.Next[0].Tool != "signing_bundle_ids_list" {
		t.Fatalf("res = %s", tooltest.JSON(t, res.Data))
	}
}

const s13aCertDoc = `{"data":[
 {"type":"certificates","id":"C3","attributes":{"name":"Apple Distribution","certificateType":"DISTRIBUTION","displayName":"Ivan","serialNumber":"S3","platform":"IOS","expirationDate":"2027-06-01T00:00:00.000+00:00","activated":true,"certificateContent":"MIIC3"}},
 {"type":"certificates","id":"C1","attributes":{"name":"Apple Development","certificateType":"DEVELOPMENT","serialNumber":"S1","expirationDate":"2025-12-20T00:00:00.000+00:00","certificateContent":"MIIC1"}},
 {"type":"certificates","id":"C2","attributes":{"name":"Developer ID","certificateType":"DEVELOPER_ID_APPLICATION","serialNumber":"S2","expirationDate":"2026-01-20T12:00:00.000+02:00","certificateContent":"MIIC2"}}]}`

func TestCertificatesExpiringFilterAndContentOmitted(t *testing.T) {
	k := s13aKit(t) // clock: 2026-01-02T03:04:05Z
	k.ASC.Handle(http.MethodGet, "/v1/certificates", http.StatusOK, s13aCertDoc)
	res := tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_certificates_list"), k.Env, `{"expiring_within_days":30}`))
	q := k.ASC.Requests()[0].Query
	if strings.Contains(q.Get("fields[certificates]"), "certificateContent") {
		t.Fatalf("content requested: %s", q.Encode())
	}
	out := tooltest.Decode[certificatesListOut](t, res)
	if out.Returned != 2 || out.Certificates[0].ID != "C1" || out.Certificates[1].ID != "C2" || out.Expired != 1 {
		t.Fatalf("out = %+v", out)
	}
	if !out.Certificates[0].Expired || *out.Certificates[0].DaysLeft != -14 || *out.Certificates[1].DaysLeft != 18 {
		t.Fatalf("days = %+v", out.Certificates)
	}
	if strings.Contains(tooltest.JSON(t, res.Data), "MIIC") {
		t.Fatal("certificate content leaked without include_content")
	}
}

func TestCertificatesTypeFilterAndContent(t *testing.T) {
	k := s13aKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/certificates", http.StatusOK, s13aCertDoc)
	res := tooltest.MustOK(t, tooltest.Call(t, s13aTool(t, "signing_certificates_list"), k.Env, `{"type":"DISTRIBUTION","include_content":true}`))
	q := k.ASC.Requests()[0].Query
	if q.Get("filter[certificateType]") != "DISTRIBUTION" || !strings.Contains(q.Get("fields[certificates]"), "certificateContent") {
		t.Fatalf("query = %s", q.Encode())
	}
	out := tooltest.Decode[certificatesListOut](t, res)
	if out.Returned != 3 || out.Certificates[2].ID != "C3" || out.Certificates[2].Content != "MIIC3" || out.Certificates[2].Activated == nil {
		t.Fatalf("out = %+v", out)
	}
}

func TestSigningToolsNeedTeamKey(t *testing.T) {
	k := s13aKit(t).Individual()
	for _, name := range []string{"signing_bundle_ids_list", "signing_bundle_id_create", "signing_capabilities_set", "signing_certificates_list"} {
		tl := s13aTool(t, name)
		if tl.ListedIn(k.Env.Profile) {
			t.Errorf("%s listed for an individual key", name)
		}
		if tl.Meta().Toolset != tool.Signing {
			t.Errorf("%s toolset = %s", name, tl.Meta().Toolset)
		}
		tooltest.MustFail(t, tooltest.Call(t, tl, k.Env, `{}`), tool.ErrIndividualKeyUnsupported.Code)
	}
	if len(k.ASC.Requests()) != 0 {
		t.Fatal("individual key must not reach Apple")
	}
}
