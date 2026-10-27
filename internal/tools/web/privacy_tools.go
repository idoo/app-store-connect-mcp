package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "privacy_get", Toolset: tool.Web, Class: tool.Read, Idempotent: true,
		Title: "Read the App Privacy declaration",
		Description: "The app's App Privacy answers (web session): per data category its purposes, linked / not linked, " +
			"tracking, or data not collected; whether they are published. catalog:true lists valid tokens.",
	}, privacyGet))
	tool.Register(tool.New(tool.Meta{
		Name: "privacy_plan", Toolset: tool.Web, Class: tool.Read, Idempotent: true,
		Title: "Plan App Privacy changes",
		Description: "Diff a FULL App Privacy declaration (not_collected or data[]) against the app's: anything not listed " +
			"is removed. Validates tokens against Apple's catalog; returns ordered steps and a plan_id for privacy_apply.",
	}, privacyPlan))
	tool.Register(tool.New(tool.Meta{
		Name: "privacy_apply", Toolset: tool.Web, Class: tool.Destructive,
		Confirm: tool.ConfirmByPlan, RequireHuman: true,
		Title: "Apply and publish App Privacy",
		Description: "Apply a privacy_plan: same arguments plus plan_id; refused if the answers changed since. Creates, " +
			"updates and deletes data usages, then publishes (public on the product page). Needs human approval.",
	}, privacyApply))
}

type privacyGetIn struct {
	App     tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Catalog bool        `json:"catalog,omitempty" jsonschema:"also list valid category, purpose and data protection tokens"`
}

type appRef struct {
	ID       string         `json:"id"`
	Name     tool.Untrusted `json:"name"`
	BundleID string         `json:"bundle_id"`
}

func newAppRef(a tool.App) appRef {
	return appRef{ID: a.ID, Name: tool.Untrusted(a.Name), BundleID: a.BundleID}
}

// catalogOut lists the live (non-retired) tokens.
type catalogOut struct {
	Categories  []catalogEntry `json:"categories"`
	Purposes    []string       `json:"purposes"`
	Protections []string       `json:"data_protections"`
}

type privacyGetOut struct {
	App          appRef       `json:"app"`
	Published    *bool        `json:"published"`
	State        string       `json:"state"`
	Declaration  declaration  `json:"declaration"`
	Summary      []string     `json:"summary"`
	Unrecognized []string     `json:"unrecognized,omitempty"`
	Catalog      *catalogOut  `json:"catalog,omitempty"`
	Publish      publishState `json:"publish_state"`
}

// privacyStateName names the declaration's state for agents.
func privacyStateName(n int, p *bool) string {
	switch {
	case n == 0:
		return "not_started"
	case p == nil:
		return "unknown"
	case *p:
		return "published"
	}
	return "unpublished"
}

// summaryLines renders the declaration as one human line per category.
func summaryLines(d declaration) []string {
	if d.NotCollected {
		return []string{"Data Not Collected"}
	}
	var out []string
	for _, e := range d.Data {
		var b strings.Builder
		b.WriteString(e.Category)
		if len(e.Purposes) > 0 {
			link := "not linked to the user"
			if e.Linked {
				link = "linked to the user"
			}
			fmt.Fprintf(&b, ": %s; %s", strings.Join(e.Purposes, ", "), link)
		}
		if e.Tracking {
			b.WriteString("; used for tracking")
		}
		out = append(out, b.String())
	}
	if len(out) == 0 {
		return []string{"no answers yet"}
	}
	return out
}

