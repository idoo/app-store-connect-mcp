package xcodecloud

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "xcc_post_actions_set", Toolset: tool.Web, Class: tool.Write, Idempotent: true, AvailableReadOnly: true,
		Confirm: tool.ConfirmByFlag,
		Title:   "Xcode Cloud workflow post-actions",
		Description: "Post-actions of an Xcode Cloud workflow (web session). internal_groups sets TestFlight Internal Testing " +
			"on its archives so new builds reach those groups; remove_internal deletes it. Preview, then confirm:true.",
		ReadOnlyDescription: "Post-actions of an Xcode Cloud workflow (web session): TestFlight testing, notarization, notifications. " +
			"Changing them needs writes.",
	}, postActions))
}

const (
	// maxInternalGroups is how many beta groups one call may set.
	maxInternalGroups = 20
	// internalPostAction is the post-action type of TestFlight Internal
	// Testing; internalNamePrefix plus the archive's platform name is the
	// name Apple's page gives it.
	internalPostAction = "testFlight_internal"
	internalNamePrefix = "TestFlight Internal Testing - "
)

// webPlatforms maps the web API's platform names to the public spelling.
var webPlatforms = map[string]string{
	"iOS": string(tool.IOS), "macOS": string(tool.MacOS), "tvOS": string(tool.TVOS),
	"visionOS": string(tool.VisionOS), "xrOS": string(tool.VisionOS), "watchOS": "WATCH_OS",
}

// postActionPlatforms are the values of the platforms argument.
var postActionPlatforms = []string{string(tool.IOS), string(tool.MacOS), string(tool.TVOS), string(tool.VisionOS)}

type postActionsIn struct {
	App            tool.AppRef     `json:"app,omitempty" jsonschema:"app whose workflow to use; default: pinned app"`
	Workflow       string          `json:"workflow" jsonschema:"workflow name or id"`
	InternalGroups []tool.GroupRef `json:"internal_groups,omitempty" jsonschema:"1-20 internal beta group names or ids that get every new build"`
	RemoveInternal bool            `json:"remove_internal,omitempty" jsonschema:"delete the TestFlight Internal Testing post-actions"`
	Platforms      []string        `json:"platforms,omitempty" jsonschema:"limit a change to these archives: IOS, MAC_OS, TV_OS, VISION_OS; default: every archive action"`
	tool.ConfirmFlag
}

type paWorkflow struct {
	ID             string         `json:"id"`
	Name           tool.Untrusted `json:"name"`
	Locked         bool           `json:"locked"`
	LastModifiedAt string         `json:"last_modified_at,omitempty"`
	LastModifiedBy tool.Untrusted `json:"last_modified_by,omitempty"`
}

type paArchive struct {
	ID           string         `json:"id"`
	Name         tool.Untrusted `json:"name"`
	Platform     string         `json:"platform,omitempty"`
	Distribution string         `json:"distribution,omitempty"`
}

type paGroup struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"` // trusted: team-created beta group name
}

type paPostAction struct {
	ID              string         `json:"id"`
	Name            tool.Untrusted `json:"name"`
	Type            string         `json:"type"`
	ArchiveActionID string         `json:"archive_action_id,omitempty"`
	Platform        string         `json:"platform,omitempty"`
	Groups          []paGroup      `json:"groups,omitempty"`
	// Testers is the number of individual testers (TestFlight types only).
	Testers *int `json:"testers,omitempty"`
}

type postActionsOut struct {
	AppID       string         `json:"app_id,omitempty"`
	ProductID   string         `json:"product_id"`
	Workflow    paWorkflow     `json:"workflow"`
	Archives    []paArchive    `json:"archives"`
	PostActions []paPostAction `json:"post_actions"`
	// Status is "set" or "removed" after a write, "already" when there was
	// nothing to change, empty for a read.
	Status string `json:"status,omitempty"`
}

// ciAction is the part of a workflow action the tool reads.
type ciAction struct {
	ID          string `json:"id"`
	DefaultName string `json:"default_name"`
	ManualName  string `json:"manual_name"`
	ActionType  string `json:"action_type"`
	Platform    struct {
		Name string `json:"name"`
	} `json:"platform"`
	ArchiveConfig struct {
		DistributionType string `json:"distribution_type"`
	} `json:"archive_config"`
}

