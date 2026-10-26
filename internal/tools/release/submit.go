package release

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/ascrules"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

type submitIn struct {
	App                  tool.AppRef     `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform             tool.Platform   `json:"platform,omitempty"`
	Version              tool.VersionRef `json:"version,omitempty" jsonschema:"version string; default: editable"`
	IncludeIAPs          []string        `json:"include_iaps,omitempty" jsonschema:"IAPs to submit with it: product id, name or id"`
	IncludeSubscriptions []string        `json:"include_subscriptions,omitempty" jsonschema:"subscriptions to submit with it: product id, name or id"`
	// AttachFirstTimeProducts nil means true.
	AttachFirstTimeProducts *bool `json:"attach_first_time_products,omitempty" jsonschema:"attach every ready IAP/subscription (default true)"`
	tool.ConfirmPlan
}

type cancelIn struct {
	App      tool.AppRef   `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform tool.Platform `json:"platform,omitempty"`
	Items    []string      `json:"items,omitempty" jsonschema:"draft only: item ids to remove; default: all"`
	tool.ConfirmFlag
}

func init() {
	platforms := []any{"IOS", "MAC_OS", "TV_OS", "VISION_OS"}
	tool.Register(tool.New(tool.Meta{
		Name: "release_submit", Toolset: tool.Release, Class: tool.Destructive, Idempotent: true,
		Confirm: tool.ConfirmByPlan, RequireHuman: true,
		Title: "Submit a version for App Review",
		Description: "Submit the version (plus IAPs/subscriptions) for App Review; reuses a draft, resubmits after " +
			"rejection. First call previews and returns plan_id; call again with it.",
		Enums: map[string][]any{"platform": platforms},
	}, releaseSubmit))
	tool.Register(tool.New(tool.Meta{
		Name: "release_cancel", Toolset: tool.Release, Class: tool.Destructive, Idempotent: true,
		Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Cancel an App Review submission",
		Description: "Cancel the open review submission (state CANCELING), or empty an unsent draft by deleting its items. " +
			"Without confirm:true returns a preview.",
		Enums: map[string][]any{"platform": platforms},
	}, releaseCancel))
}

// submitKey is release_submit's canonical plan args. Without products and
// with the default attach_first_time_products it marshals exactly like
// [submitArgsKey], so release_status plans verify.
type submitKey struct {
	submitPlanKey
	IAPs          []string `json:"include_iaps,omitempty"`
	Subscriptions []string `json:"include_subscriptions,omitempty"`
	NoAutoAttach  bool     `json:"no_auto_attach,omitempty"`
}

// openItemStates are product version states that can go into a submission.
var openItemStates = []string{"PREPARE_FOR_SUBMISSION", "READY_FOR_REVIEW", "REJECTED", "DEVELOPER_REJECTED"}

// inReviewItemStates are product version states already with App Review.
var inReviewItemStates = []string{"WAITING_FOR_REVIEW", "IN_REVIEW"}

// wantedItem is one object the submission must contain.
type wantedItem struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Target string `json:"target_id,omitempty"`
	// Create names the parent ("type:id") of a product version to create
	// when Target is empty.
	Create string `json:"-"`
	Action string `json:"action"`
}

type submitOut struct {
	App        tool.App       `json:"app"`
	Platform   tool.Platform  `json:"platform"`
	Version    prepareVersion `json:"version"`
	Submission submissionView `json:"submission"`
	Items      []wantedItem   `json:"items"`
	Resolved   []string       `json:"resolved_items,omitempty"`
	Created    bool           `json:"created_submission,omitempty"`
	Notes      []string       `json:"notes,omitempty"`
}

