package portalsigning

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "signing_portal_get", Toolset: tool.Web, Class: tool.Read, Idempotent: true, NeedsTeamKey: true,
		Title: "Read a bundle ID in the developer portal",
		Description: "A bundle ID as the developer portal sees it (web session): enabled capabilities incl. portal-only ones " +
			"(Increased Memory Limit, ...), assigned App Groups, and the portal capability catalog for it. Use before signing_portal_apply.",
	}, portalGet))
	tool.Register(tool.New(tool.Meta{
		Name: "signing_app_groups_list", Toolset: tool.Web, Class: tool.Read, Idempotent: true,
		Title:       "List App Groups",
		Description: "App Groups of the developer team (web session): id, identifier, name. filter keeps identifiers or names containing it.",
	}, appGroupsList))
	tool.Register(tool.New(tool.Meta{
		Name: "signing_portal_apply", Toolset: tool.Web, Class: tool.Destructive, NeedsTeamKey: true,
		Confirm: tool.ConfirmByPlan,
		Title:   "Set App Groups and portal capabilities",
		Description: "Without plan_id previews per bundle ID: App Groups to create/assign, capabilities (ids or names) to enable or " +
			"disable; with plan_id applies. Creating a group or disabling needs human approval. Invalidates provisioning profiles.",
	}, portalApply))
}

// --- signing_portal_get ---

type portalGetIn struct {
	BundleID string `json:"bundle_id" jsonschema:"bundle identifier (com.example.app) or bundle ID resource id"`
	TeamID   string `json:"team_id,omitempty" jsonschema:"developer team id; default the bundle ID's App ID prefix"`
}

type groupOut struct {
	ID         string         `json:"id"`
	Identifier string         `json:"identifier,omitempty"`
	Name       tool.Untrusted `json:"name,omitempty"`
}

type portalGetOut struct {
	TeamID       string       `json:"team_id"`
	BundleID     string       `json:"bundle_id"`
	Identifier   string       `json:"identifier"`
	Capabilities []enabledCap `json:"capabilities"`
	AppGroups    []groupOut   `json:"app_groups"`
	Available    []catalogCap `json:"available_capabilities"`
}

func portalGet(ctx context.Context, env *tool.Env, in portalGetIn) (*tool.Result, error) {
	ref, err := resolveBundle(ctx, env, in.BundleID)
	if err != nil {
		return nil, err
	}
	team, err := resolveTeam(ctx, env, in.TeamID, []bundleRef{ref})
	if err != nil {
		return nil, err
	}
	b, err := readBundle(ctx, env, team, ref)
	if err != nil {
		return nil, err
	}
	catalog, err := readCatalog(ctx, env, team, ref.ID)
	if err != nil {
		return nil, err
	}
	out := portalGetOut{
		TeamID: team, BundleID: ref.ID, Identifier: ref.Identifier,
		Capabilities: append([]enabledCap{}, b.caps...), AppGroups: []groupOut{}, Available: catalog,
	}
	byID := map[string]appGroup{}
	if len(b.groups) > 0 {
		// Identifiers come from the team list; a failure leaves ids only.
		if gs, err := listGroups(ctx, env, team); err == nil {
			for _, g := range gs {
				byID[g.ID] = g
			}
		}
	}
	for _, id := range b.groups {
		g := byID[id]
		out.AppGroups = append(out.AppGroups, groupOut{ID: id, Identifier: g.Identifier, Name: g.Name})
	}
	return tool.OK(out), nil
}

// --- signing_app_groups_list ---

type groupsListIn struct {
	Filter string `json:"filter,omitempty" jsonschema:"keep App Groups whose identifier or name contains this (case-insensitive)"`
	TeamID string `json:"team_id,omitempty" jsonschema:"developer team id; default the API key's team (App ID prefix)"`
}

type groupsListOut struct {
	TeamID    string     `json:"team_id"`
	AppGroups []appGroup `json:"app_groups"`
	Count     int        `json:"count"`
}

func appGroupsList(ctx context.Context, env *tool.Env, in groupsListIn) (*tool.Result, error) {
	team, err := resolveTeam(ctx, env, in.TeamID, nil)
	if err != nil {
		return nil, err
	}
	gs, err := listGroups(ctx, env, team)
	if err != nil {
		return nil, err
	}
	f := strings.ToLower(strings.TrimSpace(in.Filter))
	out := groupsListOut{TeamID: team, AppGroups: []appGroup{}}
	for _, g := range gs {
		if f == "" || strings.Contains(strings.ToLower(g.Identifier), f) || strings.Contains(strings.ToLower(string(g.Name)), f) {
			out.AppGroups = append(out.AppGroups, g)
		}
	}
	out.Count = len(out.AppGroups)
	return tool.OK(out), nil
}