// name is the action's display name.
func (a ciAction) name() string {
	if a.ManualName != "" {
		return a.ManualName
	}
	return a.DefaultName
}

// ciArchiveRef is the config of a post-action that only names its archive.
type ciArchiveRef struct {
	ArchiveActionID string `json:"archive_action_id,omitempty"`
}

type ciDeployment struct {
	IDs struct {
		BetaGroupIDs  []string `json:"beta_group_ids"`
		BetaTesterIDs []string `json:"beta_tester_ids"`
	} `json:"testflight_deployment_ids"`
	ArchiveActionID string `json:"archive_action_id"`
}

// ciPostAction is the part of a post-action the tool reads, and the whole
// of a TestFlight Internal Testing post-action it adds.
type ciPostAction struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Type         string        `json:"type"`
	Deployment   *ciDeployment `json:"deployment_config,omitempty"`
	Notification *ciArchiveRef `json:"notification_config,omitempty"`
	Notarization *ciArchiveRef `json:"notarization_config,omitempty"`
}

// archiveID is the archive action the post-action points at, if any.
func (p ciPostAction) archiveID() string {
	switch {
	case p.Deployment != nil:
		return p.Deployment.ArchiveActionID
	case p.Notarization != nil:
		return p.Notarization.ArchiveActionID
	case p.Notification != nil:
		return p.Notification.ArchiveActionID
	}
	return ""
}

// groupIDs are the beta groups of a TestFlight post-action.
func (p ciPostAction) groupIDs() []string {
	if p.Deployment == nil {
		return nil
	}
	return p.Deployment.IDs.BetaGroupIDs
}

// ciWorkflow is a workflow as the web API holds it: content with every key
// kept raw, and the parts the tool reads decoded.
type ciWorkflow struct {
	content    map[string]json.RawMessage
	modifiedAt string
	modifiedBy string
	name       string
	locked     bool
	archives   []ciAction
	// raw and posts are content.post_actions, element by element.
	raw   []json.RawMessage
	posts []ciPostAction
}

// archive returns the archive action with the id.
func (w *ciWorkflow) archive(id string) (ciAction, bool) {
	i := slices.IndexFunc(w.archives, func(a ciAction) bool { return a.ID == id })
	if i < 0 {
		return ciAction{}, false
	}
	return w.archives[i], true
}

// postPlatform is the web platform name a post-action belongs to: that of
// its archive, else the one in an internal post-action's name.
func (w *ciWorkflow) postPlatform(p ciPostAction) string {
	if a, ok := w.archive(p.archiveID()); ok {
		return a.Platform.Name
	}
	if name, ok := strings.CutPrefix(p.Name, internalNamePrefix); ok && p.Type == internalPostAction {
		return name
	}
	return ""
}

// readCIWorkflow GETs the workflow from the web API.
func readCIWorkflow(ctx context.Context, env *tool.Env, path, id string) (*ciWorkflow, error) {
	var doc struct {
		Content  map[string]json.RawMessage `json:"content"`
		Metadata struct {
			LastModifiedAt string `json:"last_modified_at"`
			LastModifiedBy string `json:"last_modified_by"`
		} `json:"metadata"`
	}
	err := env.Portal.JSON(ctx, http.MethodGet, path, nil, &doc)
	var pe *tool.PortalError
	if errors.As(err, &pe) && pe.Status == http.StatusNotFound {
		return nil, &tool.ErrNotFound{
			Kind: "workflow", Ref: id,
			Hint: "not in the web session's team; switch it with `app-store-connect-mcp web team`",
		}
	}
	if err != nil {
		return nil, fmt.Errorf("read workflow %s: %w", id, err)
	}
	if doc.Content == nil {
		return nil, fmt.Errorf("read workflow %s: answer has no content", id)
	}
	w := &ciWorkflow{content: doc.Content, modifiedAt: doc.Metadata.LastModifiedAt, modifiedBy: doc.Metadata.LastModifiedBy}
	var actions []ciAction
	for key, dst := range map[string]any{"name": &w.name, "locked": &w.locked, "actions": &actions, "post_actions": &w.raw} {
		raw, ok := doc.Content[key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			return nil, fmt.Errorf("read workflow %s: %s: %w", id, key, err)
		}
	}
	for _, a := range actions {
		if a.ActionType == "archive" {
			w.archives = append(w.archives, a)
		}
	}
	w.posts = make([]ciPostAction, len(w.raw))
	for i, raw := range w.raw {
		if err := json.Unmarshal(raw, &w.posts[i]); err != nil {
			return nil, fmt.Errorf("read workflow %s: post_actions[%d]: %w", id, i, err)
		}
	}
	return w, nil
}

