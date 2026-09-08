package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/idoo/hamn/internal/spec"
	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

var (
	specOnce sync.Once
	specIdx  *spec.Index
	specErr  error
)

func kit(t *testing.T) *tooltest.Kit {
	t.Helper()
	specOnce.Do(func() { specIdx, specErr = spec.Load() })
	if specErr != nil {
		t.Fatal(specErr)
	}
	k := tooltest.New(t)
	k.Env.Spec = specIdx
	k.Resolve.Apps = []tool.App{
		{ID: "A1", BundleID: "se.jvcr.hjul", Name: "Hjul"},
		{ID: "A2", BundleID: "se.jvcr.sikt", Name: "Sikt"},
	}
	t.Cleanup(func() {
		for _, r := range k.ASC.Requests() {
			if _, ok := specIdx.Match(r.Method, r.Path); !ok {
				t.Errorf("%s %s is not a spec operation", r.Method, r.Path)
				continue
			}
			if err := specIdx.Validate(r); err != nil {
				t.Errorf("%s %s?%s violates the spec: %v", r.Method, r.Path, r.Query.Encode(), err)
			}
		}
	})
	return k
}

func lookupTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tl
}

func wantLog(t *testing.T, k *tooltest.Kit, want ...string) {
	t.Helper()
	got := make([]string, 0)
	for _, r := range k.ASC.Requests() {
		got = append(got, r.Method+" "+r.Path)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

const usersDoc = `{"data":[
 {"type":"users","id":"U1","attributes":{"username":"ivan@example.com","firstName":"Ivan","lastName":"V","roles":["ACCOUNT_HOLDER","ADMIN"],"allAppsVisible":true,"provisioningAllowed":true}},
 {"type":"users","id":"U2","attributes":{"username":"dev@example.com","firstName":"Dana","lastName":"Dev","roles":["DEVELOPER"],"allAppsVisible":false},
  "relationships":{"visibleApps":{"data":[{"type":"apps","id":"A1"}]}}}],
 "included":[{"type":"apps","id":"A1","attributes":{"name":"Hjul","bundleId":"se.jvcr.hjul"}}]}`

const invitesDoc = `{"data":[
 {"type":"userInvitations","id":"I1","attributes":{"email":"new@example.com","firstName":"Nia","lastName":"New","roles":["MARKETING"],"allAppsVisible":true,"expirationDate":"2026-11-01T00:00:00Z"}}]}`

const emptyDoc = `{"data":[]}`

func TestUsersListWithInvitations(t *testing.T) {
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/users", http.StatusOK, usersDoc)
	k.ASC.Handle(http.MethodGet, "/v1/userInvitations", http.StatusOK, invitesDoc)
	res := tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "users_list"), k.Env, ``))
	wantLog(t, k, "GET /v1/users", "GET /v1/userInvitations")
	if q := k.ASC.Requests()[0].Query; q.Get("include") != "visibleApps" || q.Get("limit[visibleApps]") != "50" {
		t.Fatalf("query = %s", q.Encode())
	}
	out := tooltest.Decode[listOut](t, res)
	if len(out.Users) != 2 || len(out.Invitations) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if u := out.Users[1]; u.AllApps || len(u.Apps) != 1 || u.Apps[0].Name != "Hjul" || u.Roles[0] != "DEVELOPER" {
		t.Fatalf("user = %+v", u)
	}
	if inv := out.Invitations[0]; !inv.Pending || inv.Email != "new@example.com" || inv.Expires == "" {
		t.Fatalf("invitation = %+v", inv)
	}

	k = kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/users", http.StatusOK, usersDoc)
	out = tooltest.Decode[listOut](t, tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "users_list"), k.Env,
		`{"role":"DEVELOPER","query":"dana","no_invitations":true}`)))
	if k.ASC.Requests()[0].Query.Get("filter[roles]") != "DEVELOPER" || len(out.Users) != 1 || out.Users[0].ID != "U2" {
		t.Fatalf("out = %+v", out)
	}
}

const inviteArgs = `{"email":"qa@example.com","first_name":"Quinn","last_name":"Tester","roles":["DEVELOPER","APP_MANAGER"],"apps":["Hjul","se.jvcr.sikt"],"confirm":true}`