// --- signing_portal_apply ---

type applyPlanIn struct {
	BundleIDs    []string `json:"bundle_ids" jsonschema:"bundle identifiers or resource ids to change"`
	AppGroups    []string `json:"app_groups,omitempty" jsonschema:"App Group identifiers (group.com.example) to assign to every bundle ID; missing ones are created"`
	Capabilities []string `json:"capabilities,omitempty" jsonschema:"capability ids or names from the portal catalog to enable on every bundle ID"`
	Disable      []string `json:"disable,omitempty" jsonschema:"capability ids or names to disable (human approval)"`
	TeamID       string   `json:"team_id,omitempty" jsonschema:"developer team id; default the bundle IDs' App ID prefix"`
}

type applyIn struct {
	tool.ConfirmPlan
	applyPlanIn
}

// Step operations, in apply order.
const (
	opCreateGroup  = "create_group"
	opAssignGroups = "assign_groups"
	opEnable       = "enable"
	opDisable      = "disable"
)

type step struct {
	Op         string `json:"op"`
	BundleID   string `json:"bundle_id,omitempty"`
	Identifier string `json:"identifier,omitempty"`
	Target     string `json:"target"`
	// Groups is the full App Group set an assign step sends (identifiers).
	Groups  []string        `json:"groups,omitempty"`
	Outcome string          `json:"outcome,omitempty"`
	Error   *tool.ErrorBody `json:"error,omitempty"`
}

type bundlePlan struct {
	BundleID       string   `json:"bundle_id"`
	Identifier     string   `json:"identifier"`
	AddGroups      []string `json:"add_groups,omitempty"`
	Enable         []string `json:"enable,omitempty"`
	Disable        []string `json:"disable,omitempty"`
	AlreadyGroups  []string `json:"already_groups,omitempty"`
	AlreadyEnabled []string `json:"already_enabled,omitempty"`
	AlreadyOff     []string `json:"already_disabled,omitempty"`
}

type applyOut struct {
	Status       string       `json:"status"`
	TeamID       string       `json:"team_id"`
	CreateGroups []string     `json:"create_groups,omitempty"`
	Bundles      []bundlePlan `json:"bundles"`
	Steps        []step       `json:"steps"`
}

type applyPartial struct {
	tool.ErrorBody
	Partial applyOut `json:"partial"`
}

// computed is a plan computed from live portal state.
type computed struct {
	out       applyOut
	groupIDs  map[string]string   // identifier → id of existing groups
	curGroups map[string][]string // bundle id → assigned group ids
	human     bool
	state     any
}