// groupNames resolves beta group ids of one app to names, once per id.
type groupNames struct {
	appID string
	names map[string]string
}

// name returns the group's name, "" when the id no longer resolves.
func (g *groupNames) name(ctx context.Context, env *tool.Env, id string) (string, error) {
	if n, ok := g.names[id]; ok {
		return n, nil
	}
	n := ""
	if g.appID != "" {
		grp, err := env.Resolve.Group(ctx, g.appID, tool.GroupRef(id))
		var nf *tool.ErrNotFound
		switch {
		case err == nil && grp.ID == id:
			n = grp.Name
		case err != nil && !errors.As(err, &nf):
			return "", err
		}
	}
	g.names[id] = n
	return n, nil
}

// labels renders group ids as names (the id where none resolves).
func (g *groupNames) labels(ctx context.Context, env *tool.Env, ids []string) (string, error) {
	out := make([]string, len(ids))
	for i, id := range ids {
		n, err := g.name(ctx, env, id)
		if err != nil {
			return "", err
		}
		if n == "" {
			n = id
		}
		out[i] = n
	}
	return strings.Join(out, ", "), nil
}

// view builds the result from the workflow.
func (w *ciWorkflow) view(ctx context.Context, env *tool.Env, wf workflowOut, groups *groupNames) (postActionsOut, error) {
	out := postActionsOut{
		AppID: groups.appID, ProductID: wf.ProductID,
		Workflow: paWorkflow{
			ID: wf.ID, Name: tool.Untrusted(w.name), Locked: w.locked,
			LastModifiedAt: w.modifiedAt, LastModifiedBy: tool.Untrusted(w.modifiedBy),
		},
		Archives:    make([]paArchive, 0, len(w.archives)),
		PostActions: make([]paPostAction, 0, len(w.posts)),
	}
	if w.name == "" {
		out.Workflow.Name = wf.Name
	}
	for _, a := range w.archives {
		out.Archives = append(out.Archives, paArchive{
			ID: a.ID, Name: tool.Untrusted(a.name()), Platform: webPlatforms[a.Platform.Name],
			Distribution: a.ArchiveConfig.DistributionType,
		})
	}
	for _, p := range w.posts {
		o := paPostAction{
			ID: p.ID, Name: tool.Untrusted(p.Name), Type: p.Type,
			ArchiveActionID: p.archiveID(), Platform: webPlatforms[w.postPlatform(p)],
		}
		if p.Deployment != nil {
			for _, id := range p.groupIDs() {
				n, err := groups.name(ctx, env, id)
				if err != nil {
					return postActionsOut{}, err
				}
				o.Groups = append(o.Groups, paGroup{ID: id, Name: n})
			}
			n := len(p.Deployment.IDs.BetaTesterIDs)
			o.Testers = &n
		}
		out.PostActions = append(out.PostActions, o)
	}
	return out, nil
}

// targets returns the archive actions a change applies to: every one, or
// those of the named platforms.
func (w *ciWorkflow) targets(platforms []string) ([]ciAction, error) {
	if len(w.archives) == 0 {
		return nil, tool.InvalidArgs("the workflow has no archive action; add one in Xcode Cloud first")
	}
	if len(platforms) == 0 {
		return w.archives, nil
	}
	have := make([]string, 0, len(w.archives))
	for _, a := range w.archives {
		if p := webPlatforms[a.Platform.Name]; p != "" && !slices.Contains(have, p) {
			have = append(have, p)
		}
	}
	for _, p := range platforms {
		if !slices.Contains(have, p) {
			return nil, tool.InvalidArgs("the workflow has no %s archive action; its archives: %s", p, strings.Join(have, ", "))
		}
	}
	var out []ciAction
	for _, a := range w.archives {
		if slices.Contains(platforms, webPlatforms[a.Platform.Name]) {
			out = append(out, a)
		}
	}
	return out, nil
}

