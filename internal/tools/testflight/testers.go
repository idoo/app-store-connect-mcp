package testflight

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// Per-tester outcomes of testflight_testers_add.
const (
	outcomeAdded   = "added"
	outcomeAlready = "already"
	outcomeError   = "error"
	outcomeUnknown = "unknown"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "testflight_testers_list", Toolset: tool.TestFlight, Class: tool.Read, Idempotent: true,
		Title: "List TestFlight testers",
		Description: "Beta testers of an app or group, filterable by email, state or invite type, with their groups. " +
			"Names/emails are tester-provided data.",
		Enums: map[string][]any{
			"state":       {"NOT_INVITED", "INVITED", "ACCEPTED", "INSTALLED", "REVOKED"},
			"invite_type": {"EMAIL", "PUBLIC_LINK"},
		},
	}, testersList))
	tool.Register(tool.New(tool.Meta{
		Name: "testflight_testers_add", Toolset: tool.TestFlight, Class: tool.Write, Idempotent: true,
		Confirm: tool.ConfirmByFlag,
		Title:   "Add TestFlight testers",
		Description: "Add or invite up to 100 testers to a beta group; existing members are reported as already. External " +
			"groups need confirm:true and human approval after a preview.",
	}, testersAdd))
	tool.Register(tool.New(tool.Meta{
		Name: "testflight_testers_remove", Toolset: tool.TestFlight, Class: tool.Destructive,
		Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Remove TestFlight testers",
		Description: "Remove up to 100 testers from a beta group, or with scope app from every group and build. Without " +
			"confirm:true previews the matched testers; nothing changes.",
		Enums: map[string][]any{"scope": {"group", "app"}},
	}, testersRemove))
}

// --- testflight_testers_list ---

type testersListIn struct {
	App        tool.AppRef   `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Group      tool.GroupRef `json:"group,omitempty" jsonschema:"beta group name or id; default: all"`
	Email      string        `json:"email,omitempty" jsonschema:"exact tester email"`
	State      string        `json:"state,omitempty" jsonschema:"filters the fetched page"`
	InviteType string        `json:"invite_type,omitempty"`
	MaxItems   int           `json:"max_items,omitempty" jsonschema:"testers to fetch, default 200, max 1000"`
	Cursor     string        `json:"cursor,omitempty" jsonschema:"next_cursor of a previous call"`
}

func testersList(ctx context.Context, env *tool.Env, in testersListIn) (*tool.Result, error) {
	switch {
	case in.MaxItems < 0 || in.MaxItems > 1000:
		return nil, invalidArgs("max_items must be between 1 and 1000")
	case in.MaxItems == 0:
		in.MaxItems = 200
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"sort":               {"email"},
		"include":            {"betaGroups"},
		"fields[betaGroups]": {"name"},
		"limit[betaGroups]":  {"50"},
	}
	out := map[string]any{"app": appOut(app)}
	if in.Group != "" {
		g, err := env.Resolve.Group(ctx, app.ID, in.Group)
		if err != nil {
			return nil, err
		}
		q.Set("filter[betaGroups]", g.ID)
		out["group"] = g
	} else {
		q.Set("filter[apps]", app.ID)
	}
	if e := strings.TrimSpace(in.Email); e != "" {
		q.Set("filter[email]", e)
	}
	if in.InviteType != "" {
		q.Set("filter[inviteType]", in.InviteType)
	}
	names, err := appGroupNames(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	list, err := env.ASC.List(ctx, "/v1/betaTesters", tool.ListOptions{
		Query:    q,
		Fields:   map[string][]string{"betaTesters": append(append([]string{}, testerFields...), "betaGroups")},
		Limit:    min(in.MaxItems, 200),
		MaxItems: in.MaxItems,
		Cursor:   in.Cursor,
	})
	if err != nil {
		return nil, fmt.Errorf("list beta testers: %w", err)
	}
	testers := make([]testerOut, 0, len(list.Data))
	for _, r := range list.Data {
		if in.State != "" && r.Str("state") != in.State {
			continue
		}
		testers = append(testers, testerFromResource(r, names))
	}
	out["testers"] = testers
	out["returned"] = len(testers)
	if list.Total > 0 {
		out["total"] = list.Total
	}
	if list.NextCursor != "" {
		out["next_cursor"] = list.NextCursor
	}
	return tool.OK(out), nil
}

// --- testflight_testers_add ---

type testerInput struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

type testersAddIn struct {
	App     tool.AppRef   `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Group   tool.GroupRef `json:"group" jsonschema:"beta group name or id"`
	Testers []testerInput `json:"testers" jsonschema:"1-100 testers to add or invite"`
	tool.ConfirmFlag
}

