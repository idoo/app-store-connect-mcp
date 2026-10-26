package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// roles is UserRole of App Store Connect spec 4.5.1.
var roles = []any{
	"ADMIN", "FINANCE", "ACCOUNT_HOLDER", "SALES", "MARKETING", "APP_MANAGER", "DEVELOPER", "ACCESS_TO_REPORTS",
	"CUSTOMER_SUPPORT", "CREATE_APPS", "CLOUD_MANAGED_DEVELOPER_ID", "CLOUD_MANAGED_APP_DISTRIBUTION",
	"GENERATE_INDIVIDUAL_KEYS",
}

// visibleAppsLimit is the spec maximum of limit[visibleApps].
const visibleAppsLimit = 50

var (
	userFields       = []string{"username", "firstName", "lastName", "roles", "allAppsVisible", "provisioningAllowed", "visibleApps"}
	invitationFields = []string{"email", "firstName", "lastName", "expirationDate", "roles", "allAppsVisible", "provisioningAllowed", "visibleApps"}
	appFields        = []string{"name", "bundleId"}
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "users_list", Toolset: tool.Admin, Class: tool.Read, Idempotent: true,
		Title: "List team users and invitations",
		Description: "Team members and pending invitations with roles, all-apps access, visible app names and invitation " +
			"expiry. Filter by role or a name/email substring. Needs an Admin key.",
		Enums: map[string][]any{"role": roles},
	}, list))
	tool.Register(tool.New(tool.Meta{
		Name: "users_invite", Toolset: tool.Admin, Class: tool.Destructive, Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Invite a team user",
		Description: "Invite someone to the App Store Connect team with roles and either all apps or a list of apps. " +
			"Apple emails them immediately. An existing user or pending invite is reported, not re-sent. " +
			"Needs confirm:true and human approval.",
		Enums: map[string][]any{"roles": roles},
	}, invite))
	tool.Register(tool.New(tool.Meta{
		Name: "users_remove", Toolset: tool.Admin, Class: tool.Destructive, Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Remove a user or cancel an invitation",
		Description: "Remove a team member (by email or user id) or cancel their pending invitation; access ends " +
			"immediately. Needs confirm:true and human approval.",
	}, remove))
}

// appOut is an app a member can see.
type appOut struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	BundleID string `json:"bundle_id,omitempty"`
}

// member is a user or a pending invitation.
type member struct {
	ID                  string   `json:"id"`
	Email               string   `json:"email"`
	FirstName           string   `json:"first_name,omitempty"`
	LastName            string   `json:"last_name,omitempty"`
	Roles               []string `json:"roles"`
	AllApps             bool     `json:"all_apps"`
	ProvisioningAllowed bool     `json:"provisioning_allowed,omitempty"`
	Apps                []appOut `json:"apps,omitempty"`
	AppsTruncated       bool     `json:"apps_truncated,omitempty"`
	Expires             string   `json:"expires,omitempty"`
	// Pending marks an invitation not yet accepted.
	Pending bool `json:"pending,omitempty"`
}

