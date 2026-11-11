package web

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const (
	mdAccount = "0a1b2c3d-0000-1111-2222-333344445555"
	mdBase    = "/ppm/complianceform/v1/accounts/" + mdAccount
	mdReqs    = mdBase + "/requirements?contentId=" + appID
	mdForm    = mdBase + "/requirements/req-1/forms?contentId=" + appID
	mdSave    = mdBase + "/contents/" + appID + "/requirements/req-1/forms"

	mdPending   = "PENDING_COLLECTION"
	mdCollected = "COLLECTED"

	// The form shapes below follow App Store Connect's answers (2026-11):
	// the stored object under data, the offered values under constraints.
	mdIdentity    = `"accountId":"` + mdAccount + `","contentId":"` + appID + `","requirementId":"req-1","requirementName":"MEDICAL_DEVICE"`
	mdConstraints = `{"$[*].countriesOrRegions":{"attributeName":"countriesOrRegions","required":true,"options":[{"value":"USA"},{"value":"GBR"},{"value":"EU"}]},
	 "$[*].medicalDeviceData.declaration":{"attributeName":"declaration","options":[{"value":"YES"},{"value":"NO"}],"selectedIndex":-1}}`
	mdUnanswered = `{"data":{` + mdIdentity + `,"medicalDeviceData":{}},"constraints":` + mdConstraints + `}`
	// A saved No keeps a row per region; the rows do not change the answer.
	mdAnsweredNo = `{"data":{` + mdIdentity + `,"formId":"form-1","medicalDeviceData":{"declaration":"no","registrationInfo":[
	 {"declaration":"yes","countriesOrRegions":["GBR"]},{"declaration":"yes","countriesOrRegions":["USA"]},{"declaration":"yes","countriesOrRegions":["EEA"]}]}},"constraints":` + mdConstraints + `}`
	mdAnsweredYes = `{"data":{` + mdIdentity + `,"formId":"form-1","medicalDeviceData":{"declaration":"yes","registrationInfo":[
	 {"declaration":"yes","countriesOrRegions":["GBR"]},{"declaration":"no","countriesOrRegions":["USA"]},{"declaration":"yes","countriesOrRegions":["EEA"]}]}},"constraints":` + mdConstraints + `}`
)

// mdState is what the fake App Store Connect holds; tests change it between
// calls.
type mdState struct {
	status, form string
	listed       bool
}

// mdKit signs the kit in and serves the session, the app's requirements and
// the medical device form from st.
func mdKit(t *testing.T, st *mdState) *tooltest.Kit {
	t.Helper()
	k := kit(t)
	k.Portal.HandleJSON(http.MethodGet, "/olympus/v1/session", `{"provider":{"providerId":4242424,"publicProviderId":"`+mdAccount+`"}}`)
	k.Portal.Handle(http.MethodGet, mdReqs, func(tooltest.PortalCall) (string, error) {
		reqs := `{"id":"req-9","name":"OTHER_REQUIREMENT","status":"` + mdPending + `","isRequired":false}`
		if st.listed {
			reqs += `,{"id":"req-1","name":"MEDICAL_DEVICE","ref":"medical","status":"` + st.status + `","formId":"form-1","isRequired":true}`
		}
		return `{"accountId":"` + mdAccount + `","requirementData":[{"contentId":"` + appID + `","requirements":[` + reqs + `]}]}`, nil
	})
	k.Portal.Handle(http.MethodGet, mdForm, func(tooltest.PortalCall) (string, error) { return st.form, nil })
	return k
}

// mdSaves returns the form writes sent so far.
func mdSaves(k *tooltest.Kit) []string {
	var out []string
	for _, c := range k.Portal.Calls() {
		if c.Method != http.MethodGet {
			out = append(out, c.Method+" "+c.Path+" "+c.Body)
		}
	}
	return out
}