// addOutcome is the per-email result of testflight_testers_add.
type addOutcome struct {
	Email   string `json:"email"`
	Outcome string `json:"outcome"`
	ID      string `json:"id,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

func testersAdd(ctx context.Context, env *tool.Env, in testersAddIn) (*tool.Result, error) {
	if len(in.Testers) == 0 || len(in.Testers) > maxTesterBatch {
		return nil, invalidArgs("testers must list 1-%d entries", maxTesterBatch)
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	g, err := env.Resolve.Group(ctx, app.ID, in.Group)
	if err != nil {
		return nil, err
	}

	// Normalize and dedupe; invalid emails fail individually.
	outcomes := make([]addOutcome, 0, len(in.Testers))
	pos := map[string]int{} // emailKey → index in outcomes
	var wanted []testerInput
	for _, t := range in.Testers {
		t.Email = strings.TrimSpace(t.Email)
		key := emailKey(t.Email)
		if _, dup := pos[key]; dup {
			continue
		}
		pos[key] = len(outcomes)
		if !validEmail(t.Email) {
			outcomes = append(outcomes, addOutcome{Email: t.Email, Outcome: outcomeError, Detail: "not a valid email address"})
			continue
		}
		outcomes = append(outcomes, addOutcome{Email: t.Email})
		wanted = append(wanted, t)
	}
	set := func(email, outcome, id, detail string) {
		o := &outcomes[pos[emailKey(email)]]
		o.Outcome, o.ID, o.Detail = outcome, id, detail
	}
	emails := func(ts []testerInput) []string {
		s := make([]string, 0, len(ts))
		for _, t := range ts {
			s = append(s, t.Email)
		}
		return s
	}
	inGroup := url.Values{"filter[betaGroups]": {g.ID}}

	// 1. Already members of the group.
	members, err := findTesters(ctx, env, inGroup, "filter[email]", emails(wanted))
	if err != nil {
		return nil, err
	}
	memberBy := byEmail(members)
	var rest []testerInput
	for _, t := range wanted {
		if m, ok := memberBy[emailKey(t.Email)]; ok {
			set(t.Email, outcomeAlready, m.ID, "")
			continue
		}
		rest = append(rest, t)
	}
	if len(rest) > 0 {
		var gres *tool.Result
		if ctx, gres = gateTestersAdd(ctx, env, app, g, emails(rest), in.Confirm); gres != nil {
			return gres, nil
		}
	}

	// 2. Existing team testers are linked in one call; the others are created.
	existing, err := findTesters(ctx, env, nil, "filter[email]", emails(rest))
	if err != nil {
		return nil, err
	}
	existBy := byEmail(existing)
	var link []testerInput
	var create []testerInput
	for _, t := range rest {
		if _, ok := existBy[emailKey(t.Email)]; ok {
			link = append(link, t)
		} else {
			create = append(create, t)
		}
	}
	linkTesters(ctx, env, g.ID, link, existBy, set)
	for _, t := range create {
		createTester(ctx, env, g.ID, t, set)
	}

	counts := map[string]int{}
	for _, o := range outcomes {
		counts[o.Outcome]++
	}
	out := map[string]any{
		"app": appOut(app), "group": g, "testers": outcomes,
		outcomeAdded: counts[outcomeAdded], outcomeAlready: counts[outcomeAlready],
		"failed": counts[outcomeError] + counts[outcomeUnknown],
	}
	if counts[outcomeAlready] == len(outcomes) {
		return tool.Already(out), nil
	}
	res := tool.OK(out)
	if g.Internal {
		res.Warn("internal groups accept only App Store Connect users of the team; other emails fail")
	}
	if counts[outcomeUnknown] > 0 {
		res.Warn("some requests have an unknown outcome; check with testflight_testers_list before retrying")
		res.Then(tool.NextStep{Tool: "testflight_testers_list", Args: map[string]any{"app": app.ID, "group": g.ID}})
	}
	return res, nil
}

// gateTestersAdd lets additions to internal groups (team members only)
// through unconfirmed; inviting outsiders to an external group gives them the
// app's beta builds, so it needs confirm:true and a human.
func gateTestersAdd(ctx context.Context, env *tool.Env, app tool.App, g tool.Group, emails []string, confirm bool) (context.Context, *tool.Result) {
	if g.Internal {
		return tool.Unconfirmed(ctx, env)
	}
	return tool.RequireHumanConfirm(ctx, env, confirm, tool.PlanPreview{
		Summary:      fmt.Sprintf("add or invite %d tester(s) to external beta group %s of app %s", len(emails), g.Name, app.ID),
		Affected:     append([]string{"group " + g.Name + " (" + g.ID + "), external"}, emails...),
		Intended:     map[string]any{"group": g, "invite": emails},
		Irreversible: []string{"invitation emails are sent; invitees can install the group's approved builds"},
	})
}

type setOutcome func(email, outcome, id, detail string)

// linkTesters adds existing team testers to the group in one request. A 409
// is resolved by reading the membership back.
func linkTesters(ctx context.Context, env *tool.Env, groupID string, ts []testerInput, existBy map[string]tool.Resource, set setOutcome) {
	if len(ts) == 0 {
		return
	}
	ids := make([]string, 0, len(ts))
	for _, t := range ts {
		ids = append(ids, existBy[emailKey(t.Email)].ID)
	}
	_, err := env.ASC.Do(ctx, tool.Request{
		Method: http.MethodPost, Path: "/v1/betaGroups/" + groupID + "/relationships/betaTesters",
		Body: testerLinkage(ids), Write: true,
	})
	if err == nil {
		for i, t := range ts {
			set(t.Email, outcomeAdded, ids[i], "existing tester linked to the group")
		}
		return
	}
	if !isConflict(err) {
		failAll(ts, err, set)
		return
	}
	readBackMembers(ctx, env, groupID, ts, err, set)
}

// createTester invites one new tester into the group. A 409 (the tester was
// created meanwhile, or exists under another app) is resolved by read-back:
// a member is already added; an existing non-member is linked.
func createTester(ctx context.Context, env *tool.Env, groupID string, t testerInput, set setOutcome) {
	attrs := map[string]any{"email": t.Email}
	if s := strings.TrimSpace(t.FirstName); s != "" {
		attrs["firstName"] = s
	}
	if s := strings.TrimSpace(t.LastName); s != "" {
		attrs["lastName"] = s
	}
	body := map[string]any{"data": map[string]any{
		"type":       "betaTesters",
		"attributes": attrs,
		"relationships": map[string]any{"betaGroups": map[string]any{
			"data": []map[string]string{{"type": "betaGroups", "id": groupID}},
		}},
	}}
	resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: "/v1/betaTesters", Body: body, Write: true})
	if err == nil {
		id := ""
		if r, ok := resp.First(); ok {
			id = r.ID
		}
		set(t.Email, outcomeAdded, id, "invited")
		return
	}
	if !isConflict(err) {
		failAll([]testerInput{t}, err, set)
		return
	}
	members, rerr := findTesters(ctx, env, url.Values{"filter[betaGroups]": {groupID}}, "filter[email]", []string{t.Email})
	if rerr != nil {
		set(t.Email, outcomeUnknown, "", errText(err)+"; read-back failed: "+errText(rerr))
		return
	}
	if m, ok := byEmail(members)[emailKey(t.Email)]; ok {
		set(t.Email, outcomeAlready, m.ID, "")
		return
	}
	existing, rerr := findTesters(ctx, env, nil, "filter[email]", []string{t.Email})
	if rerr != nil {
		set(t.Email, outcomeUnknown, "", errText(err)+"; read-back failed: "+errText(rerr))
		return
	}
	existBy := byEmail(existing)
	if _, ok := existBy[emailKey(t.Email)]; ok {
		linkTesters(ctx, env, groupID, []testerInput{t}, existBy, set)
		return
	}
	set(t.Email, outcomeError, "", errText(err))
}

// readBackMembers settles a conflicting link request by membership read-back.
func readBackMembers(ctx context.Context, env *tool.Env, groupID string, ts []testerInput, cause error, set setOutcome) {
	es := make([]string, 0, len(ts))
	for _, t := range ts {
		es = append(es, t.Email)
	}
	members, err := findTesters(ctx, env, url.Values{"filter[betaGroups]": {groupID}}, "filter[email]", es)
	if err != nil {
		for _, t := range ts {
			set(t.Email, outcomeUnknown, "", errText(cause)+"; read-back failed: "+errText(err))
		}
		return
	}
	by := byEmail(members)
	for _, t := range ts {
		if m, ok := by[emailKey(t.Email)]; ok {
			set(t.Email, outcomeAlready, m.ID, "")
		} else {
			set(t.Email, outcomeError, "", errText(cause))
		}
	}
}

// failAll records err for every tester; an unknown-outcome error (timeout or
// 5xx on a mutation) is reported as such, never as a failure.
func failAll(ts []testerInput, err error, set setOutcome) {
	outcome := outcomeError
	if errors.Is(err, tool.ErrOutcomeUnknown) {
		outcome = outcomeUnknown
	}
	for _, t := range ts {
		set(t.Email, outcome, "", errText(err))
	}
}

// --- testflight_testers_remove ---

type testersRemoveIn struct {
	tool.ConfirmFlag
	App     tool.AppRef   `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Scope   string        `json:"scope,omitempty" jsonschema:"group (default) or app: every group and build"`
	Group   tool.GroupRef `json:"group,omitempty" jsonschema:"beta group name or id; required for scope group"`
	Testers []string      `json:"testers" jsonschema:"1-100 tester emails or ids"`
}

