package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// reviewIn is the App Review contact, demo account and notes of a version.
type reviewIn struct {
	ContactFirstName    *string `json:"contact_first_name,omitempty"`
	ContactLastName     *string `json:"contact_last_name,omitempty"`
	ContactPhone        *string `json:"contact_phone,omitempty"`
	ContactEmail        *string `json:"contact_email,omitempty"`
	DemoAccountRequired *bool   `json:"demo_account_required,omitempty"`
	DemoAccountName     *string `json:"demo_account_name,omitempty"`
	DemoAccountPassword *string `json:"demo_account_password,omitempty" jsonschema:"never echoed back"`
	Notes               *string `json:"notes,omitempty"`
}

// attrs maps the set fields to appStoreReviewDetails attributes.
func (r *reviewIn) attrs() map[string]any {
	a := map[string]any{}
	put := func(k string, v *string) {
		if v != nil {
			a[k] = *v
		}
	}
	put("contactFirstName", r.ContactFirstName)
	put("contactLastName", r.ContactLastName)
	put("contactPhone", r.ContactPhone)
	put("contactEmail", r.ContactEmail)
	put("demoAccountName", r.DemoAccountName)
	put("demoAccountPassword", r.DemoAccountPassword)
	put("notes", r.Notes)
	if r.DemoAccountRequired != nil {
		a["demoAccountRequired"] = *r.DemoAccountRequired
	}
	return a
}

type appinfoIn struct {
	tool.ConfirmFlag
	App               tool.AppRef     `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform          tool.Platform   `json:"platform,omitempty"`
	Version           tool.VersionRef `json:"version,omitempty" jsonschema:"editable (default) or a version string"`
	PrimaryCategory   string          `json:"primary_category,omitempty" jsonschema:"category id or name, e.g. PRODUCTIVITY"`
	SecondaryCategory *string         `json:"secondary_category,omitempty" jsonschema:"category id or name; empty string clears it"`
	AgeRating         map[string]any  `json:"age_rating,omitempty" jsonschema:"ageRatingDeclaration attributes, e.g. {\"gambling\":false}"`
	Review            *reviewIn       `json:"review,omitempty" jsonschema:"App Review contact, demo account and notes"`
	ContentRights     string          `json:"content_rights,omitempty"`
	Copyright         *string         `json:"copyright,omitempty"`
	Accessibility     *a11yIn         `json:"accessibility,omitempty" jsonschema:"Accessibility Nutrition Labels"`
	ReviewAttachments *attachIn       `json:"review_attachments,omitempty"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "appinfo_update", Toolset: tool.Metadata, Class: tool.Write, Confirm: tool.ConfirmByFlag,
		Title: "Update app info and review details",
		Description: "Set categories, age rating, a11y labels, App Review contact/demo/notes/attachments (add paths/" +
			"remove ids), content rights, copyright. Previews until confirm:true. Listing text: metadata_plan.",
		Enums: map[string][]any{
			"platform":                    platformEnum,
			"content_rights":              {"DOES_NOT_USE_THIRD_PARTY_CONTENT", "USES_THIRD_PARTY_CONTENT"},
			"accessibility.device_family": {"IPHONE", "IPAD", "APPLE_TV", "APPLE_WATCH", "MAC", "VISION"},
			"accessibility.supports":      a11yFeatureEnum,
		},
	}, appinfoUpdate))
}

// infoChange is one attribute change shown in the preview.
type infoChange struct {
	Target  string `json:"target"`
	ID      string `json:"id,omitempty"`
	Field   string `json:"field"`
	Current any    `json:"current"`
	New     any    `json:"new"`
}

// infoWrite is one request to send after confirmation.
type infoWrite struct {
	label string
	req   tool.Request
	// then, when set, builds a follow-up request from the created id.
	then func(createdID string) tool.Request
	// run, when set, replaces req (e.g. a multi-request upload) and
	// returns the created id.
	run func(ctx context.Context) (string, error)
	// attach is the review attachment path (add) or id (remove) this
	// write handles, if any.
	attach string
}

// infoItem reports one sent request.
type infoItem struct {
	Target  string `json:"target"`
	ID      string `json:"id,omitempty"`
	Op      string `json:"op"`
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
	Code    string `json:"code,omitempty"`
	// File is the local path of an uploaded review attachment.
	File string `json:"file,omitempty"`
}