func dedupe(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func compute(ctx context.Context, env *tool.Env, in applyPlanIn) (*computed, error) {
	refsIn := dedupe(in.BundleIDs)
	groupsIn := dedupe(in.AppGroups)
	if len(refsIn) == 0 {
		return nil, tool.InvalidArgs("bundle_ids must name at least one bundle ID")
	}
	if len(groupsIn) == 0 && len(dedupe(in.Capabilities)) == 0 && len(dedupe(in.Disable)) == 0 {
		return nil, tool.InvalidArgs("pass app_groups, capabilities or disable")
	}
	for _, g := range groupsIn {
		if !groupRE.MatchString(g) {
			return nil, tool.InvalidArgs("App Group identifier %q must look like group.com.example.shared", tool.SanitizeText(g))
		}
	}
	refs := make([]bundleRef, 0, len(refsIn))
	for _, r := range refsIn {
		b, err := resolveBundle(ctx, env, r)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(refs, func(x bundleRef) bool { return x.ID == b.ID }) {
			refs = append(refs, b)
		}
	}
	team, err := resolveTeam(ctx, env, in.TeamID, refs)
	if err != nil {
		return nil, err
	}
	c := &computed{groupIDs: map[string]string{}, curGroups: map[string][]string{}}
	c.out = applyOut{TeamID: team, Bundles: []bundlePlan{}, Steps: []step{}}
	var groupState []appGroup
	if len(groupsIn) > 0 {
		gs, err := listGroups(ctx, env, team)
		if err != nil {
			return nil, err
		}
		for _, g := range gs {
			c.groupIDs[g.Identifier] = g.ID
		}
		for _, g := range groupsIn {
			if _, ok := c.groupIDs[g]; ok {
				groupState = append(groupState, appGroup{ID: c.groupIDs[g], Identifier: g})
				continue
			}
			c.out.CreateGroups = append(c.out.CreateGroups, g)
			c.out.Steps = append(c.out.Steps, step{Op: opCreateGroup, Target: g})
			c.human = true
		}
	}
	idToIdent := map[string]string{}
	for ident, id := range c.groupIDs {
		idToIdent[id] = ident
	}
	var enables, disables []step
	var bundleState []any
	for _, ref := range refs {
		b, err := readBundle(ctx, env, team, ref)
		if err != nil {
			return nil, err
		}
		bundleState = append(bundleState, map[string]any{"id": ref.ID, "caps": b.caps, "groups": b.groups})
		c.curGroups[ref.ID] = b.groups
		bp := bundlePlan{BundleID: ref.ID, Identifier: ref.Identifier}
		if len(groupsIn) > 0 {
			full := []string{}
			for _, id := range b.groups {
				full = append(full, identOr(idToIdent, id))
			}
			for _, g := range groupsIn {
				if id, ok := c.groupIDs[g]; ok && slices.Contains(b.groups, id) {
					bp.AlreadyGroups = append(bp.AlreadyGroups, g)
					continue
				}
				bp.AddGroups = append(bp.AddGroups, g)
				full = append(full, g)
			}
			if len(bp.AddGroups) > 0 {
				c.out.Steps = append(c.out.Steps, step{Op: opAssignGroups, BundleID: ref.ID, Identifier: ref.Identifier, Target: capAppGroups, Groups: full})
			}
		}
		if len(in.Capabilities) > 0 || len(in.Disable) > 0 {
			catalog, err := readCatalog(ctx, env, team, ref.ID)
			if err != nil {
				return nil, err
			}
			for _, q := range dedupe(in.Capabilities) {
				cc, err := matchCap(catalog, q)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", ref.Identifier, err)
				}
				if cc.ID == capAppGroups && len(groupsIn) > 0 {
					continue // the assign step enables it
				}
				if b.has(cc.ID) || slices.Contains(bp.Enable, cc.ID) {
					bp.AlreadyEnabled = appendNew(bp.AlreadyEnabled, cc.ID)
					continue
				}
				bp.Enable = append(bp.Enable, cc.ID)
				enables = append(enables, step{Op: opEnable, BundleID: ref.ID, Identifier: ref.Identifier, Target: cc.ID})
			}
			for _, q := range dedupe(in.Disable) {
				cc, err := matchCap(catalog, q)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", ref.Identifier, err)
				}
				if slices.Contains(bp.Enable, cc.ID) || (cc.ID == capAppGroups && len(groupsIn) > 0) {
					return nil, tool.InvalidArgs("capability %s is both enabled and disabled", cc.ID)
				}
				if !b.has(cc.ID) {
					bp.AlreadyOff = appendNew(bp.AlreadyOff, cc.ID)
					continue
				}
				if slices.Contains(bp.Disable, cc.ID) {
					continue
				}
				bp.Disable = append(bp.Disable, cc.ID)
				disables = append(disables, step{Op: opDisable, BundleID: ref.ID, Identifier: ref.Identifier, Target: cc.ID})
				c.human = true
			}
		}
		c.out.Bundles = append(c.out.Bundles, bp)
	}
	c.out.Steps = append(append(c.out.Steps, enables...), disables...)
	c.state = map[string]any{"team": team, "groups": groupState, "bundles": bundleState}
	return c, nil
}

func identOr(m map[string]string, id string) string {
	if s, ok := m[id]; ok {
		return s
	}
	return id
}

func appendNew(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}

func (c *computed) preview() tool.PlanPreview {
	intended := make([]string, 0, len(c.out.Steps))
	affected := make([]string, 0, len(c.out.Bundles))
	for _, s := range c.out.Steps {
		switch s.Op {
		case opCreateGroup:
			intended = append(intended, "create App Group "+s.Target)
		case opAssignGroups:
			intended = append(intended, fmt.Sprintf("set App Groups of %s to %s", s.Identifier, strings.Join(s.Groups, ", ")))
		default:
			intended = append(intended, s.Op+" "+s.Target+" on "+s.Identifier)
		}
	}
	for _, b := range c.out.Bundles {
		affected = append(affected, b.Identifier)
	}
	p := tool.PlanPreview{
		Summary:      fmt.Sprintf("change developer portal signing setup of %d bundle ID(s) in team %s (%d steps)", len(c.out.Bundles), c.out.TeamID, len(c.out.Steps)),
		Intended:     intended,
		Affected:     affected,
		Irreversible: []string{"provisioning profiles of these bundle IDs become invalid and must be regenerated"},
	}
	if len(c.out.CreateGroups) > 0 {
		p.Irreversible = append(p.Irreversible, "an App Group identifier, once created, can never be reused by another team or after deletion")
	}
	for _, s := range c.out.Steps {
		if s.Op == opDisable {
			p.Irreversible = append(p.Irreversible, "disabling a capability drops its configuration and the entitlement shipped builds rely on")
			break
		}
	}
	return p
}