// removeTarget is a matched tester in the remove preview.
type removeTarget struct {
	Ref   string         `json:"ref"`
	ID    string         `json:"id"`
	Email tool.Untrusted `json:"email,omitempty"`
	State string         `json:"state,omitempty"`
}

func testersRemove(ctx context.Context, env *tool.Env, in testersRemoveIn) (*tool.Result, error) {
	if len(in.Testers) == 0 || len(in.Testers) > maxTesterBatch {
		return nil, invalidArgs("testers must list 1-%d emails or ids", maxTesterBatch)
	}
	scope := in.Scope
	if scope == "" {
		scope = "group"
	}
	if scope == "group" && in.Group == "" {
		return nil, invalidArgs("group is required for scope group (or pass scope app)")
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"app": appOut(app), "scope": scope}
	filter := url.Values{"filter[apps]": {app.ID}}
	path := "/v1/apps/" + app.ID + "/relationships/betaTesters"
	var g tool.Group
	if scope == "group" {
		if g, err = env.Resolve.Group(ctx, app.ID, in.Group); err != nil {
			return nil, err
		}
		out["group"] = g
		filter = url.Values{"filter[betaGroups]": {g.ID}}
		path = "/v1/betaGroups/" + g.ID + "/relationships/betaTesters"
	}

	targets, notFound, err := matchTesters(ctx, env, filter, in.Testers)
	if err != nil {
		return nil, err
	}
	out["not_found"] = notFound
	if len(targets) == 0 {
		out["removed"] = []string{}
		return tool.OK(out).Warn("none of the testers is a member; nothing to remove"), nil
	}

	affected := make([]string, 0, len(targets))
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		affected = append(affected, t.Ref)
		ids = append(ids, t.ID)
	}
	p := tool.PlanPreview{
		Current:  map[string]any{"members": targets, "not_found": notFound},
		Affected: affected,
	}
	if scope == "group" {
		p.Summary = fmt.Sprintf("remove %d tester(s) from beta group %q of %s", len(targets), g.Name, app.Name)
		p.Intended = "testers leave the group; their other groups are unchanged"
		p.Irreversible = []string{"testers lose access to builds shared only through this group; re-adding sends a new invitation"}
	} else {
		p.Summary = fmt.Sprintf("remove %d tester(s) from every beta group and build of %s", len(targets), app.Name)
		p.Intended = "testers lose all TestFlight access to the app"
		p.Irreversible = []string{"testers lose access to every build of the app immediately; re-adding needs a new invitation"}
	}
	wctx, res := tool.RequireConfirm(ctx, env, in.Confirm, p)
	if res != nil {
		return res, nil
	}
	if _, err := env.ASC.Do(wctx, tool.Request{Method: http.MethodDelete, Path: path, Body: testerLinkage(ids), Write: true}); err != nil {
		return nil, fmt.Errorf("remove beta testers: %w", err)
	}
	out["removed"] = affected
	return tool.OK(out), nil
}