type appinfoOut struct {
	Status  string       `json:"status"`
	App     appOut       `json:"app"`
	Version *versionOut  `json:"version,omitempty"`
	Changes []infoChange `json:"changes"`
	Items   []infoItem   `json:"items,omitempty"`
}

// maskedSecret replaces the demo account password in previews and results.
const maskedSecret = "(hidden)" //nolint:gosec // a mask, not a credential

func appinfoUpdate(ctx context.Context, env *tool.Env, in appinfoIn) (*tool.Result, error) {
	needInfo := in.PrimaryCategory != "" || in.SecondaryCategory != nil || len(in.AgeRating) > 0
	needVersion := in.Review != nil || in.Copyright != nil || in.ReviewAttachments != nil
	if !needInfo && !needVersion && in.ContentRights == "" && in.Accessibility == nil {
		return nil, fmt.Errorf("%w: nothing to update; pass categories, age_rating, review, review_attachments, content_rights, copyright or accessibility", tool.ErrInvalidArgs)
	}
	if in.Review != nil && in.Review.ContactEmail != nil && *in.Review.ContactEmail != "" {
		if err := tool.ValidEmail(*in.Review.ContactEmail); err != nil {
			return nil, err
		}
	}
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := app.Platform(in.Platform)
	out := appinfoOut{App: appOut{ID: app.ID, Name: tool.Untrusted(app.Name), BundleID: app.BundleID}, Changes: []infoChange{}}
	var writes []infoWrite

	if needInfo {
		w, ch, err := planAppInfo(ctx, env, app.ID, platform, in)
		if err != nil {
			return nil, err
		}
		writes, out.Changes = append(writes, w...), append(out.Changes, ch...)
	}
	if needVersion {
		v, err := env.Resolve.Version(ctx, app.ID, platform, in.Version)
		if err != nil {
			return nil, err
		}
		if v.ID == "" {
			return nil, tool.WithNext(&tool.ErrNotFound{Kind: "version", Ref: v.String, Hint: "create the version first"},
				tool.NextStep{Tool: "release_prepare", Args: map[string]any{"app": app.ID, "version": v.String}})
		}
		out.Version = &versionOut{ID: v.ID, Version: v.String, State: v.State}
		w, ch, err := planVersion(ctx, env, v.ID, in)
		if err != nil {
			return nil, err
		}
		writes, out.Changes = append(writes, w...), append(out.Changes, ch...)
	}
	if in.ContentRights != "" {
		resp, err := env.ASC.Get(ctx, "/v1/apps/"+app.ID, url.Values{"fields[apps]": {"contentRightsDeclaration"}})
		if err != nil {
			return nil, fmt.Errorf("read app: %w", err)
		}
		cur := ""
		if r, ok := resp.First(); ok {
			cur = r.Str("contentRightsDeclaration")
		}
		if cur != in.ContentRights {
			out.Changes = append(out.Changes, infoChange{Target: "app", ID: app.ID, Field: "contentRightsDeclaration", Current: nullable(cur), New: in.ContentRights})
			writes = append(writes, infoWrite{label: "app", req: patchReq("/v1/apps/"+app.ID, "apps", app.ID,
				map[string]any{"contentRightsDeclaration": in.ContentRights}, nil)})
		}
	}
	publish := false
	if in.Accessibility != nil {
		w, ch, pub, err := planAccessibility(ctx, env, app.ID, in.Accessibility)
		if err != nil {
			return nil, err
		}
		writes, out.Changes, publish = append(writes, w...), append(out.Changes, ch...), pub
	}

	if len(writes) == 0 {
		out.Status = "unchanged"
		return tool.Already(out), nil
	}
	for _, w := range writes {
		if w.run != nil {
			continue
		}
		if err := validate(env, w.req); err != nil {
			return nil, err
		}
	}
	affected := []string{app.ID}
	if out.Version != nil {
		affected = append(affected, out.Version.ID)
	}
	preview := tool.PlanPreview{
		Summary:  fmt.Sprintf("update %d app info field(s) of %s", len(out.Changes), app.Name),
		Current:  currentOf(out.Changes),
		Intended: out.Changes,
		Affected: affected,
	}
	gate := tool.RequireConfirm
	if publish {
		preview.Summary += "; publishes Accessibility Nutrition Labels on the public product page"
		gate = tool.RequireHumanConfirm
	}
	wctx, res := gate(ctx, env, in.Confirm, preview)
	if res != nil {
		return res, nil
	}
	var unknown, failed []error
	// done holds attachment paths uploaded and ids deleted, dropped from the
	// suggested re-run so it never duplicates them.
	done := map[string]bool{}
	for _, w := range writes {
		it := infoItem{Target: w.label, Op: strings.ToLower(w.req.Method), Outcome: "ok"}
		if w.run != nil {
			it.Op, it.File = "upload", w.attach
			id, err := w.run(wctx)
			it.ID = id
			if err == nil {
				done[w.attach] = true
			}
			if err != nil {
				it.Outcome, it.Error, it.Code = "error", err.Error(), tool.BodyFor(err).Error
				e := fmt.Errorf("%s: %w", w.label, err)
				if errors.Is(err, tool.ErrOutcomeUnknown) {
					unknown = append(unknown, e)
				} else {
					failed = append(failed, e)
				}
			}
			out.Items = append(out.Items, it)
			continue
		}
		if r, err := env.ASC.Do(wctx, w.req); err == nil && w.attach != "" {
			done[w.attach] = true
		} else if err != nil {
			it.Outcome, it.Error, it.Code = "error", err.Error(), tool.BodyFor(err).Error
			e := fmt.Errorf("%s: %w", w.label, err)
			if errors.Is(err, tool.ErrOutcomeUnknown) {
				unknown = append(unknown, e)
			} else {
				failed = append(failed, e)
			}
		} else if first, ok := r.First(); ok {
			it.ID = first.ID
			if w.then != nil {
				next := w.then(first.ID)
				nit := infoItem{Target: w.label, ID: first.ID, Op: "publish", Outcome: "ok"}
				if _, err := env.ASC.Do(wctx, next); err != nil {
					nit.Outcome, nit.Error, nit.Code = "error", err.Error(), tool.BodyFor(err).Error
					e := fmt.Errorf("%s publish: %w", w.label, err)
					if errors.Is(err, tool.ErrOutcomeUnknown) {
						unknown = append(unknown, e)
					} else {
						failed = append(failed, e)
					}
				}
				out.Items = append(out.Items, it, nit)
				continue
			}
		}
		out.Items = append(out.Items, it)
	}
	nFailed := len(unknown) + len(failed)
	if nFailed == 0 {
		out.Status = "applied"
		return tool.OK(&out), nil
	}
	replan := tool.NextStep{Tool: "appinfo_update", Args: appinfoReplanArgs(withoutDone(in, done), app.ID)}
	if nFailed == len(out.Items) {
		// Outcome-unknown items first, so the result reports outcome_unknown
		// whenever any request may have been applied.
		return tool.Batch(&out, 0, 0, append(unknown, failed...), replan), nil
	}
	out.Status = "partial"
	return tool.OK(&out).Warn(fmt.Sprintf("%d request(s) failed; call again without confirm to see what remains", nFailed)).Then(replan), nil
}