func releaseSubmit(ctx context.Context, env *tool.Env, in submitIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := app.Platform(in.Platform)
	snap, err := loadReleaseSnapshot(ctx, env, app.ID, platform, in.Version)
	if err != nil {
		return nil, err
	}
	if snap.Detail == nil {
		return nil, errNoVersion(app, platform, in.Version)
	}
	ver := snap.Detail.Version
	out := submitOut{App: app, Platform: platform, Version: prepareVersion{ID: ver.ID, String: ver.Str("versionString")}}
	if snap.Submission != nil {
		out.Submission = submissionView{ID: snap.Submission.ID, State: snap.Submission.Str("state")}
		if out.Submission.Items, err = fetchSubmissionItems(ctx, env, snap.Submission.ID); err != nil {
			return nil, err
		}
	}
	if st := out.Submission.State; st == "WAITING_FOR_REVIEW" || st == "IN_REVIEW" {
		return tool.Already(map[string]any{"status": "already_submitted", "submission": out.Submission}).
			Then(reviewStatusNext(app, platform)), nil
	}
	if _, ok := snap.attachedBuild(); !ok {
		return nil, tool.WithNext(fmt.Errorf("%w: version %s has no build; attach one with release_prepare", tool.ErrInvalidArgs, out.Version.String),
			statusNext(app, platform, out.Version.String, map[string]any{"build": "latest"}))
	}
	ss, err := loadStatusSnapshot(ctx, env, app, platform, in.Version)
	if err != nil {
		return nil, err
	}
	if err := preflightBlocks(runChecks(ss)); err != nil {
		return nil, tool.WithNext(err, statusNext(app, platform, out.Version.String, nil))
	}

	attach := in.AttachFirstTimeProducts == nil || *in.AttachFirstTimeProducts
	iaps, subs := in.IncludeIAPs, in.IncludeSubscriptions
	if attach {
		iaps = mergeRefs(iaps, ss.ReadyIAPs)
		subs = mergeRefs(subs, ss.ReadySubscriptions)
	}
	wanted := []wantedItem{{Kind: kindAppStoreVersion, Label: "version " + out.Version.String, Target: ver.ID}}
	products, notes, err := resolveProducts(ctx, env, app.ID, iaps, subs)
	if err != nil {
		return nil, err
	}
	wanted = append(wanted, products...)
	out.Notes = notes
	if !attach && len(ss.ReadyIAPs)+len(ss.ReadySubscriptions) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d ready product(s) not attached (attach_first_time_products:false)", len(ss.ReadyIAPs)+len(ss.ReadySubscriptions)))
	}
	var rejected []string
	for _, it := range out.Submission.Items {
		if it.State == "REJECTED" {
			rejected = append(rejected, it.ID)
		}
	}
	intended := []string{}
	if out.Submission.ID == "" {
		intended = append(intended, "create review submission")
	}
	for _, id := range rejected {
		intended = append(intended, "mark rejected item "+id+" resolved")
	}
	for i := range wanted {
		w := &wanted[i]
		switch {
		case w.Target != "" && slices.ContainsFunc(out.Submission.Items, func(it submissionItem) bool {
			return it.Target == w.Target && it.State != "REMOVED"
		}):
			w.Action = "kept"
		case w.Target == "":
			w.Action = "create_version_and_add"
			intended = append(intended, "create "+w.Kind+" for "+w.Label+" and add it")
		default:
			w.Action = "add"
			intended = append(intended, "add "+w.Label)
		}
	}
	intended = append(intended, "submit for App Review")

	key := submitKey{submitPlanKey: submitArgsKey(app, platform, out.Version.String), IAPs: in.IncludeIAPs, Subscriptions: in.IncludeSubscriptions, NoAutoAttach: !attach}
	fp, err := snap.State.fingerprint()
	if err != nil {
		return nil, err
	}
	wctx, res := tool.Preview(ctx, env, in.PlanID, key, fp, tool.PlanPreview{
		Summary:      fmt.Sprintf("submit %s %s %s for App Review", app.Name, platform, out.Version.String),
		Current:      out.Submission,
		Intended:     intended,
		Affected:     []string{app.ID, ver.ID},
		Irreversible: []string{"App Review starts; the version is locked until review ends or the submission is cancelled"},
	})
	if res != nil {
		return res, nil
	}

	subID := out.Submission.ID
	if subID == "" {
		resp, err := doWrite(wctx, env, http.MethodPost, "/v1/reviewSubmissions",
			jsonapiBody("reviewSubmissions", "", map[string]any{"platform": string(platform)}, map[string]string{"app": "apps:" + app.ID}))
		if err != nil {
			return nil, err
		}
		s, ok := resp.First()
		if !ok {
			return nil, fmt.Errorf("create review submission: empty response: %w", tool.ErrInternal)
		}
		subID, out.Created = s.ID, true
	}
	for _, id := range rejected {
		if _, err := doWrite(wctx, env, http.MethodPatch, "/v1/reviewSubmissionItems/"+id,
			jsonapiBody("reviewSubmissionItems", id, map[string]any{"resolved": true}, nil)); err != nil {
			return nil, err
		}
		out.Resolved = append(out.Resolved, id)
	}
	for i := range wanted {
		w := &wanted[i]
		if w.Action == "kept" {
			continue
		}
		if w.Target == "" {
			t, _, _ := strings.Cut(w.Create, ":")
			rel := strings.TrimSuffix(t, "s")
			resp, err := doWrite(wctx, env, http.MethodPost, "/v1/"+w.Kind+"s",
				jsonapiBody(w.Kind+"s", "", nil, map[string]string{rel: w.Create}))
			if err != nil {
				return nil, err
			}
			v, ok := resp.First()
			if !ok {
				return nil, fmt.Errorf("create %s: empty response: %w", w.Kind, tool.ErrInternal)
			}
			w.Target = v.ID
		}
		if _, err := doWrite(wctx, env, http.MethodPost, "/v1/reviewSubmissionItems",
			jsonapiBody("reviewSubmissionItems", "", nil, map[string]string{
				"reviewSubmission": "reviewSubmissions:" + subID,
				w.Kind:             w.Kind + "s:" + w.Target,
			})); err != nil {
			return nil, err
		}
		w.Action = "added"
	}
	out.Items = wanted
	resp, err := doWrite(wctx, env, http.MethodPatch, "/v1/reviewSubmissions/"+subID,
		jsonapiBody("reviewSubmissions", subID, map[string]any{"submitted": true}, nil))
	if err != nil {
		return nil, err
	}
	state := ""
	if s, ok := resp.First(); ok {
		state = s.Str("state")
	}
	if state == "" {
		// Read back a submission this call may just have created.
		rb, err := env.ASC.Get(ascrules.WithEventualConsistency(wctx), "/v1/reviewSubmissions/"+subID, url.Values{"fields[reviewSubmissions]": {"state"}})
		if err != nil {
			return nil, fmt.Errorf("read back review submission: %w", err)
		}
		if s, ok := rb.First(); ok {
			state = s.Str("state")
		}
	}
	out.Submission = submissionView{ID: subID, State: state, Items: out.Submission.Items}
	return tool.OK(out).Then(reviewStatusNext(app, platform)), nil
}