// planNext is the call that applies a plan.
func planNext(in applyPlanIn, planID string) tool.NextStep {
	args := map[string]any{"bundle_ids": in.BundleIDs, "plan_id": planID}
	if len(in.AppGroups) > 0 {
		args["app_groups"] = in.AppGroups
	}
	if len(in.Capabilities) > 0 {
		args["capabilities"] = in.Capabilities
	}
	if len(in.Disable) > 0 {
		args["disable"] = in.Disable
	}
	if in.TeamID != "" {
		args["team_id"] = in.TeamID
	}
	return tool.NextStep{Tool: "signing_portal_apply", Args: args}
}

func portalApply(ctx context.Context, env *tool.Env, in applyIn) (*tool.Result, error) {
	c, err := compute(ctx, env, in.applyPlanIn)
	if err != nil {
		return nil, err
	}
	if len(c.out.Steps) == 0 {
		c.out.Status = "applied"
		return tool.Already(c.out), nil
	}
	fp, err := tool.Fingerprint(c.state)
	if err != nil {
		return nil, err
	}
	gate := tool.Preview
	if c.human {
		gate = tool.PreviewHuman
	}
	wctx, res := gate(ctx, env, in.PlanID, in.applyPlanIn, fp, c.preview())
	if res != nil {
		if in.PlanID == "" && !res.IsError {
			// Add the per-bundle diff to the preview body.
			if m := asMap(res.Data); m != nil {
				m["plan"] = withStatus(c.out, "planned")
				res.Data = m
				if id, _ := m["plan_id"].(string); id != "" {
					res.Then(planNext(in.applyPlanIn, id))
				}
			}
		}
		return res, nil
	}
	out := c.out
	for i := range out.Steps {
		s := &out.Steps[i]
		if err := c.run(wctx, env, out.TeamID, s); err != nil {
			s.Outcome = outcomeOf(err)
			b := tool.BodyFor(err)
			s.Error = &b
			for j := i + 1; j < len(out.Steps); j++ {
				out.Steps[j].Outcome = outcomeNotAttempted
			}
			out.Status = "partial"
			replan := tool.NextStep{Tool: "signing_portal_get", Args: map[string]any{"bundle_id": firstBundle(out, s)}}
			return &tool.Result{Data: applyPartial{ErrorBody: b, Partial: out}, IsError: true, Next: append(b.Next, replan)}, nil
		}
		s.Outcome = outcomeDone
	}
	out.Status = "applied"
	r := tool.OK(out)
	r.Warn("existing provisioning profiles of these bundle IDs are now invalid: regenerate them (Xcode automatic signing, or signing_profiles_list then signing_profile_create)")
	for _, b := range out.Bundles {
		r.Then(tool.NextStep{Tool: "signing_profiles_list", Args: map[string]any{"bundle_id": b.Identifier}})
	}
	return r, nil
}

func withStatus(o applyOut, s string) applyOut {
	o.Status = s
	return o
}

// asMap re-decodes a preview body as a JSON object (nil when it isn't one).
func asMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func firstBundle(out applyOut, s *step) string {
	if s.BundleID != "" {
		return s.BundleID
	}
	return out.Bundles[0].BundleID
}

// run performs one step; assign steps use ids of groups created earlier.
func (c *computed) run(ctx context.Context, env *tool.Env, team string, s *step) error {
	switch s.Op {
	case opCreateGroup:
		id, err := createGroup(ctx, env, team, s.Target)
		if err != nil {
			return fmt.Errorf("create App Group %s: %w", s.Target, err)
		}
		c.groupIDs[s.Target] = id
		return nil
	case opAssignGroups:
		ids := make([]string, 0, len(s.Groups))
		cur := c.curGroups[s.BundleID]
		for _, g := range s.Groups {
			switch id, ok := c.groupIDs[g]; {
			case ok:
				ids = append(ids, id)
			case slices.Contains(cur, g): // an assigned group outside the team list, kept by id
				ids = append(ids, g)
			default:
				return fmt.Errorf("%w: App Group %s has no id", tool.ErrInternal, g)
			}
		}
		if err := patchCapability(ctx, env, team, s.BundleID, capAppGroups, true, ids); err != nil {
			return fmt.Errorf("assign App Groups to %s: %w", s.Identifier, err)
		}
		return nil
	case opEnable, opDisable:
		if err := patchCapability(ctx, env, team, s.BundleID, s.Target, s.Op == opEnable, nil); err != nil {
			return fmt.Errorf("%s %s on %s: %w", s.Op, s.Target, s.Identifier, err)
		}
		return nil
	}
	return fmt.Errorf("%w: unknown step %q", tool.ErrInternal, s.Op)
}