func TestUsersInvitePromptShowsEmailRolesApps(t *testing.T) {
	k := kit(t)
	asked := k.Elicit(true)
	k.ASC.Handle(http.MethodGet, "/v1/users", http.StatusOK, emptyDoc)
	k.ASC.Handle(http.MethodGet, "/v1/userInvitations", http.StatusOK, emptyDoc)
	k.ASC.Handle(http.MethodPost, "/v1/userInvitations", http.StatusCreated,
		`{"data":{"type":"userInvitations","id":"I9","attributes":{"email":"qa@example.com","firstName":"Quinn","lastName":"Tester","roles":["APP_MANAGER","DEVELOPER"],"allAppsVisible":false,"expirationDate":"2026-11-25T00:00:00Z"}}}`)
	res := tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "users_invite"), k.Env, inviteArgs))
	wantLog(t, k, "GET /v1/users", "GET /v1/userInvitations", "POST /v1/userInvitations")
	if len(*asked) != 1 {
		t.Fatalf("asked = %v", *asked)
	}
	for _, want := range []string{"qa@example.com", "APP_MANAGER", "DEVELOPER", "Hjul (A1)", "Sikt (A2)"} {
		if !strings.Contains((*asked)[0], want) {
			t.Fatalf("prompt lacks %q: %s", want, (*asked)[0])
		}
	}
	b, _ := json.Marshal(k.ASC.Requests()[2].Body)
	body := string(b)
	if !strings.Contains(body, `"visibleApps":{"data":[{"id":"A1","type":"apps"},{"id":"A2","type":"apps"}]}`) ||
		!strings.Contains(body, `"allAppsVisible":false`) {
		t.Fatalf("body = %s", body)
	}
	out := tooltest.Decode[map[string]member](t, res)
	if inv := out["invited"]; inv.ID != "I9" || len(inv.Apps) != 2 || !inv.Pending {
		t.Fatalf("out = %+v", out)
	}
}