// preflightBlocks refuses a submission release_status would block, before
// anything is created at Apple.
func preflightBlocks(checks []check) error {
	var blocks []string
	for _, c := range checks {
		if c.Status != statusBlock {
			continue
		}
		msg := c.ID + ": " + c.Detail
		if len(c.Items) > 0 {
			msg += " (" + strings.Join(c.Items, "; ") + ")"
		}
		if c.FixVia != nil && c.FixVia.Tool != "" {
			msg += " [fix: " + c.FixVia.Tool + "]"
		} else if c.FixVia != nil && c.FixVia.Manual != "" {
			msg += " [fix: " + c.FixVia.Manual + "]"
		}
		blocks = append(blocks, msg)
	}
	if len(blocks) == 0 {
		return nil
	}
	return fmt.Errorf("%w: nothing was submitted, %d preflight check(s) block it: %s", tool.ErrInvalidArgs, len(blocks), strings.Join(blocks, " | "))
}

// mergeRefs appends the refs of extra not already in refs.
func mergeRefs(refs, extra []string) []string {
	out := slices.Clone(refs)
	for _, r := range extra {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// resolveProducts resolves IAP and subscription references to the open
// product versions to submit (empty Target = to be created).
func resolveProducts(ctx context.Context, env *tool.Env, appID string, iaps, subs []string) ([]wantedItem, []string, error) {
	var out []wantedItem
	var notes []string
	if len(iaps) > 0 {
		list, err := env.ASC.List(ctx, "/v1/apps/"+appID+"/inAppPurchasesV2", tool.ListOptions{
			Fields:   map[string][]string{"inAppPurchases": {"name", "productId", "state"}},
			MaxItems: productsMax,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("list in-app purchases: %w", err)
		}
		for _, ref := range iaps {
			p, err := matchProduct(list.Data, "in-app purchase", ref)
			if err != nil {
				return nil, nil, err
			}
			w, note, err := productVersion(ctx, env, kindIAPVersion, "/v2/inAppPurchases/"+p.ID+"/versions", "inAppPurchases:"+p.ID, productLabel(p))
			if err != nil {
				return nil, nil, err
			}
			out, notes = appendItem(out, notes, w, note)
		}
	}
	if len(subs) > 0 {
		groups, err := env.ASC.List(ctx, "/v1/apps/"+appID+"/subscriptionGroups", tool.ListOptions{
			Include:  []string{"subscriptions"},
			Query:    url.Values{"limit[subscriptions]": {"50"}},
			MaxItems: productsMax,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("list subscription groups: %w", err)
		}
		var all []tool.Resource
		groupOf := map[string]tool.Resource{}
		for _, g := range groups.Data {
			for _, id := range g.RelIDs("subscriptions") {
				groupOf[id] = g
			}
		}
		for _, r := range groups.Included {
			if r.Type == "subscriptions" {
				all = append(all, r)
			}
		}
		seenGroup := map[string]bool{}
		for _, ref := range subs {
			s, err := matchProduct(all, "subscription", ref)
			if err != nil {
				return nil, nil, err
			}
			if g, ok := groupOf[s.ID]; ok && !seenGroup[g.ID] {
				seenGroup[g.ID] = true
				w, note, err := groupVersion(ctx, env, g)
				if err != nil {
					return nil, nil, err
				}
				out, notes = appendItem(out, notes, w, note)
			}
			w, note, err := productVersion(ctx, env, kindSubVersion, "/v1/subscriptions/"+s.ID+"/versions", "subscriptions:"+s.ID, productLabel(s))
			if err != nil {
				return nil, nil, err
			}
			out, notes = appendItem(out, notes, w, note)
		}
	}
	return out, notes, nil
}

func appendItem(out []wantedItem, notes []string, w *wantedItem, note string) ([]wantedItem, []string) {
	if w != nil {
		out = append(out, *w)
	}
	if note != "" {
		notes = append(notes, note)
	}
	return out, notes
}

func productLabel(p tool.Resource) string {
	if id := p.Str("productId"); id != "" {
		return id
	}
	return p.Str("name")
}

// matchProduct finds ref by id, product id or case-insensitive name.
func matchProduct(rs []tool.Resource, kind, ref string) (tool.Resource, error) {
	var hits []tool.Resource
	for _, r := range rs {
		if r.ID == ref || r.Str("productId") == ref {
			return r, nil
		}
		if strings.EqualFold(r.Str("name"), ref) {
			hits = append(hits, r)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	return tool.Resource{}, fmt.Errorf("%w: %s %q matches %d products; pass the product id", tool.ErrInvalidArgs, kind, ref, len(hits))
}

// productVersion returns the open version of a product, nil when one is
// already in review, or an item whose version must be created.
func productVersion(ctx context.Context, env *tool.Env, kind, path, parent, label string) (*wantedItem, string, error) {
	resp, err := env.ASC.Get(ctx, path, url.Values{
		"filter[state]":         {strings.Join(append(slices.Clone(openItemStates), inReviewItemStates...), ",")},
		"fields[" + kind + "s]": {"version,state"},
	})
	if err != nil {
		return nil, "", fmt.Errorf("list %s versions: %w", label, err)
	}
	w := &wantedItem{Kind: kind, Label: label, Create: parent}
	for _, v := range resp.Data {
		switch {
		case slices.Contains(inReviewItemStates, v.Str("state")):
			return nil, label + " is already " + v.Str("state"), nil
		case slices.Contains(openItemStates, v.Str("state")):
			w.Target = v.ID
		}
	}
	return w, "", nil
}

// groupVersion returns the subscription group's version item when the group
// was never approved (a brand-new group must be reviewed too).
func groupVersion(ctx context.Context, env *tool.Env, g tool.Resource) (*wantedItem, string, error) {
	label := "group " + g.Str("referenceName")
	resp, err := env.ASC.Get(ctx, "/v1/subscriptionGroups/"+g.ID+"/versions", url.Values{
		"fields[subscriptionGroupVersions]": {"version,state"},
	})
	if err != nil {
		return nil, "", fmt.Errorf("list %s versions: %w", label, err)
	}
	w := &wantedItem{Kind: kindGroupVersion, Label: label, Create: "subscriptionGroups:" + g.ID}
	for _, v := range resp.Data {
		switch st := v.Str("state"); {
		case slices.Contains(inReviewItemStates, st):
			return nil, label + " is already " + st, nil
		case st == "APPROVED" || st == "ACCEPTED" || st == "REPLACED_WITH_NEW_VERSION":
			return nil, "", nil
		case slices.Contains(openItemStates, st):
			w.Target = v.ID
		}
	}
	return w, "", nil
}

type cancelOut struct {
	App        tool.App       `json:"app"`
	Platform   tool.Platform  `json:"platform"`
	Submission submissionView `json:"submission"`
	Action     string         `json:"action"`
	Removed    []string       `json:"removed_items,omitempty"`
}

func releaseCancel(ctx context.Context, env *tool.Env, in cancelIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := app.Platform(in.Platform)
	sub, ok, err := fetchOpenSubmission(ctx, env, app.ID, platform)
	if err != nil {
		return nil, err
	}
	if !ok {
		return tool.Already(map[string]any{"status": "no_open_submission"}).Then(reviewStatusNext(app, platform)), nil
	}
	items, err := fetchSubmissionItems(ctx, env, sub.ID)
	if err != nil {
		return nil, err
	}
	out := cancelOut{App: app, Platform: platform, Submission: submissionView{ID: sub.ID, State: sub.Str("state"), Items: items}}
	preview := tool.PlanPreview{Current: out.Submission, Affected: []string{app.ID, sub.ID}}
	var remove []submissionItem
	if out.Submission.State == "READY_FOR_REVIEW" {
		for _, it := range items {
			if it.State != "REMOVED" && (len(in.Items) == 0 || slices.Contains(in.Items, it.ID)) {
				remove = append(remove, it)
			}
		}
		if len(remove) == 0 {
			return nil, fmt.Errorf("%w: draft submission %s has no matching items to remove", tool.ErrInvalidArgs, sub.ID)
		}
		out.Action = "remove_draft_items"
		preview.Summary = fmt.Sprintf("remove %d item(s) from the unsent draft submission %s of %s", len(remove), sub.ID, app.Name)
		intended := make([]string, 0, len(remove))
		for _, it := range remove {
			intended = append(intended, "delete item "+it.ID+" ("+it.Kind+" "+it.Target+")")
		}
		preview.Intended = intended
	} else {
		if len(in.Items) > 0 {
			return nil, fmt.Errorf("%w: items applies only to a READY_FOR_REVIEW draft; a %s submission is canceled as a whole", tool.ErrInvalidArgs, out.Submission.State)
		}
		out.Action = "cancel"
		preview.Summary = fmt.Sprintf("cancel the %s review submission %s of %s", out.Submission.State, sub.ID, app.Name)
		preview.Intended = []string{"canceled:true (state becomes CANCELING)"}
		preview.Irreversible = []string{"the place in the App Review queue is lost; resubmitting starts over"}
	}
	wctx, res := tool.RequireConfirm(ctx, env, in.Confirm, preview)
	if res != nil {
		return res, nil
	}
	if out.Action == "cancel" {
		resp, err := doWrite(wctx, env, http.MethodPatch, "/v1/reviewSubmissions/"+sub.ID,
			jsonapiBody("reviewSubmissions", sub.ID, map[string]any{"canceled": true}, nil))
		if err != nil {
			return nil, err
		}
		out.Submission.State = "CANCELING"
		if s, ok := resp.First(); ok && s.Str("state") != "" {
			out.Submission.State = s.Str("state")
		}
		return tool.OK(out).Then(reviewStatusNext(app, platform)), nil
	}
	for _, it := range remove {
		if _, err := doWrite(wctx, env, http.MethodDelete, "/v1/reviewSubmissionItems/"+it.ID, nil); err != nil && !goneOK(err) {
			return nil, err
		}
		out.Removed = append(out.Removed, it.ID)
	}
	return tool.OK(out).Then(reviewStatusNext(app, platform)), nil
}