func TestMedicalDeviceGetStates(t *testing.T) {
	get := mustTool(t, "medical_device_get")
	for _, tc := range []struct {
		name     string
		st       mdState
		want     medicalDeviceOut
		calls    int
		warn     string
		nextTool string
	}{
		{
			name: "listed, nothing saved", st: mdState{mdPending, mdUnanswered, true},
			want: medicalDeviceOut{State: mdNotDeclared, Required: true}, calls: 3,
			warn: "refuses to review", nextTool: "medical_device_set",
		},
		{
			// The region rows of a saved No are not regions it is regulated in.
			name: "No saved", st: mdState{mdCollected, mdAnsweredNo, true},
			want: medicalDeviceOut{State: mdNotRegulated, Required: true, Complete: true}, calls: 3,
		},
		{
			name: "Yes saved without its details", st: mdState{mdPending, mdAnsweredYes, true},
			want: medicalDeviceOut{State: mdRegulated, Required: true, Regions: []string{"EEA", "GBR"}}, calls: 3,
			warn: "not report the declaration as complete",
		},
		{
			// Older answers come as a one-element array or at the top level:
			// read as not declared, they would be declared a second time.
			name: "No saved, array form", st: mdState{mdCollected, `{"data":[{"medicalDeviceData":{"declaration":"NO"}}],"constraints":{}}`, true},
			want: medicalDeviceOut{State: mdNotRegulated, Required: true, Complete: true}, calls: 3,
		},
		{
			name: "No saved, top-level form", st: mdState{mdCollected, `{"medicalDeviceData":{"declaration":"no"},"constraints":{}}`, true},
			want: medicalDeviceOut{State: mdNotRegulated, Required: true, Complete: true}, calls: 3,
		},
		{
			name: "Apple tracks no declaration for the app", st: mdState{"", "", false},
			want: medicalDeviceOut{State: mdNotListed}, calls: 2,
		},
	} {
		k := mdKit(t, &tc.st).ReadOnly()
		res := tooltest.MustOK(t, tooltest.Call(t, get, k.Env, `{"app":"Hjul"}`))
		out := tooltest.Decode[medicalDeviceOut](t, res)
		if out.State != tc.want.State || out.Required != tc.want.Required || out.Complete != tc.want.Complete || !slices.Equal(out.Regions, tc.want.Regions) {
			t.Errorf("%s: out = %+v", tc.name, out)
		}
		if out.App.ID != appID || !strings.HasSuffix(out.Manual, "/apps/"+appID+"/distribution/info") {
			t.Errorf("%s: app %+v manual %s", tc.name, out.App, out.Manual)
		}
		if n := len(k.Portal.Calls()); n != tc.calls {
			t.Errorf("%s: web requests = %d, want %d", tc.name, n, tc.calls)
		}
		if (tc.warn == "") != (len(res.Meta.Warnings) == 0) || (tc.warn != "" && !strings.Contains(res.Meta.Warnings[0], tc.warn)) {
			t.Errorf("%s: warnings = %q", tc.name, res.Meta.Warnings)
		}
		if tc.nextTool != "" {
			// The answer itself is the caller's: the suggestion names the app only.
			if len(res.Next) != 1 || res.Next[0].Tool != tc.nextTool || len(res.Next[0].Args) != 1 || res.Next[0].Args["app"] != appID {
				t.Errorf("%s: next = %+v", tc.name, res.Next)
			}
		} else if len(res.Next) != 0 {
			t.Errorf("%s: next = %+v", tc.name, res.Next)
		}
	}
}

func TestMedicalDeviceNeedsWebSession(t *testing.T) {
	k := kit(t)
	k.Env.Portal = nil
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "medical_device_get"), k.Env, `{"app":"Hjul"}`), tool.ErrWebSessionRequired.Code)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "medical_device_set"), k.Env, `{"app":"Hjul","regulated":false}`), tool.ErrWebSessionRequired.Code)
}