// appinfoReplanArgs is in without confirm and the demo password (never
// echoed back; the agent passes it again).
func appinfoReplanArgs(in appinfoIn, appID string) map[string]any {
	in.Confirm, in.App = false, tool.AppRef(appID)
	b, err := json.Marshal(in)
	args := map[string]any{}
	if err != nil || json.Unmarshal(b, &args) != nil {
		return map[string]any{"app": appID}
	}
	if rv, ok := args["review"].(map[string]any); ok {
		delete(rv, "demo_account_password")
	}
	return args
}

// planAppInfo diffs categories and the age rating declaration of the
// editable app info.
func planAppInfo(ctx context.Context, env *tool.Env, appID string, platform tool.Platform, in appinfoIn) ([]infoWrite, []infoChange, error) {
	infos, err := appInfos(ctx, env, appID, "ageRatingDeclaration,primaryCategory,secondaryCategory")
	if err != nil {
		return nil, nil, err
	}
	editable, included := infos.Editable, infos.Included
	if editable == nil {
		if st := infos.lockedState(); st != "" {
			return nil, nil, tool.WithNext(fmt.Errorf("%w: app info is %s, so categories and age rating can't change until review ends", tool.ErrInvalidArgs, st),
				tool.NextStep{Tool: "release_review_status", Args: map[string]any{"app": appID}})
		}
		return nil, nil, tool.WithNext(fmt.Errorf("%w: categories and age rating can change only while a new version is being prepared", tool.ErrInvalidArgs),
			tool.NextStep{Tool: "release_prepare", Args: map[string]any{"app": appID}})
	}
	var (
		writes  []infoWrite
		changes []infoChange
	)
	rels := map[string]any{}
	if in.PrimaryCategory != "" || in.SecondaryCategory != nil {
		cats, err := categories(ctx, env, platform)
		if err != nil {
			return nil, nil, err
		}
		set := func(rel, want string) error {
			cur := editable.RelID(rel)
			id := ""
			if want != "" {
				if id, err = matchCategory(cats, want); err != nil {
					return err
				}
			}
			if id == cur {
				return nil
			}
			changes = append(changes, infoChange{Target: "appInfo", ID: editable.ID, Field: rel, Current: nullable(cur), New: nullable(id)})
			if id == "" {
				rels[rel] = map[string]any{"data": nil}
			} else {
				rels[rel] = map[string]any{"data": map[string]any{"type": "appCategories", "id": id}}
			}
			return nil
		}
		if in.PrimaryCategory != "" {
			if err := set("primaryCategory", in.PrimaryCategory); err != nil {
				return nil, nil, err
			}
		}
		if in.SecondaryCategory != nil {
			if err := set("secondaryCategory", *in.SecondaryCategory); err != nil {
				return nil, nil, err
			}
		}
	}
	if len(rels) > 0 {
		writes = append(writes, infoWrite{label: "appInfo", req: patchReq("/v1/appInfos/"+editable.ID, "appInfos", editable.ID, nil, rels)})
	}
	if len(in.AgeRating) > 0 {
		ageID := editable.RelID("ageRatingDeclaration")
		if ageID == "" {
			return nil, nil, fmt.Errorf("%w: app info %s has no age rating declaration", tool.ErrUnavailable, editable.ID)
		}
		var cur tool.Resource
		for _, r := range included {
			if r.Type == "ageRatingDeclarations" && r.ID == ageID {
				cur = r
			}
		}
		attrs := map[string]any{}
		for _, k := range slices.Sorted(maps.Keys(in.AgeRating)) {
			if old, ok := cur.Attributes[k]; ok && reflect.DeepEqual(old, in.AgeRating[k]) {
				continue
			}
			attrs[k] = in.AgeRating[k]
			changes = append(changes, infoChange{Target: "ageRatingDeclaration", ID: ageID, Field: k, Current: cur.Attributes[k], New: in.AgeRating[k]})
		}
		if len(attrs) > 0 {
			writes = append(writes, infoWrite{label: "ageRatingDeclaration", req: patchReq("/v1/ageRatingDeclarations/"+ageID, "ageRatingDeclarations", ageID, attrs, nil)})
		}
	}
	return writes, changes, nil
}

