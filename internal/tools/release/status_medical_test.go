package release

import (
	"reflect"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const (
	medAccount     = "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b"
	medSessionPath = "/olympus/v1/session"
	medReqPath     = "/ppm/complianceform/v1/accounts/" + medAccount + "/requirements?contentId=100"
	medSessionBody = `{"provider":{"publicProviderId":"` + medAccount + `"}}`
)

// healthFixture is readyFixture with a Health & Fitness primary category.
func healthFixture() fixture {
	f := readyFixture()
	f.category = "HEALTH_AND_FITNESS"
	return f
}

// withMedicalDevice signs the kit in and serves the requirement with status.
func withMedicalDevice(k *tooltest.Kit, status string) {
	k.Env.Portal = k.Portal
	k.Portal.HandleJSON("GET", medSessionPath, medSessionBody)
	k.Portal.HandleJSON("GET", medReqPath, `{"requirementData":[{"contentId":"100","requirements":[{"id":"req-1","name":"MEDICAL_DEVICE","status":"`+status+`","isRequired":true}]}]}`)
}

func hasCheck(out statusOut, id string) bool {
	for _, c := range out.Checks {
		if c.ID == id {
			return true
		}
	}
	return false
}

func TestStatusMedicalDeviceNotApplicable(t *testing.T) {
	f := readyFixture()
	f.category = "PRODUCTIVITY"
	k := setup(t, f)
	k.Env.Portal = k.Portal // signed in: an unrouted portal request fails the test
	out, _ := call(t, k, `{"app":"Hjul"}`)
	if hasCheck(out, "medical_device") {
		t.Errorf("medical_device check for a non-health app: %+v", out.Checks)
	}
	if n := len(k.Portal.Calls()); n != 0 {
		t.Errorf("portal calls = %d, want 0", n)
	}
	if !ready(out) {
		t.Error("not ready")
	}
}

func TestStatusMedicalDeviceStates(t *testing.T) {
	// No web session: manual, still ready.
	k := setup(t, healthFixture())
	out, _ := call(t, k, `{"app":"Hjul"}`)
	c := checkByID(t, out, "medical_device")
	if c.Status != statusManual || !strings.Contains(c.Detail, "web_login") || strings.Contains(c.Detail, "read failed") ||
		c.FixVia == nil || c.FixVia.Tool != "" || !strings.HasSuffix(c.FixVia.Manual, "distribution/info") {
		t.Errorf("no session: %+v fix=%+v", c, c.FixVia)
	}
	if !ready(out) {
		t.Error("no session: not ready")
	}

	// Declared: ok with exactly two portal requests.
	k = setup(t, healthFixture())
	withMedicalDevice(k, tool.MedicalDeviceCollected)
	out, _ = call(t, k, `{"app":"Hjul"}`)
	if c := checkByID(t, out, "medical_device"); c.Status != statusOK {
		t.Errorf("declared: %+v", c)
	}
	if !ready(out) {
		t.Error("declared: not ready")
	}
	if n := len(k.Portal.Calls()); n != 2 {
		t.Errorf("portal calls = %d, want 2", n)
	}

	// Not declared: blocks with medical_device_set as the fix.
	k = setup(t, healthFixture())
	withMedicalDevice(k, "PENDING_COLLECTION")
	out, res := call(t, k, `{"app":"Hjul"}`)
	c = checkByID(t, out, "medical_device")
	want := map[string]any{"app": "100"}
	if c.Status != statusBlock || c.FixVia == nil || c.FixVia.Tool != "medical_device_set" || !reflect.DeepEqual(c.FixVia.Args, want) || c.FixVia.Manual == "" {
		t.Errorf("pending: %+v fix=%+v", c, c.FixVia)
	}
	if ready(out) {
		t.Error("ready without the medical device declaration")
	}
	found := false
	for _, n := range res.Next {
		if n.Tool != "medical_device_set" {
			continue
		}
		found = true
		if !reflect.DeepEqual(n.Args, want) {
			t.Errorf("next args = %+v, want %+v", n.Args, want)
		}
	}
	if !found {
		t.Errorf("next = %+v, want medical_device_set", res.Next)
	}

	// Any other portal error never fails the status.
	k = setup(t, healthFixture())
	k.Env.Portal = k.Portal
	k.Portal.HandleError("GET", medSessionPath, 500, "boom")
	out, _ = call(t, k, `{"app":"Hjul"}`)
	c = checkByID(t, out, "medical_device")
	if c.Status != statusManual || !strings.Contains(c.Detail, "web-session read failed") || c.FixVia == nil || c.FixVia.Tool != "" {
		t.Errorf("portal error: %+v", c)
	}
	if !ready(out) {
		t.Error("portal error: not ready")
	}
}

func TestStatusMedicalDeviceNoRequirementListed(t *testing.T) {
	k := setup(t, healthFixture())
	k.Env.Portal = k.Portal
	k.Portal.HandleJSON("GET", medSessionPath, medSessionBody)
	k.Portal.HandleJSON("GET", medReqPath, `{"requirementData":[]}`)
	out, _ := call(t, k, `{"app":"Hjul"}`)
	if hasCheck(out, "medical_device") {
		t.Errorf("medical_device check without a listed requirement: %+v", out.Checks)
	}
}

func TestStatusMedicalDeviceAgeRatingApplies(t *testing.T) {
	f := readyFixture()
	f.category = "PRODUCTIVITY"
	f.age = map[string]any{"medicalOrTreatmentInformation": "FREQUENT_OR_INTENSE"}
	k := setup(t, f)
	withMedicalDevice(k, "PENDING_COLLECTION")
	out, _ := call(t, k, `{"app":"Hjul"}`)
	if c := checkByID(t, out, "medical_device"); c.Status != statusBlock {
		t.Errorf("age rating: %+v", c)
	}
}