func TestMedicalDeviceSetDeclaresNo(t *testing.T) {
	st := &mdState{mdPending, mdUnanswered, true}
	k := mdKit(t, st)
	k.Portal.Handle(http.MethodPost, mdSave, func(tooltest.PortalCall) (string, error) {
		st.status, st.form = mdCollected, mdAnsweredNo
		return `{}`, nil
	})
	set := mustTool(t, "medical_device_set")
	// What App Store Connect's own page sends for a first No: the form's
	// stored object, the answer, and the regions EEA, GBR, USA (not the
	// "EU" the form's constraints offer).
	body := `{"accountId":"` + mdAccount + `","contentId":"100","countriesOrRegions":["EEA","GBR","USA"],` +
		`"medicalDeviceData":{"declaration":"no"},"requirementId":"req-1","requirementName":"MEDICAL_DEVICE"}`

	// The preview writes nothing and shows the exact form that will be sent.
	res := tooltest.MustOK(t, tooltest.Call(t, set, k.Env, `{"app":"Hjul","regulated":false}`))
	if b := tooltest.JSON(t, res); !strings.Contains(b, `"needs_confirm"`) || !strings.Contains(b, "Hjul is not a regulated medical device") ||
		!strings.Contains(b, `"countriesOrRegions":["EEA","GBR","USA"]`) {
		t.Fatalf("preview = %s", b)
	}
	if got := mdSaves(k); len(got) != 0 {
		t.Fatalf("preview wrote %v", got)
	}

	// Without a human nothing is saved.
	tooltest.MustFail(t, tooltest.Call(t, set, k.Env, `{"app":"Hjul","regulated":false,"confirm":true}`), tool.ErrHumanRequired.Code)
	if got := mdSaves(k); len(got) != 0 {
		t.Fatalf("unapproved confirm wrote %v", got)
	}

	tooltest.MustOK(t, tooltest.Call(t, set, k.Env, `{"app":"Hjul","regulated":false}`))
	asked := k.Elicit(true)
	res = tooltest.MustOK(t, tooltest.Call(t, set, k.Env, `{"app":"Hjul","regulated":false,"confirm":true}`))
	out := tooltest.Decode[medicalDeviceOut](t, res)
	if out.Status != "declared" || out.State != mdNotRegulated || !out.Complete || len(out.Regions) != 0 {
		t.Errorf("out = %+v", out)
	}
	if got := mdSaves(k); len(got) != 1 || got[0] != "POST "+mdSave+" "+body {
		t.Errorf("writes = %v\nwant POST %s %s", got, mdSave, body)
	}
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "Hjul is not a regulated medical device") || !strings.Contains((*asked)[0], "FDA") ||
		!strings.Contains((*asked)[0], `"declaration":"no"`) {
		t.Errorf("approval prompts = %q", *asked)
	}
	if len(res.Next) != 1 || res.Next[0].Tool != "release_status" {
		t.Errorf("next = %+v", res.Next)
	}

	// Declared already: no second write, no second approval.
	res = tooltest.MustOK(t, tooltest.Call(t, set, k.Env, `{"app":"Hjul","regulated":false,"confirm":true}`))
	if b := tooltest.JSON(t, res); !strings.Contains(b, `"already":true`) || len(mdSaves(k)) != 1 || len(*asked) != 1 {
		t.Errorf("already = %s, writes %d, asked %d", b, len(mdSaves(k)), len(*asked))
	}
}