// planVersion diffs the review detail and copyright of version id.
func planVersion(ctx context.Context, env *tool.Env, versionID string, in appinfoIn) ([]infoWrite, []infoChange, error) {
	resp, err := env.ASC.Get(ctx, "/v1/appStoreVersions/"+versionID, url.Values{"include": {"appStoreReviewDetail"}})
	if err != nil {
		return nil, nil, fmt.Errorf("read version: %w", err)
	}
	ver, ok := resp.First()
	if !ok {
		return nil, nil, fmt.Errorf("%w: version %s returned no data", tool.ErrInternal, versionID)
	}
	var (
		writes  []infoWrite
		changes []infoChange
	)
	if in.Copyright != nil {
		if cur := attrStr(ver.Attributes["copyright"]); cur != *in.Copyright {
			changes = append(changes, infoChange{Target: "appStoreVersion", ID: versionID, Field: "copyright", Current: nullable(cur), New: *in.Copyright})
			writes = append(writes, infoWrite{label: "appStoreVersion", req: patchReq("/v1/appStoreVersions/"+versionID, "appStoreVersions", versionID,
				map[string]any{"copyright": *in.Copyright}, nil)})
		}
	}
	detailID := ver.RelID("appStoreReviewDetail")
	if in.ReviewAttachments != nil {
		w, ch, err := planAttachments(ctx, env, versionID, detailID, in.ReviewAttachments)
		if err != nil {
			return nil, nil, err
		}
		writes, changes = append(writes, w...), append(changes, ch...)
	}
	if in.Review == nil {
		return writes, changes, nil
	}
	var detail tool.Resource
	for _, r := range resp.Included {
		if r.Type == "appStoreReviewDetails" && r.ID == detailID {
			detail = r
		}
	}
	want := in.Review.attrs()
	attrs := map[string]any{}
	for _, k := range slices.Sorted(maps.Keys(want)) {
		old, had := detail.Attributes[k]
		if s, isStr := want[k].(string); isStr {
			if had && attrStr(old) == s {
				continue
			}
			old = nullable(attrStr(old))
		} else if had && old == want[k] {
			continue
		}
		attrs[k] = want[k]
		shownOld, shownNew := old, want[k]
		if k == "demoAccountPassword" {
			if shownOld != nil {
				shownOld = maskedSecret
			}
			shownNew = maskedSecret
		}
		changes = append(changes, infoChange{Target: "appStoreReviewDetail", ID: detailID, Field: k, Current: shownOld, New: shownNew})
	}
	if len(attrs) == 0 {
		return writes, changes, nil
	}
	if detailID == "" {
		writes = append(writes, infoWrite{label: "appStoreReviewDetail", req: tool.Request{
			Method: http.MethodPost, Path: "/v1/appStoreReviewDetails", Write: true,
			Body: map[string]any{"data": map[string]any{
				"type": "appStoreReviewDetails", "attributes": attrs,
				"relationships": map[string]any{"appStoreVersion": map[string]any{"data": map[string]any{"type": "appStoreVersions", "id": versionID}}},
			}},
		}})
	} else {
		writes = append(writes, infoWrite{label: "appStoreReviewDetail", req: patchReq("/v1/appStoreReviewDetails/"+detailID, "appStoreReviewDetails", detailID, attrs, nil)})
	}
	return writes, changes, nil
}