// checkSettable refuses targets Apple's page would not give TestFlight
// Internal Testing to.
func (w *ciWorkflow) checkSettable(targets []ciAction) error {
	seen := map[string]string{}
	for _, a := range targets {
		switch d := a.ArchiveConfig.DistributionType; d {
		case "testFlightInternalOnly", "testFlightExternalAndAppStore":
		default:
			return tool.InvalidArgs("archive %q is prepared for distribution %q; set it to TestFlight (Internal Testing Only) or "+
				"TestFlight and App Store in the workflow, or leave it out with platforms", a.name(), d)
		}
		if other, dup := seen[a.Platform.Name]; dup {
			return tool.InvalidArgs("archives %q and %q are both %s; a workflow takes one TestFlight Internal Testing post-action per platform",
				other, a.name(), a.Platform.Name)
		}
		seen[a.Platform.Name] = a.name()
		for _, p := range w.posts {
			if p.Type == internalPostAction && p.archiveID() != a.ID && w.postPlatform(p) == a.Platform.Name {
				return tool.InvalidArgs("TestFlight Internal Testing post-action %q of %s points at another archive action than %q; "+
					"delete it first (remove_internal, or in Xcode Cloud)", p.Name, a.Platform.Name, a.name())
			}
		}
	}
	return nil
}

// internalChange is the new post_actions array and what the preview says.
type internalChange struct {
	raw      []json.RawMessage
	posts    []ciPostAction
	lines    []string
	warnings []string
}

// setInternal gives every target archive a TestFlight Internal Testing
// post-action with the groups; nil when all of them have exactly those.
func (w *ciWorkflow) setInternal(ctx context.Context, env *tool.Env, targets []ciAction, set []tool.Group, groups *groupNames) (*internalChange, error) {
	ids := make([]string, len(set))
	for i, g := range set {
		ids[i] = g.ID
	}
	want, err := groups.labels(ctx, env, ids)
	if err != nil {
		return nil, err
	}
	c := &internalChange{}
	changed := false
	has := map[string]bool{}
	for i, p := range w.posts {
		el := w.raw[i]
		a, ok := w.archive(p.archiveID())
		if p.Type == internalPostAction && ok && slices.ContainsFunc(targets, func(t ciAction) bool { return t.ID == a.ID }) {
			has[a.ID] = true
			if cur := p.groupIDs(); !sameIDs(cur, ids) {
				if el, err = setGroupIDs(el, ids); err != nil {
					return nil, fmt.Errorf("post-action %s: %w", p.ID, err)
				}
				changed = true
				if len(cur) > 0 {
					was, err := groups.labels(ctx, env, cur)
					if err != nil {
						return nil, err
					}
					c.warnings = append(c.warnings, fmt.Sprintf("replaces the current groups of %q: %s", a.name(), was))
				}
				d := *p.Deployment
				d.IDs.BetaGroupIDs = ids
				p.Deployment = &d
			}
		}
		c.raw, c.posts = append(c.raw, el), append(c.posts, p)
	}
	for _, a := range targets {
		c.lines = append(c.lines, a.name()+" → "+want)
		if has[a.ID] {
			continue
		}
		id, err := newUUID()
		if err != nil {
			return nil, err
		}
		p := ciPostAction{ID: id, Name: internalNamePrefix + a.Platform.Name, Type: internalPostAction, Deployment: &ciDeployment{ArchiveActionID: a.ID}}
		p.Deployment.IDs.BetaGroupIDs, p.Deployment.IDs.BetaTesterIDs = ids, []string{}
		el, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		c.raw, c.posts = append(c.raw, el), append(c.posts, p)
		changed = true
	}
	if !changed {
		return nil, nil
	}
	return c, nil
}

// removeInternal drops the TestFlight Internal Testing post-actions of the
// target archives; nil when there is none.
func (w *ciWorkflow) removeInternal(ctx context.Context, env *tool.Env, targets []ciAction, groups *groupNames) (*internalChange, error) {
	c := &internalChange{raw: []json.RawMessage{}}
	for i, p := range w.posts {
		a, ok := w.archive(p.archiveID())
		if p.Type != internalPostAction || !ok || !slices.ContainsFunc(targets, func(t ciAction) bool { return t.ID == a.ID }) {
			c.raw, c.posts = append(c.raw, w.raw[i]), append(c.posts, p)
			continue
		}
		was, err := groups.labels(ctx, env, p.groupIDs())
		if err != nil {
			return nil, err
		}
		c.lines = append(c.lines, a.name())
		c.warnings = append(c.warnings, fmt.Sprintf("new builds of %q no longer reach: %s", a.name(), was))
	}
	if len(c.lines) == 0 {
		return nil, nil
	}
	return c, nil
}