func privacyGet(ctx context.Context, env *tool.Env, in privacyGetIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	us, err := readUsages(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	ps, err := readPublishState(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	ts := make([]tuple, len(us))
	for i, u := range us {
		ts[i] = u.tuple
	}
	d, unrec := declarationOf(ts)
	out := privacyGetOut{
		App: newAppRef(app), Published: ps.Published, State: privacyStateName(len(us), ps.Published),
		Declaration: d, Summary: summaryLines(d), Unrecognized: unrec, Publish: ps,
	}
	if in.Catalog {
		c, err := readCatalog(ctx, env)
		if err != nil {
			return nil, err
		}
		co := &catalogOut{Purposes: active(c.Purposes), Protections: active(c.Protections)}
		for _, e := range c.Categories {
			if !e.Deleted {
				co.Categories = append(co.Categories, e)
			}
		}
		out.Catalog = co
	}
	res := tool.OK(out)
	if out.State != "published" {
		res.Then(tool.NextStep{Tool: "privacy_plan", Args: map[string]any{"app": app.ID}})
	}
	return res, nil
}

// privacyPlanIn is the input of privacy_plan; privacy_apply adds plan_id.
type privacyPlanIn struct {
	App          tool.AppRef `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	NotCollected bool        `json:"not_collected,omitempty" jsonschema:"the app collects no data"`
	Data         []declEntry `json:"data,omitempty" jsonschema:"every collected category; omitted categories are removed"`
}

type privacyApplyIn struct {
	tool.ConfirmPlan
	privacyPlanIn
}

// Plan statuses.
const (
	statusPlanned   = "planned"
	statusNoChanges = "no_changes"
	statusInvalid   = "invalid"
)

type privacyPlanOut struct {
	Status    string       `json:"status"`
	App       appRef       `json:"app"`
	Published *bool        `json:"published"`
	Current   []string     `json:"current"`
	Desired   []string     `json:"desired"`
	Steps     []step       `json:"steps,omitempty"`
	Removals  []string     `json:"removals,omitempty"`
	Problems  []string     `json:"problems,omitempty"`
	PlanID    string       `json:"plan_id,omitempty"`
	Publish   publishState `json:"-"`
}

// privacyComputed is a plan computed from live state.
type privacyComputed struct {
	app         tool.App
	out         privacyPlanOut
	desired     []tuple
	fingerprint string
	key         privacyPlanIn
}

// computePrivacy reads the app's declaration, publish state and the
// catalog, validates in and diffs it into ordered steps.
func computePrivacy(ctx context.Context, env *tool.Env, in privacyPlanIn) (*privacyComputed, error) {
	desired, err := desiredTuples(declaration{NotCollected: in.NotCollected, Data: in.Data})
	if err != nil {
		return nil, err
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	us, err := readUsages(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	ps, err := readPublishState(ctx, env, app.ID)
	if err != nil {
		return nil, err
	}
	cat, err := readCatalog(ctx, env)
	if err != nil {
		return nil, err
	}
	cur := make([]tuple, len(us))
	for i, u := range us {
		cur[i] = u.tuple
	}
	cd, _ := declarationOf(cur)
	dd, _ := declarationOf(desired)
	p := &privacyComputed{app: app, desired: desired, key: in}
	p.key.App = tool.AppRef(app.ID)
	p.out = privacyPlanOut{App: newAppRef(app), Published: ps.Published, Current: summaryLines(cd), Desired: summaryLines(dd), Publish: ps}
	if len(us) == 0 {
		p.out.Current = []string{"no answers yet"}
	}
	if p.out.Problems = staleTokens(desired, cat); len(p.out.Problems) > 0 {
		p.out.Status = statusInvalid
		return p, nil
	}
	p.out.Steps = diffSteps(us, desired)
	for _, s := range p.out.Steps {
		if s.Op == opDelete {
			p.out.Removals = append(p.out.Removals, s.Usage)
		}
	}
	if len(p.out.Steps) > 0 || ps.Published == nil || !*ps.Published {
		p.out.Steps = append(p.out.Steps, step{Op: opPublish, Usage: "publish the App Privacy answers"})
	}
	if len(p.out.Steps) == 0 {
		p.out.Status = statusNoChanges
	} else {
		p.out.Status = statusPlanned
	}
	if p.fingerprint, err = tool.Fingerprint(map[string]any{"usages": us, "publish": ps}); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *privacyComputed) preview() tool.PlanPreview {
	var intended []string
	for _, s := range p.out.Steps {
		line := s.Op + " " + s.Usage
		if s.Op == opUpdate {
			line += " (was " + s.From + ")"
		}
		if s.Op == opPublish {
			line = s.Usage
		}
		intended = append(intended, line)
	}
	pv := tool.PlanPreview{
		Summary:  fmt.Sprintf("replace and publish the App Privacy answers of %s (%d steps)", p.app.Name, len(p.out.Steps)),
		Current:  p.out.Current,
		Intended: intended,
		Affected: []string{"app " + p.app.ID + " appDataUsages (web session)"},
		Irreversible: []string{
			"published App Privacy answers are public on the product page at once (edits to a published declaration go live immediately)",
		},
		Warnings: []string{"the declaration given is the complete one: anything not in it is removed"},
	}
	for _, r := range p.out.Removals {
		pv.Warnings = append(pv.Warnings, "removes "+r)
	}
	return pv
}

func privacyPlan(ctx context.Context, env *tool.Env, in privacyPlanIn) (*tool.Result, error) {
	p, err := computePrivacy(ctx, env, in)
	if err != nil {
		return nil, err
	}
	switch p.out.Status {
	case statusInvalid:
		return tool.OK(p.out).Warn("fix the tokens (privacy_get catalog:true lists valid ones) and plan again; no plan was issued"), nil
	case statusNoChanges:
		return tool.OK(p.out), nil
	}
	id, err := env.Plans.Propose(ctx, "privacy_apply", p.key, p.fingerprint, p.preview())
	if err != nil {
		return nil, fmt.Errorf("propose privacy_apply: %w", err)
	}
	p.out.PlanID = id
	args := map[string]any{"app": p.app.ID, "plan_id": id}
	if in.NotCollected {
		args["not_collected"] = true
	}
	if len(in.Data) > 0 {
		args["data"] = in.Data
	}
	return tool.OK(p.out).Then(tool.NextStep{Tool: "privacy_apply", Args: args}), nil
}

type privacyApplyOut struct {
	Status    string   `json:"status"`
	App       appRef   `json:"app"`
	Steps     []step   `json:"steps"`
	Published *bool    `json:"published"`
	Final     []string `json:"declaration,omitempty"`
	// Remaining counts the changes still outstanding after a failure (nil
	// when the re-read failed).
	Remaining *int `json:"remaining_changes,omitempty"`
}

type privacyPartial struct {
	tool.ErrorBody
	Partial privacyApplyOut `json:"partial"`
}

func privacyApply(ctx context.Context, env *tool.Env, in privacyApplyIn) (*tool.Result, error) {
	p, err := computePrivacy(ctx, env, in.privacyPlanIn)
	if err != nil {
		return nil, err
	}
	replan := tool.NextStep{Tool: "privacy_plan", Args: map[string]any{"app": p.app.ID}}
	switch p.out.Status {
	case statusInvalid:
		return nil, tool.WithNext(fmt.Errorf("%w: %s", tool.ErrInvalidArgs, strings.Join(p.out.Problems, "; ")), replan)
	case statusNoChanges:
		return tool.Already(privacyApplyOut{Status: "applied", App: p.out.App, Steps: []step{}, Published: p.out.Published, Final: p.out.Desired}), nil
	}
	wctx, res := tool.Preview(ctx, env, in.PlanID, p.key, p.fingerprint, p.preview())
	if res != nil {
		return res, nil
	}
	out := privacyApplyOut{App: p.out.App, Steps: p.out.Steps}
	for i := range out.Steps {
		s := &out.Steps[i]
		var err error
		if s.Op == opPublish {
			err = publish(ctx, wctx, env, p.app.ID, s)
		} else {
			err = runStep(wctx, env, p.app.ID, s)
		}
		if err == nil {
			continue
		}
		s.Outcome = outcomeOf(err)
		b := tool.BodyFor(err)
		s.Error = &b
		for j := i + 1; j < len(out.Steps); j++ {
			out.Steps[j].Outcome = outcomeNotAttempted
		}
		recheck(ctx, env, p, &out, s)
		out.Status = "partial"
		return &tool.Result{Data: privacyPartial{ErrorBody: b, Partial: out}, IsError: true, Next: append(b.Next, replan)}, nil
	}
	// Read back what Apple now has.
	if us, err := readUsages(ctx, env, p.app.ID); err == nil {
		ts := make([]tuple, len(us))
		for i, u := range us {
			ts[i] = u.tuple
		}
		d, _ := declarationOf(ts)
		out.Final = summaryLines(d)
	}
	if ps, err := readPublishState(ctx, env, p.app.ID); err == nil {
		out.Published = ps.Published
	}
	out.Status = "applied"
	r := tool.OK(out)
	if out.Published == nil || !*out.Published {
		r.Warn("Apple does not report the declaration as published yet; check privacy_get")
	}
	return r.Then(tool.NextStep{Tool: "release_status", Args: map[string]any{"app": p.app.ID}}), nil
}

// runStep performs one appDataUsages mutation.
func runStep(ctx context.Context, env *tool.Env, appID string, s *step) error {
	var req tool.Request
	switch s.Op {
	case opDelete:
		req = tool.Request{Method: http.MethodDelete, Path: "/v1/appDataUsages/" + s.UsageID, Write: true}
	case opUpdate:
		req = tool.Request{
			Method: http.MethodPatch, Path: "/v1/appDataUsages/" + s.UsageID, Write: true,
			Body: map[string]any{"data": map[string]any{"type": "appDataUsages", "id": s.UsageID, "relationships": usageRels("", s.t)}},
		}
	case opCreate:
		req = tool.Request{
			Method: http.MethodPost, Path: "/v1/appDataUsages", Write: true,
			Body: map[string]any{"data": map[string]any{"type": "appDataUsages", "relationships": usageRels(appID, s.t)}},
		}
	default:
		return fmt.Errorf("%w: unknown privacy step %q", tool.ErrInternal, s.Op)
	}
	resp, err := env.Web.Do(ctx, req)
	if err != nil {
		if s.Op == opDelete && tool.APIStatus(err) == http.StatusNotFound {
			s.Outcome = outcomeAlready
			return nil
		}
		return fmt.Errorf("%s %s: %w", s.Op, s.Usage, err)
	}
	if s.Op == opCreate {
		if r, ok := resp.First(); ok {
			s.UsageID = r.ID
		}
	}
	s.Outcome = outcomeDone
	return nil
}

// usageRels is the relationships object of an appDataUsages body.
func usageRels(appID string, t tuple) map[string]any {
	rels := map[string]any{"dataProtection": tool.RelData("appDataUsageDataProtections", t.Protection)}
	if appID != "" {
		rels["app"] = tool.RelData("apps", appID)
	}
	if t.Category != "" {
		rels["category"] = tool.RelData("appDataUsageCategories", t.Category)
	}
	if t.Purpose != "" {
		rels["purpose"] = tool.RelData("appDataUsagePurposes", t.Purpose)
	}
	return rels
}

// publish sets the declaration published ("Publish" in App Store Connect)
// unless Apple already reports it published (edits to a published
// declaration stay published).
func publish(ctx, wctx context.Context, env *tool.Env, appID string, s *step) error {
	ps, err := readPublishState(ctx, env, appID)
	if err != nil {
		return err
	}
	if ps.Published != nil && *ps.Published {
		s.Outcome = outcomeAlready
		return nil
	}
	if ps.ID == "" {
		return fmt.Errorf("publish App Privacy: Apple returned no publish state id: %w", tool.ErrUpstreamInvalid)
	}
	_, err = env.Web.Do(wctx, tool.Request{
		Method: http.MethodPatch, Path: "/v1/appDataUsagesPublishState/" + ps.ID, Write: true,
		Body: map[string]any{"data": map[string]any{"type": "appDataUsagesPublishState", "id": ps.ID, "attributes": map[string]any{"published": true}}},
	})
	if err != nil {
		return fmt.Errorf("publish App Privacy: %w", err)
	}
	s.Outcome = outcomeDone
	return nil
}

// recheck re-reads the declaration once after a failed step: an
// outcome_unknown step is resolved from remote evidence and the remaining
// change count is reported. A failed re-read leaves everything as is.
func recheck(ctx context.Context, env *tool.Env, p *privacyComputed, out *privacyApplyOut, failed *step) {
	us, err := readUsages(ctx, env, p.app.ID)
	if err != nil {
		return
	}
	n := len(diffSteps(us, p.desired))
	out.Remaining = &n
	if failed.Outcome != outcomeUnknown || failed.Op == opPublish {
		return
	}
	byID := map[string]usage{}
	keys := map[string]bool{}
	for _, u := range us {
		byID[u.ID] = u
		keys[u.key()] = true
	}
	switch failed.Op {
	case opCreate:
		if keys[failed.t.key()] {
			failed.Outcome = outcomeDone
		} else {
			failed.Outcome = outcomeNotAttempted
		}
	case opDelete:
		if _, ok := byID[failed.UsageID]; ok {
			failed.Outcome = outcomeNotAttempted
		} else {
			failed.Outcome = outcomeDone
		}
	case opUpdate:
		if u, ok := byID[failed.UsageID]; ok {
			if u.key() == failed.t.key() {
				failed.Outcome = outcomeDone
			} else {
				failed.Outcome = outcomeNotAttempted
			}
		}
	}
}