func TestMedicalDeviceSetRefuses(t *testing.T) {
	set := mustTool(t, "medical_device_set")
	for _, tc := range []struct {
		name, args, code, msg string
		st                    mdState
		calls                 int
	}{
		{name: "regulated device", args: `{"app":"Hjul","regulated":true,"confirm":true}`, code: tool.ErrInvalidArgs.Code, msg: "App Store Connect", st: mdState{mdPending, mdUnanswered, true}},
		{name: "answer left out", args: `{"app":"Hjul"}`, code: tool.ErrInvalidArgs.Code, msg: "regulated", st: mdState{mdPending, mdUnanswered, true}},
		{name: "already declared regulated", args: `{"app":"Hjul","regulated":false,"confirm":true}`, code: tool.ErrInvalidArgs.Code, msg: "declared a regulated medical device", st: mdState{mdPending, mdAnsweredYes, true}, calls: 3},
		{name: "No saved but incomplete", args: `{"app":"Hjul","regulated":false,"confirm":true}`, code: tool.ErrInvalidArgs.Code, msg: "not report it as complete", st: mdState{mdPending, mdAnsweredNo, true}, calls: 3},
		{name: "not tracked by Apple", args: `{"app":"Hjul","regulated":false}`, code: tool.ErrInvalidArgs.Code, msg: "nothing to declare", st: mdState{"", "", false}, calls: 2},
	} {
		k := mdKit(t, &tc.st)
		k.Elicit(true)
		b := tooltest.MustFail(t, tooltest.Call(t, set, k.Env, tc.args), tc.code)
		if !strings.Contains(b.Message, tc.msg) {
			t.Errorf("%s: message = %s", tc.name, b.Message)
		}
		if got := mdSaves(k); len(got) != 0 || len(k.Portal.Calls()) != tc.calls {
			t.Errorf("%s: writes %v, web requests %d (want %d)", tc.name, got, len(k.Portal.Calls()), tc.calls)
		}
	}

	// Read-only sessions cannot declare.
	st := mdState{mdPending, mdUnanswered, true}
	k := mdKit(t, &st).ReadOnly()
	tooltest.MustFail(t, tooltest.Call(t, set, k.Env, `{"app":"Hjul","regulated":false}`), tool.ErrWritesDisabled.Code)
}

// A write whose answer was lost is settled by reading the form back, never
// by sending it again.
func TestMedicalDeviceSetLostAnswer(t *testing.T) {
	set := mustTool(t, "medical_device_set")
	for _, landed := range []bool{true, false} {
		st := &mdState{mdPending, mdUnanswered, true}
		k := mdKit(t, st)
		k.Portal.Handle(http.MethodPost, mdSave, func(tooltest.PortalCall) (string, error) {
			if landed {
				st.status, st.form = mdCollected, mdAnsweredNo
			}
			return "", fmt.Errorf("POST: %w", tool.ErrOutcomeUnknown)
		})
		tooltest.MustOK(t, tooltest.Call(t, set, k.Env, `{"app":"Hjul","regulated":false}`))
		k.Elicit(true)
		res := tooltest.Call(t, set, k.Env, `{"app":"Hjul","regulated":false,"confirm":true}`)
		if len(mdSaves(k)) != 1 {
			t.Fatalf("landed=%v: writes = %v", landed, mdSaves(k))
		}
		if landed {
			if out := tooltest.Decode[medicalDeviceOut](t, tooltest.MustOK(t, res)); out.Status != "declared" || out.State != mdNotRegulated {
				t.Errorf("landed: out = %+v", out)
			}
			continue
		}
		tooltest.MustFail(t, res, tool.ErrOutcomeUnknown.Code)
		if len(res.Next) != 1 || res.Next[0].Tool != "medical_device_get" {
			t.Errorf("not landed: next = %+v", res.Next)
		}
	}

	// Apple's refusal is reported as is, without a read-back.
	st := &mdState{mdPending, mdUnanswered, true}
	k := mdKit(t, st)
	k.Portal.HandleError(http.MethodPost, mdSave, http.StatusForbidden)
	tooltest.MustOK(t, tooltest.Call(t, set, k.Env, `{"app":"Hjul","regulated":false}`))
	k.Elicit(true)
	res := tooltest.Call(t, set, k.Env, `{"app":"Hjul","regulated":false,"confirm":true}`)
	calls := k.Portal.Calls()
	if !res.IsError || len(mdSaves(k)) != 1 || calls[len(calls)-1].Method != http.MethodPost {
		t.Errorf("refused: error %v, writes %v, last request %+v", res.IsError, mdSaves(k), calls[len(calls)-1])
	}
}