// categories lists the top-level App Store categories of platform.
func categories(ctx context.Context, env *tool.Env, platform tool.Platform) ([]string, error) {
	res, err := env.ASC.List(ctx, "/v1/appCategories", tool.ListOptions{
		Query: url.Values{"filter[platforms]": {string(platform)}, "exists[parent]": {"false"}},
		Limit: 200, MaxItems: 500,
	})
	if err != nil {
		return nil, fmt.Errorf("list app categories: %w", err)
	}
	ids := make([]string, 0, len(res.Data))
	for _, r := range res.Data {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	return ids, nil
}

// matchCategory maps a category id or display name ("Health & Fitness") to
// its id.
func matchCategory(ids []string, want string) (string, error) {
	norm := strings.ToUpper(strings.TrimSpace(want))
	norm = strings.NewReplacer("&", "AND", " ", "_", "-", "_").Replace(norm)
	for strings.Contains(norm, "__") {
		norm = strings.ReplaceAll(norm, "__", "_")
	}
	if slices.Contains(ids, norm) {
		return norm, nil
	}
	return "", fmt.Errorf("%w: unknown category %q; valid: %s", tool.ErrInvalidArgs, want, strings.Join(ids, ", "))
}

// patchReq builds a JSON:API PATCH request.
func patchReq(path, typ, id string, attrs, rels map[string]any) tool.Request {
	data := map[string]any{"type": typ, "id": id}
	if len(attrs) > 0 {
		data["attributes"] = attrs
	}
	if len(rels) > 0 {
		data["relationships"] = rels
	}
	return tool.Request{Method: http.MethodPatch, Path: path, Body: map[string]any{"data": data}, Write: true}
}

// validate checks req against the spec, turning violations into
// invalid-argument errors before anything is previewed or sent.
func validate(env *tool.Env, req tool.Request) error {
	var ve *tool.ValidationError
	if err := env.Spec.Validate(req); errors.As(err, &ve) {
		return fmt.Errorf("%w: %s %s: %s", tool.ErrInvalidArgs, req.Method, req.Path, strings.Join(ve.Problems, "; "))
	}
	return nil
}

func currentOf(changes []infoChange) map[string]any {
	cur := make(map[string]any, len(changes))
	for _, c := range changes {
		cur[c.Target+"."+c.Field] = c.Current
	}
	return cur
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