func postActions(ctx context.Context, env *tool.Env, in postActionsIn) (*tool.Result, error) {
	in.Workflow = strings.TrimSpace(in.Workflow)
	set := in.InternalGroups != nil
	switch {
	case in.Workflow == "":
		return nil, tool.InvalidArgs("workflow is required: a workflow name or id")
	case set && in.RemoveInternal:
		return nil, tool.InvalidArgs("pass internal_groups or remove_internal, not both")
	case set && len(in.InternalGroups) == 0:
		return nil, tool.InvalidArgs("internal_groups needs 1-%d groups; remove_internal deletes the post-action", maxInternalGroups)
	case len(in.InternalGroups) > maxInternalGroups:
		return nil, tool.InvalidArgs("at most %d internal_groups, got %d", maxInternalGroups, len(in.InternalGroups))
	case len(in.Platforms) > 0 && !set && !in.RemoveInternal:
		return nil, tool.InvalidArgs("platforms limits a change; pass internal_groups or remove_internal with it")
	}
	platforms := make([]string, len(in.Platforms))
	for i, p := range in.Platforms {
		platforms[i] = strings.ToUpper(strings.TrimSpace(p))
		if !slices.Contains(postActionPlatforms, platforms[i]) {
			return nil, tool.InvalidArgs("unknown platform %q; use %s", p, strings.Join(postActionPlatforms, ", "))
		}
	}
	wf, err := resolveWorkflow(ctx, env, in.App, in.Workflow, "xcc_post_actions_set")
	if err != nil {
		return nil, err
	}
	team, err := ciTeam(ctx, env)
	if err != nil {
		return nil, err
	}
	_, appID, err := resolveProduct(ctx, env, "", wf.ProductID)
	if err != nil {
		return nil, err
	}
	path := "/ci/api/teams/" + team + "/products/" + wf.ProductID + "/workflows-v15/" + wf.ID
	w, err := readCIWorkflow(ctx, env, path, wf.ID)
	if err != nil {
		return nil, err
	}
	groups := &groupNames{appID: appID, names: map[string]string{}}
	if !set && !in.RemoveInternal {
		out, err := w.view(ctx, env, wf, groups)
		if err != nil {
			return nil, err
		}
		return tool.OK(out), nil
	}

	if w.locked {
		return nil, tool.InvalidArgs("workflow %q is locked for editing; unlock it in Xcode Cloud first", w.name)
	}
	targets, err := w.targets(platforms)
	if err != nil {
		return nil, err
	}
	var change *internalChange
	verb, status := "remove", "removed"
	if set {
		verb, status = "set", "set"
		if err := w.checkSettable(targets); err != nil {
			return nil, err
		}
		if appID == "" {
			return nil, fmt.Errorf("the Xcode Cloud product %s has no app; its beta groups cannot be resolved", wf.ProductID)
		}
		var resolved []tool.Group
		for _, ref := range in.InternalGroups {
			g, err := env.Resolve.Group(ctx, appID, ref)
			if err != nil {
				return nil, err
			}
			if !g.Internal {
				return nil, tool.InvalidArgs("beta group %q is external; TestFlight Internal Testing takes internal groups only", g.Name)
			}
			if !slices.ContainsFunc(resolved, func(r tool.Group) bool { return r.ID == g.ID }) {
				resolved = append(resolved, g)
				groups.names[g.ID] = g.Name
			}
		}
		change, err = w.setInternal(ctx, env, targets, resolved, groups)
	} else {
		change, err = w.removeInternal(ctx, env, targets, groups)
	}
	if err != nil {
		return nil, err
	}
	if change == nil {
		out, err := w.view(ctx, env, wf, groups)
		if err != nil {
			return nil, err
		}
		out.Status = "already"
		return tool.OK(out), nil
	}

	// Bound to the last save: a workflow saved by anyone since the preview
	// needs a new one.
	wctx, res := tool.RequireConfirmTarget(ctx, env, in.Confirm,
		map[string]any{"workflow": wf.ID, "last_modified_at": w.modifiedAt},
		tool.PlanPreview{
			Summary:  fmt.Sprintf("%s TestFlight Internal Testing of workflow %q: %s", verb, w.name, strings.Join(change.lines, "; ")),
			Affected: []string{"Xcode Cloud workflow " + wf.ID},
			Warnings: change.warnings,
		})
	if res != nil {
		return res, nil
	}
	read := tool.NextStep{Tool: "xcc_post_actions_set", Args: map[string]any{"workflow": wf.ID}}
	if in.App != "" {
		read.Args["app"] = string(in.App)
	}
	body := maps.Clone(w.content)
	if body["post_actions"], err = json.Marshal(change.raw); err != nil {
		return nil, err
	}
	if err := env.Portal.JSON(wctx, http.MethodPut, path, body, nil); err != nil {
		err = fmt.Errorf("save post-actions of workflow %s: %w", wf.ID, err)
		var pe *tool.PortalError
		if !errors.As(err, &pe) && !errors.Is(err, tool.ErrWritesDisabled) && !errors.Is(err, tool.ErrWebSessionRequired) {
			// Network failure: the workflow may have been saved; read it back.
			err = tool.WithNext(err, read)
		}
		return nil, err
	}
	got, err := readCIWorkflow(ctx, env, path, wf.ID)
	if err != nil {
		return nil, tool.WithNext(fmt.Errorf("post-actions saved, but reading the workflow back failed: %w", err), read)
	}
	out, err := got.view(ctx, env, wf, groups)
	if err != nil {
		return nil, tool.WithNext(fmt.Errorf("post-actions saved, but reading their groups failed: %w", err), read)
	}
	out.Status = status
	r := tool.OK(out)
	if !slices.Equal(internalState(got.posts), internalState(change.posts)) {
		r.Warn("Apple saved other TestFlight Internal Testing post-actions than were sent; check post_actions")
	}
	if keys := changedKeys(w.content, got.content); len(keys) > 0 {
		r.Warn("Apple changed other parts of the workflow on save: " + strings.Join(keys, ", "))
	}
	return r, nil
}