// matchTesters finds the refs (emails or ids) among the testers in filter's
// scope: at most one GET for emails and one for ids.
func matchTesters(ctx context.Context, env *tool.Env, filter url.Values, refs []string) ([]removeTarget, []string, error) {
	var emails, ids []string
	seen := map[string]bool{}
	for _, r := range refs {
		r = strings.TrimSpace(r)
		key := emailKey(r)
		if r == "" || seen[key] {
			continue
		}
		seen[key] = true
		if strings.Contains(r, "@") {
			emails = append(emails, r)
		} else {
			ids = append(ids, r)
		}
	}
	byMail, err := findTesters(ctx, env, filter, "filter[email]", emails)
	if err != nil {
		return nil, nil, err
	}
	byID, err := findTesters(ctx, env, filter, "filter[id]", ids)
	if err != nil {
		return nil, nil, err
	}
	mailIdx := byEmail(byMail)
	idIdx := make(map[string]tool.Resource, len(byID))
	for _, r := range byID {
		idIdx[r.ID] = r
	}
	var targets []removeTarget
	notFound := []string{}
	add := func(ref string, r tool.Resource, ok bool) {
		if !ok {
			notFound = append(notFound, ref)
			return
		}
		targets = append(targets, removeTarget{Ref: ref, ID: r.ID, Email: tool.Untrusted(r.Str("email")), State: r.Str("state")})
	}
	for _, e := range emails {
		r, ok := mailIdx[emailKey(e)]
		add(e, r, ok)
	}
	for _, id := range ids {
		r, ok := idIdx[id]
		add(id, r, ok)
	}
	return targets, notFound, nil
}