func strs(v any) []string {
	out := []string{}
	if a, ok := v.([]any); ok {
		for _, e := range a {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// toMember flattens a users or userInvitations resource; apps maps included
// app ids to their names.
func toMember(r tool.Resource, apps map[string]appOut) member {
	m := member{
		ID: r.ID, Email: r.Str("username"), FirstName: r.Str("firstName"), LastName: r.Str("lastName"),
		Roles: strs(r.Attributes["roles"]), Expires: r.Str("expirationDate"),
	}
	if r.Type == "userInvitations" {
		m.Email, m.Pending = r.Str("email"), true
	}
	m.AllApps, _ = r.Bool("allAppsVisible")
	m.ProvisioningAllowed, _ = r.Bool("provisioningAllowed")
	if !m.AllApps {
		ids := r.RelIDs("visibleApps")
		for _, id := range ids {
			a, ok := apps[id]
			if !ok {
				a = appOut{ID: id}
			}
			m.Apps = append(m.Apps, a)
		}
		m.AppsTruncated = len(ids) >= visibleAppsLimit
	}
	return m
}

// listMembers lists users or invitations with their visible apps.
func listMembers(ctx context.Context, env *tool.Env, path, typ string, fields []string, q url.Values) ([]member, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("limit[visibleApps]", fmt.Sprint(visibleAppsLimit))
	res, err := env.ASC.List(ctx, path, tool.ListOptions{
		Query:    q,
		Fields:   map[string][]string{typ: fields, "apps": appFields},
		Include:  []string{"visibleApps"},
		MaxItems: 1000,
	})
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", typ, err)
	}
	apps := map[string]appOut{}
	for _, r := range res.Included {
		if r.Type == "apps" {
			apps[r.ID] = appOut{ID: r.ID, Name: r.Str("name"), BundleID: r.Str("bundleId")}
		}
	}
	out := make([]member, 0, len(res.Data))
	for _, r := range res.Data {
		out = append(out, toMember(r, apps))
	}
	return out, nil
}

type listIn struct {
	Role         string `json:"role,omitempty" jsonschema:"only members with this role"`
	Query        string `json:"query,omitempty" jsonschema:"case-insensitive substring of email or name"`
	NoInvitation bool   `json:"no_invitations,omitempty" jsonschema:"skip pending invitations"`
}

type listOut struct {
	Users       []member `json:"users"`
	Invitations []member `json:"invitations,omitempty"`
}

func matches(m member, q string) bool {
	q = strings.ToLower(q)
	return q == "" || strings.Contains(strings.ToLower(m.Email+" "+m.FirstName+" "+m.LastName), q)
}

func list(ctx context.Context, env *tool.Env, in listIn) (*tool.Result, error) {
	q := func() url.Values {
		if in.Role == "" {
			return nil
		}
		return url.Values{"filter[roles]": {in.Role}}
	}
	users, err := listMembers(ctx, env, "/v1/users", "users", userFields, q())
	if err != nil {
		return nil, err
	}
	out := listOut{Users: slices.DeleteFunc(users, func(m member) bool { return !matches(m, in.Query) })}
	if !in.NoInvitation {
		inv, err := listMembers(ctx, env, "/v1/userInvitations", "userInvitations", invitationFields, q())
		if err != nil {
			return nil, err
		}
		out.Invitations = slices.DeleteFunc(inv, func(m member) bool { return !matches(m, in.Query) })
	}
	return tool.OK(out), nil
}

type inviteIn struct {
	tool.ConfirmFlag
	Email               string        `json:"email" jsonschema:"invitee email"`
	FirstName           string        `json:"first_name"`
	LastName            string        `json:"last_name"`
	Roles               []string      `json:"roles" jsonschema:"one or more roles"`
	AllApps             bool          `json:"all_apps,omitempty" jsonschema:"access to every app, including future ones"`
	Apps                []tool.AppRef `json:"apps,omitempty" jsonschema:"apps the user can see (id, bundle id, SKU or name) when all_apps is false"`
	ProvisioningAllowed bool          `json:"provisioning_allowed,omitempty" jsonschema:"allow access to certificates, identifiers and profiles"`
}

// findMember returns the user or pending invitation for email, if any.
func findMember(ctx context.Context, env *tool.Env, email string) (*member, error) {
	users, err := listMembers(ctx, env, "/v1/users", "users", userFields, url.Values{"filter[username]": {email}})
	if err != nil {
		return nil, err
	}
	for _, m := range users {
		if strings.EqualFold(m.Email, email) {
			return &m, nil
		}
	}
	inv, err := listMembers(ctx, env, "/v1/userInvitations", "userInvitations", invitationFields, url.Values{"filter[email]": {email}})
	if err != nil {
		return nil, err
	}
	for _, m := range inv {
		if strings.EqualFold(m.Email, email) {
			return &m, nil
		}
	}
	return nil, nil
}

func invite(ctx context.Context, env *tool.Env, in inviteIn) (*tool.Result, error) {
	email := strings.TrimSpace(in.Email)
	at := strings.LastIndex(email, "@")
	switch {
	case at < 1 || at == len(email)-1 || strings.ContainsAny(email, " ,;<>"):
		return nil, tool.InvalidArgs("email %q is not an email address", in.Email)
	case strings.TrimSpace(in.FirstName) == "" || strings.TrimSpace(in.LastName) == "":
		return nil, tool.InvalidArgs("first_name and last_name are required")
	case len(in.Roles) == 0:
		return nil, tool.InvalidArgs("at least one role is required")
	case slices.Contains(in.Roles, "ACCOUNT_HOLDER"):
		return nil, tool.InvalidArgs("ACCOUNT_HOLDER cannot be invited; transfer it in App Store Connect")
	case in.AllApps && len(in.Apps) > 0:
		return nil, tool.InvalidArgs("set either all_apps or apps, not both")
	case !in.AllApps && len(in.Apps) == 0:
		return nil, tool.InvalidArgs("set all_apps:true or list apps")
	}
	rs := slices.Clone(in.Roles)
	slices.Sort(rs)
	rs = slices.Compact(rs)
	var apps []tool.App
	appLabels := []string{"all apps"}
	if !in.AllApps {
		appLabels = nil
		for _, ref := range in.Apps {
			a, err := env.Resolve.App(ctx, ref)
			if err != nil {
				return nil, err
			}
			if !slices.ContainsFunc(apps, func(x tool.App) bool { return x.ID == a.ID }) {
				apps = append(apps, a)
				appLabels = append(appLabels, a.Name+" ("+a.ID+")")
			}
		}
	}
	existing, err := findMember(ctx, env, email)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		kind := "user"
		if existing.Pending {
			kind = "invitation"
		}
		return tool.Already(map[string]any{kind: existing}).
			Then(tool.NextStep{Tool: "users_list", Args: map[string]any{"query": email}}), nil
	}
	intended := map[string]any{
		"email": email, "first_name": in.FirstName, "last_name": in.LastName, "roles": rs,
		"apps": appLabels, "provisioning_allowed": in.ProvisioningAllowed,
	}
	wctx, res := tool.RequireConfirm(ctx, env, in.Confirm, tool.PlanPreview{
		Summary:      fmt.Sprintf("invite %s as %s with access to %s", email, strings.Join(rs, ", "), strings.Join(appLabels, ", ")),
		Intended:     intended,
		Affected:     []string{email},
		Irreversible: []string{"Apple emails the invitation to " + email + " immediately"},
	})
	if res != nil {
		return res, nil
	}
	data := map[string]any{
		"type": "userInvitations",
		"attributes": map[string]any{
			"email": email, "firstName": in.FirstName, "lastName": in.LastName, "roles": rs,
			"allAppsVisible": in.AllApps, "provisioningAllowed": in.ProvisioningAllowed,
		},
	}
	if len(apps) > 0 {
		refs := make([]map[string]any, len(apps))
		for i, a := range apps {
			refs[i] = map[string]any{"type": "apps", "id": a.ID}
		}
		data["relationships"] = map[string]any{"visibleApps": map[string]any{"data": refs}}
	}
	resp, err := env.ASC.Do(wctx, tool.Request{Method: http.MethodPost, Path: "/v1/userInvitations", Write: true, Body: map[string]any{"data": data}})
	if err != nil {
		if tool.APIStatus(err) == http.StatusConflict {
			if m, rerr := findMember(ctx, env, email); rerr == nil && m != nil {
				return tool.Already(map[string]any{"invitation": m}), nil
			}
		}
		return nil, fmt.Errorf("invite %s: %w", email, err)
	}
	r, ok := resp.First()
	if !ok {
		return nil, fmt.Errorf("invite %s: %w: empty response", email, tool.ErrOutcomeUnknown)
	}
	inv := toMember(r, nil)
	inv.Apps = nil
	for _, a := range apps {
		inv.Apps = append(inv.Apps, appOut{ID: a.ID, Name: a.Name, BundleID: a.BundleID})
	}
	return tool.OK(map[string]any{"invited": inv}), nil
}

type removeIn struct {
	tool.ConfirmFlag
	User string `json:"user" jsonschema:"email, user id or invitation id"`
}

// lookup finds a user or invitation by email or id; invitation reports which.
func lookup(ctx context.Context, env *tool.Env, ref string) (m member, invitation bool, err error) {
	if strings.Contains(ref, "@") {
		found, err := findMember(ctx, env, ref)
		if err != nil {
			return member{}, false, err
		}
		if found == nil {
			return member{}, false, &tool.ErrNotFound{Kind: "user", Ref: ref, Next: []tool.NextStep{{Tool: "users_list"}}}
		}
		return *found, found.Pending, nil
	}
	q := url.Values{"fields[users]": {strings.Join(userFields[:6], ",")}}
	res, err := env.ASC.Get(ctx, "/v1/users/"+url.PathEscape(ref), q)
	if err == nil {
		if r, ok := res.First(); ok {
			return toMember(r, nil), false, nil
		}
	} else if tool.APIStatus(err) != http.StatusNotFound {
		return member{}, false, fmt.Errorf("get user %s: %w", ref, err)
	}
	q = url.Values{"fields[userInvitations]": {strings.Join(invitationFields[:7], ",")}}
	res, err = env.ASC.Get(ctx, "/v1/userInvitations/"+url.PathEscape(ref), q)
	if err == nil {
		if r, ok := res.First(); ok {
			return toMember(r, nil), true, nil
		}
	} else if tool.APIStatus(err) != http.StatusNotFound {
		return member{}, false, fmt.Errorf("get invitation %s: %w", ref, err)
	}
	return member{}, false, &tool.ErrNotFound{Kind: "user", Ref: ref, Next: []tool.NextStep{{Tool: "users_list"}}}
}

func remove(ctx context.Context, env *tool.Env, in removeIn) (*tool.Result, error) {
	m, isInv, err := lookup(ctx, env, strings.TrimSpace(in.User))
	if err != nil {
		return nil, err
	}
	if slices.Contains(m.Roles, "ACCOUNT_HOLDER") {
		return nil, tool.InvalidArgs("the Account Holder cannot be removed")
	}
	p := tool.PlanPreview{
		Summary:      fmt.Sprintf("remove %s (%s) from the team", m.Email, strings.Join(m.Roles, ", ")),
		Current:      m,
		Affected:     []string{m.Email},
		Irreversible: []string{m.Email + " loses App Store Connect access immediately; re-adding needs a new invitation"},
	}
	path := "/v1/users/" + m.ID
	if isInv {
		p.Summary = fmt.Sprintf("cancel the pending invitation of %s (%s)", m.Email, strings.Join(m.Roles, ", "))
		p.Irreversible = []string{"the invitation link stops working"}
		path = "/v1/userInvitations/" + m.ID
	}
	wctx, res := tool.RequireConfirm(ctx, env, in.Confirm, p)
	if res != nil {
		return res, nil
	}
	if _, err := env.ASC.Do(wctx, tool.Request{Method: http.MethodDelete, Path: path, Write: true}); err != nil {
		if tool.APIStatus(err) == http.StatusNotFound {
			return tool.Already(map[string]any{"removed": m.Email}), nil
		}
		return nil, fmt.Errorf("remove %s: %w", m.Email, err)
	}
	key := "removed_user"
	if isInv {
		key = "cancelled_invitation"
	}
	return tool.OK(map[string]any{key: map[string]any{"id": m.ID, "email": m.Email}}), nil
}