// sameIDs reports whether a and b hold the same ids, in any order.
func sameIDs(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// internalState lists the TestFlight Internal Testing post-actions as
// sorted "archive: groups" lines, for comparing two workflows.
func internalState(posts []ciPostAction) []string {
	var out []string
	for _, p := range posts {
		if p.Type == internalPostAction {
			ids := slices.Clone(p.groupIDs())
			slices.Sort(ids)
			out = append(out, p.archiveID()+": "+strings.Join(ids, ","))
		}
	}
	slices.Sort(out)
	return out
}

// changedKeys returns the sorted content keys other than post_actions
// whose JSON value differs between before and after.
func changedKeys(before, after map[string]json.RawMessage) []string {
	var out []string
	check := func(key string) {
		if key == "post_actions" || slices.Contains(out, key) {
			return
		}
		var a, b any
		if json.Unmarshal(before[key], &a) != nil || json.Unmarshal(after[key], &b) != nil || !reflect.DeepEqual(a, b) {
			out = append(out, key)
		}
	}
	for key := range before {
		check(key)
	}
	for key := range after {
		check(key)
	}
	slices.Sort(out)
	return out
}

// setGroupIDs replaces deployment_config.testflight_deployment_ids.beta_group_ids
// of a post-action element; every other key of it survives.
func setGroupIDs(el json.RawMessage, ids []string) (json.RawMessage, error) {
	var obj, dep, tf map[string]json.RawMessage
	if err := json.Unmarshal(el, &obj); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(obj["deployment_config"], &dep); err != nil {
		return nil, fmt.Errorf("deployment_config: %w", err)
	}
	if raw, ok := dep["testflight_deployment_ids"]; ok {
		if err := json.Unmarshal(raw, &tf); err != nil {
			return nil, fmt.Errorf("testflight_deployment_ids: %w", err)
		}
	}
	if tf == nil {
		tf = map[string]json.RawMessage{}
	}
	var err error
	if tf["beta_group_ids"], err = json.Marshal(ids); err != nil {
		return nil, err
	}
	if dep["testflight_deployment_ids"], err = json.Marshal(tf); err != nil {
		return nil, err
	}
	if obj["deployment_config"], err = json.Marshal(dep); err != nil {
		return nil, err
	}
	return json.Marshal(obj)
}

// newUUID returns a random lower-case UUID v4, as Apple's page makes for a
// new post-action.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("make a post-action id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