func TestUsersInviteGates(t *testing.T) {
	// Without elicitation: refused, nothing written.
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/users", http.StatusOK, emptyDoc)
	k.ASC.Handle(http.MethodGet, "/v1/userInvitations", http.StatusOK, emptyDoc)
	tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "users_invite"), k.Env, inviteArgs), tool.ErrHumanRequired.Code)
	// Declined.
	k.Elicit(false)
	tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "users_invite"), k.Env, inviteArgs), tool.ErrDeclined.Code)
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("unapproved invite wrote")
	}
	// Existing member: already, no prompt.
	k = kit(t)
	asked := k.Elicit(true)
	k.ASC.Handle(http.MethodGet, "/v1/users", http.StatusOK, emptyDoc)
	k.ASC.Handle(http.MethodGet, "/v1/userInvitations", http.StatusOK,
		`{"data":[{"type":"userInvitations","id":"I3","attributes":{"email":"QA@example.com","firstName":"Q","lastName":"T","roles":["DEVELOPER"]}}]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "users_invite"), k.Env, inviteArgs))
	if m := tooltest.Decode[map[string]any](t, res); m["already"] != true || len(*asked) != 0 || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("out = %v asked = %v", m, *asked)
	}
	// Validation.
	k = kit(t)
	for _, args := range []string{
		`{"email":"nope","first_name":"a","last_name":"b","roles":["DEVELOPER"],"all_apps":true}`,
		`{"email":"a@b.se","first_name":"a","last_name":"b","roles":["ACCOUNT_HOLDER"],"all_apps":true}`,
		`{"email":"a@b.se","first_name":"a","last_name":"b","roles":["DEVELOPER"]}`,
		`{"email":"a@b.se","first_name":"a","last_name":"b","roles":["DEVELOPER"],"all_apps":true,"apps":["Hjul"]}`,
		`{"email":"a@b.se","first_name":"a","last_name":"b","roles":["WIZARD"],"all_apps":true}`,
	} {
		tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "users_invite"), k.Env, args), tool.ErrInvalidArgs.Code)
	}
	if len(k.ASC.Requests()) != 0 {
		t.Fatal("invalid args made requests")
	}
}

func TestUsersRemoveUserByEmail(t *testing.T) {
	k := kit(t)
	asked := k.Elicit(true)
	k.ASC.Handle(http.MethodGet, "/v1/users", http.StatusOK, usersDoc)
	k.ASC.Handle(http.MethodDelete, "/v1/users/U2", http.StatusNoContent, ``)
	res := tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "users_remove"), k.Env, `{"user":"dev@example.com","confirm":true}`))
	wantLog(t, k, "GET /v1/users", "DELETE /v1/users/U2")
	if k.ASC.Requests()[0].Query.Get("filter[username]") != "dev@example.com" {
		t.Fatalf("query = %s", k.ASC.Requests()[0].Query.Encode())
	}
	if m := tooltest.Decode[map[string]any](t, res); m["removed_user"] == nil || !strings.Contains((*asked)[0], "remove dev@example.com (DEVELOPER)") {
		t.Fatalf("out = %v prompt = %v", m, *asked)
	}
}

func TestUsersRemoveInvitation(t *testing.T) {
	k := kit(t)
	asked := k.Elicit(true)
	k.ASC.Handle(http.MethodGet, "/v1/users", http.StatusOK, emptyDoc)
	k.ASC.Handle(http.MethodGet, "/v1/userInvitations", http.StatusOK, invitesDoc)
	k.ASC.Handle(http.MethodDelete, "/v1/userInvitations/I1", http.StatusNoContent, ``)
	res := tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "users_remove"), k.Env, `{"user":"new@example.com","confirm":true}`))
	wantLog(t, k, "GET /v1/users", "GET /v1/userInvitations", "DELETE /v1/userInvitations/I1")
	if m := tooltest.Decode[map[string]any](t, res); m["cancelled_invitation"] == nil || !strings.Contains((*asked)[0], "cancel the pending invitation of new@example.com") {
		t.Fatalf("out = %v prompt = %v", m, *asked)
	}
}

func TestUsersRemoveByIDFallsBackToInvitation(t *testing.T) {
	k := kit(t)
	k.Env.Profile.AllowNoElicit = true
	k.ASC.Handle(http.MethodGet, "/v1/users/I1", http.StatusNotFound,
		`{"errors":[{"status":"404","code":"NOT_FOUND","title":"The specified resource does not exist"}]}`)
	k.ASC.Handle(http.MethodGet, "/v1/userInvitations/I1", http.StatusOK,
		`{"data":{"type":"userInvitations","id":"I1","attributes":{"email":"new@example.com","roles":["MARKETING"]}}}`)
	k.ASC.Handle(http.MethodDelete, "/v1/userInvitations/I1", http.StatusNoContent, ``)
	tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "users_remove"), k.Env, `{"user":"I1","confirm":true}`))
	wantLog(t, k, "GET /v1/users/I1", "GET /v1/userInvitations/I1", "DELETE /v1/userInvitations/I1")
}

func TestUsersRemoveRefusals(t *testing.T) {
	k := kit(t)
	k.Elicit(true)
	k.ASC.Handle(http.MethodGet, "/v1/users", http.StatusOK, usersDoc)
	tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "users_remove"), k.Env, `{"user":"ivan@example.com","confirm":true}`), tool.ErrInvalidArgs.Code)
	k.ASC.Handle(http.MethodGet, "/v1/userInvitations", http.StatusOK, emptyDoc)
	tooltest.MustFail(t, tooltest.Call(t, lookupTool(t, "users_remove"), k.Env, `{"user":"ghost@example.com","confirm":true}`), "not_found")
	// Preview without confirm: nothing written.
	res := tooltest.MustOK(t, tooltest.Call(t, lookupTool(t, "users_remove"), k.Env, `{"user":"dev@example.com"}`))
	if tooltest.Decode[map[string]any](t, res)["status"] != tool.ErrNeedsConfirm.Code || len(k.ASC.Mutations()) != 0 {
		t.Fatalf("preview = %s", tooltest.JSON(t, res.Data))
	}
}
