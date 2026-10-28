package release

import (
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// withPublishState replaces the kit's web client with one serving the given
// publish state.
func withPublishState(t *testing.T, k *tooltest.Kit, published bool) *tooltest.FakeASC {
	w := tooltest.NewFakeASC(t)
	body := `{"data":{"type":"appDataUsagesPublishState","id":"ps1","attributes":{"published":false}}}`
	if published {
		body = strings.Replace(body, "false", "true", 1)
	}
	w.Handle("GET", "/v1/apps/{id}/dataUsagePublishState", 200, body)
	k.Env.Web = w
	return w
}

func TestStatusAppPrivacyStates(t *testing.T) {
	// No web session: the manual check, now pointing at web login.
	k := setup(t, readyFixture())
	out, _ := call(t, k, `{"app":"Hjul"}`)
	c := checkByID(t, out, "app_privacy")
	if c.Status != statusManual || !strings.Contains(c.Detail, "web login") || c.FixVia == nil || c.FixVia.Manual == "" {
		t.Errorf("no session: %+v", c)
	}

	// Published: ok, still ready.
	k = setup(t, readyFixture())
	w := withPublishState(t, k, true)
	out, _ = call(t, k, `{"app":"Hjul"}`)
	if c := checkByID(t, out, "app_privacy"); c.Status != statusOK {
		t.Errorf("published: %+v", c)
	}
	if n := len(w.Requests()); n != 1 {
		t.Errorf("web requests = %d, want 1", n)
	}

	// Not published: blocks with privacy_plan as the fix.
	k = setup(t, readyFixture())
	withPublishState(t, k, false)
	out, _ = call(t, k, `{"app":"Hjul"}`)
	c = checkByID(t, out, "app_privacy")
	if c.Status != statusBlock || c.FixVia == nil || c.FixVia.Tool != "privacy_plan" || c.FixVia.Args["app"] != "100" {
		t.Errorf("unpublished: %+v", c)
	}
	if ready(out) {
		t.Error("ready with unpublished App Privacy")
	}

	// Any other web error never fails the status.
	k = setup(t, readyFixture())
	w = tooltest.NewFakeASC(t)
	w.Handle("GET", "/v1/apps/{id}/dataUsagePublishState", 500, `{"errors":[{"status":"500","code":"UNEXPECTED_ERROR"}]}`)
	k.Env.Web = w
	out, _ = call(t, k, `{"app":"Hjul"}`)
	if c := checkByID(t, out, "app_privacy"); c.Status != statusManual || !strings.Contains(c.Detail, "web-session read failed") {
		t.Errorf("web error: %+v", c)
	}
}
