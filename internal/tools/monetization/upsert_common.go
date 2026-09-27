package monetization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/upload"
)

// Per-item outcome actions shared by iap_upsert and subscriptions_upsert.
const (
	actionCreated   = "created"
	actionUpdated   = "updated"
	actionUnchanged = "unchanged"
	actionAlready   = "already"
	actionSkipped   = "skipped"
	actionFailed    = "failed"
)

// openVersionStates are the product/group version states that still
// accept localization edits.
const openVersionStates = "PREPARE_FOR_SUBMISSION,READY_FOR_REVIEW,REJECTED,DEVELOPER_REJECTED"

// productIDRE is Apple's product id alphabet.
var productIDRE = regexp.MustCompile(`^[A-Za-z0-9._]{1,100}$`)

// locIn is one product localization (display name and description).
type locIn struct {
	Name        string `json:"name" jsonschema:"display name shown to customers"`
	Description string `json:"description,omitempty"`
}

// upsertItem is the outcome of one object written by an upsert.
type upsertItem struct {
	Kind    string   `json:"kind"`
	ID      string   `json:"id,omitempty"`
	Locale  string   `json:"locale,omitempty"`
	Action  string   `json:"action"`
	Changed []string `json:"changed,omitempty"`
	Note    string   `json:"note,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// versionOut describes the version localizations were written to.
type versionOut struct {
	ID      string `json:"id"`
	State   string `json:"state,omitempty"`
	Created bool   `json:"created,omitempty"`
}

// versionKind describes a versioned product family (IAP, subscription,
// subscription group): where its versions and version localizations live.
type versionKind struct {
	ListPath    string // versions of a parent, with %s for the parent id
	CreatePath  string // POST collection for a new version
	Type        string // version resource type
	ParentRel   string // version → parent relationship name
	ParentType  string // parent resource type
	LocListPath string // localizations of a version, with %s for the version id
	LocPath     string // POST collection of version localizations (v2)
	LocType     string // localization resource type
	LocFields   []string
}

var (
	iapVersions = versionKind{
		ListPath: "/v2/inAppPurchases/%s/versions", CreatePath: "/v1/inAppPurchaseVersions",
		Type: "inAppPurchaseVersions", ParentRel: "inAppPurchase", ParentType: "inAppPurchases",
		LocListPath: "/v1/inAppPurchaseVersions/%s/localizations", LocPath: "/v2/inAppPurchaseLocalizations",
		LocType: "inAppPurchaseLocalizations", LocFields: []string{"locale", "name", "description"},
	}
	subVersions = versionKind{
		ListPath: "/v1/subscriptions/%s/versions", CreatePath: "/v1/subscriptionVersions",
		Type: "subscriptionVersions", ParentRel: "subscription", ParentType: "subscriptions",
		LocListPath: "/v1/subscriptionVersions/%s/localizations", LocPath: "/v2/subscriptionLocalizations",
		LocType: "subscriptionLocalizations", LocFields: []string{"locale", "name", "description"},
	}
	groupVersions = versionKind{
		ListPath: "/v1/subscriptionGroups/%s/versions", CreatePath: "/v1/subscriptionGroupVersions",
		Type: "subscriptionGroupVersions", ParentRel: "subscriptionGroup", ParentType: "subscriptionGroups",
		LocListPath: "/v1/subscriptionGroupVersions/%s/localizations", LocPath: "/v2/subscriptionGroupLocalizations",
		LocType: "subscriptionGroupLocalizations", LocFields: []string{"locale", "name", "customAppName"},
	}
)

// invalidArgs wraps a validation problem as invalid_arguments.
func invalidArgs(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{tool.ErrInvalidArgs}, args...)...)
}

// checkProductID validates an App Store product id.
func checkProductID(id string) error {
	if !productIDRE.MatchString(id) {
		return invalidArgs("product_id %q must be 1-100 letters, digits, '.' or '_'", id)
	}
	return nil
}

// checkLimit applies an applemeta length limit to a named argument.
func checkLimit(arg string, f applemeta.Field, v string) error {
	if err := applemeta.Check(f, v); err != nil {
		return invalidArgs("%s: %v", arg, err)
	}
	return nil
}

// normLocs normalizes locale keys and validates name/description limits.
// It returns the canonical attribute sets keyed by ASC locale code.
func normLocs(arg string, in map[string]locIn) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any, len(in))
	for k, l := range in {
		loc, err := applemeta.NormalizeLocale(k)
		if err != nil {
			return nil, invalidArgs("%s: %v", arg, err)
		}
		if _, dup := out[loc]; dup {
			return nil, invalidArgs("%s: locale %s given twice", arg, loc)
		}
		if strings.TrimSpace(l.Name) == "" {
			return nil, invalidArgs("%s[%s].name is required", arg, loc)
		}
		if err := checkLimit(arg+"["+loc+"].name", applemeta.FieldIAPDisplayName, l.Name); err != nil {
			return nil, err
		}
		attrs := map[string]any{"name": l.Name}
		if l.Description != "" {
			if err := checkLimit(arg+"["+loc+"].description", applemeta.FieldIAPDescription, l.Description); err != nil {
				return nil, err
			}
			attrs["description"] = l.Description
		}
		out[loc] = attrs
	}
	return out, nil
}

// checkScreenshot validates a review screenshot path before any request.
func checkScreenshot(env *tool.Env, path string) error {
	if path == "" {
		return nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg":
	default:
		return invalidArgs("review_screenshot %q must be a .png or .jpg file", path)
	}
	fi, err := env.Files.Stat(path)
	if err != nil {
		return invalidArgs("review_screenshot: %v", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() == 0 {
		return invalidArgs("review_screenshot %q is not a non-empty file", path)
	}
	return nil
}

// resourceBody builds a JSON:API request document. rels maps a relationship name
// to {type, id}.
func resourceBody(typ, id string, attrs map[string]any, rels map[string][2]string) map[string]any {
	data := map[string]any{"type": typ}
	if id != "" {
		data["id"] = id
	}
	if len(attrs) > 0 {
		data["attributes"] = attrs
	}
	if len(rels) > 0 {
		r := make(map[string]any, len(rels))
		for name, ti := range rels {
			r[name] = map[string]any{"data": map[string]any{"type": ti[0], "id": ti[1]}}
		}
		data["relationships"] = r
	}
	return map[string]any{"data": data}
}

// isConflict reports a 409 from Apple (the object already exists).
func isConflict(err error) bool {
	var ae *tool.APIError
	return errors.As(err, &ae) && ae.Status == http.StatusConflict
}

// sameValue reports whether a remote attribute equals the wanted value
// (compared as JSON so 1 and 1.0 match). Text the client wrapped as
// [tool.Untrusted] is compared unwrapped.
func sameValue(remote, want any) bool {
	a, err1 := json.Marshal(tool.Unwrap(remote))
	b, err2 := json.Marshal(want)
	return err1 == nil && err2 == nil && string(a) == string(b)
}

// attrDiff returns the wanted attributes that differ from r, and their
// sorted names. Family sharing can never be turned off once enabled.
func attrDiff(r tool.Resource, want map[string]any) (map[string]any, []string, error) {
	if v, ok := want["familySharable"].(bool); ok && !v {
		if cur, ok := r.Bool("familySharable"); ok && cur {
			return nil, nil, invalidArgs("family sharing cannot be turned off once enabled (%s %s)", r.Type, r.ID)
		}
	}
	changed := map[string]any{}
	var names []string
	for k, v := range want {
		if !sameValue(r.Attributes[k], v) {
			changed[k] = v
			names = append(names, k)
		}
	}
	slices.Sort(names)
	return changed, names, nil
}

// patch updates attrs of one resource. ctx must be the write context returned
// by the tool's confirm gate (tool.RequireConfirm / RequireHumanConfirm).
func patch(ctx context.Context, env *tool.Env, path, typ, id string, attrs map[string]any) error {
	_, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPatch, Path: path, Write: true, Body: resourceBody(typ, id, attrs, nil)})
	return err
}

// postResource creates a resource and returns it.
func postResource(ctx context.Context, env *tool.Env, path, typ string, attrs map[string]any, rels map[string][2]string) (tool.Resource, error) {
	resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: path, Write: true, Body: resourceBody(typ, "", attrs, rels)})
	if err != nil {
		return tool.Resource{}, err
	}
	r, ok := resp.First()
	if !ok || r.ID == "" {
		return tool.Resource{}, fmt.Errorf("create %s: response has no resource: %w", typ, tool.ErrOutcomeUnknown)
	}
	return r, nil
}

// indexIncluded indexes included resources by type and id.
func indexIncluded(inc []tool.Resource) map[string]tool.Resource {
	m := make(map[string]tool.Resource, len(inc))
	for _, r := range inc {
		m[r.Type+"/"+r.ID] = r
	}
	return m
}

// related returns the included resources of r's to-many relationship.
func related(r tool.Resource, rel, typ string, inc map[string]tool.Resource) []tool.Resource {
	var out []tool.Resource
	for _, id := range r.RelIDs(rel) {
		if x, ok := inc[typ+"/"+id]; ok {
			out = append(out, x)
		}
	}
	return out
}

// latestVersion returns the highest-numbered version among vs.
func latestVersion(vs []tool.Resource) (tool.Resource, bool) {
	var best tool.Resource
	found, bestN := false, -1
	for _, v := range vs {
		if n, _ := v.Int("version"); !found || n > bestN {
			best, bestN, found = v, n, true
		}
	}
	return best, found
}

// openVersion finds the parent's editable version (with its
// localizations) or creates one. Localizations of a newly created version are
// read back, because Apple may copy them from the previous version.
func openVersion(ctx context.Context, env *tool.Env, k versionKind, parentID string) (versionOut, []tool.Resource, error) {
	q := url.Values{
		"filter[state]":             {openVersionStates},
		"include":                   {"localizations"},
		"fields[" + k.Type + "]":    {"version,state,localizations"},
		"fields[" + k.LocType + "]": {strings.Join(k.LocFields, ",")},
		"limit[localizations]":      {"50"},
	}
	resp, err := env.ASC.Get(ctx, fmt.Sprintf(k.ListPath, url.PathEscape(parentID)), q)
	if err != nil {
		return versionOut{}, nil, fmt.Errorf("read %s of %s: %w", k.Type, parentID, err)
	}
	if v, ok := latestVersion(resp.Data); ok {
		locs := related(v, "localizations", k.LocType, indexIncluded(resp.Included))
		return versionOut{ID: v.ID, State: v.Str("state")}, locs, nil
	}
	v, err := postResource(ctx, env, k.CreatePath, k.Type, nil, map[string][2]string{k.ParentRel: {k.ParentType, parentID}})
	if err != nil {
		return versionOut{}, nil, fmt.Errorf("create %s for %s: %w", k.Type, parentID, err)
	}
	out := versionOut{ID: v.ID, State: v.Str("state"), Created: true}
	lr, err := env.ASC.Get(ctx, fmt.Sprintf(k.LocListPath, url.PathEscape(v.ID)), url.Values{
		"fields[" + k.LocType + "]": {strings.Join(k.LocFields, ",")},
	})
	if err != nil {
		return out, nil, fmt.Errorf("read localizations of %s %s: %w", k.Type, v.ID, err)
	}
	return out, lr.Data, nil
}

// upsertLocs writes the wanted localizations on a version: PATCH the
// changed fields of existing locales, POST missing ones. Locales absent from
// want are never touched. Failures are recorded per item.
func upsertLocs(ctx context.Context, env *tool.Env, k versionKind, versionID string, existing []tool.Resource, want map[string]map[string]any) []upsertItem {
	byLocale := make(map[string]tool.Resource, len(existing))
	for _, r := range existing {
		byLocale[r.Str("locale")] = r
	}
	locales := make([]string, 0, len(want))
	for l := range want {
		locales = append(locales, l)
	}
	slices.Sort(locales)
	items := make([]upsertItem, 0, len(locales))
	for _, loc := range locales {
		attrs := want[loc]
		it := upsertItem{Kind: k.LocType, Locale: loc}
		if cur, ok := byLocale[loc]; ok {
			it.ID = cur.ID
			changed, names, err := attrDiff(cur, attrs)
			switch {
			case err != nil:
				it.Action, it.Error = actionFailed, err.Error()
			case len(changed) == 0:
				it.Action = actionUnchanged
			default:
				if err := patch(ctx, env, k.LocPath+"/"+url.PathEscape(cur.ID), k.LocType, cur.ID, changed); err != nil {
					it.Action, it.Error = actionFailed, err.Error()
				} else {
					it.Action, it.Changed = actionUpdated, names
				}
			}
			items = append(items, it)
			continue
		}
		body := map[string]any{"locale": loc}
		for a, v := range attrs {
			body[a] = v
		}
		r, err := postResource(ctx, env, k.LocPath, k.LocType, body, map[string][2]string{"version": {k.Type, versionID}})
		switch {
		case err == nil:
			it.ID, it.Action = r.ID, actionCreated
		case isConflict(err):
			it.Action, it.Note = actionAlready, "Apple reports this locale exists; rerun to update it"
		default:
			it.Action, it.Error = actionFailed, err.Error()
		}
		items = append(items, it)
	}
	return items
}

// screenshotKind describes where a product's review screenshot lives.
type screenshotKind struct {
	CreatePath string
	Type       string
	ParentRel  string
	ParentType string
}

var (
	iapShot = screenshotKind{
		CreatePath: "/v1/inAppPurchaseAppStoreReviewScreenshots",
		Type:       "inAppPurchaseAppStoreReviewScreenshots", ParentRel: "inAppPurchaseV2", ParentType: "inAppPurchases",
	}
	subShot = screenshotKind{
		CreatePath: "/v1/subscriptionAppStoreReviewScreenshots",
		Type:       "subscriptionAppStoreReviewScreenshots", ParentRel: "subscription", ParentType: "subscriptions",
	}
)

// shotFields are the sparse fields read for an existing review screenshot.
const shotFields = "fileName,sourceFileChecksum,assetDeliveryState"

// uploadScreenshot uploads the review screenshot at path unless the current one
// (cur, may be zero) has the same checksum. A different existing screenshot is
// never replaced: replacement deletes Apple's copy, which needs an explicit
// DELETE the user confirms.
func uploadScreenshot(ctx context.Context, env *tool.Env, k screenshotKind, parentID, path string, cur tool.Resource) upsertItem {
	it := upsertItem{Kind: k.Type}
	f, err := env.Files.Open(path)
	if err != nil {
		it.Action, it.Error = actionFailed, err.Error()
		return it
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		it.Action, it.Error = actionFailed, err.Error()
		return it
	}
	sum, err := upload.FileMD5(io.NewSectionReader(f, 0, fi.Size()))
	if err != nil {
		it.Action, it.Error = actionFailed, err.Error()
		return it
	}
	if cur.ID != "" {
		it.ID = cur.ID
		if strings.EqualFold(cur.Str("sourceFileChecksum"), sum) {
			it.Action = actionUnchanged
			return it
		}
		it.Action = actionSkipped
		it.Note = fmt.Sprintf("a different review screenshot (%s) exists; delete it with asc_api_request DELETE %s/%s, then rerun", cur.Str("fileName"), k.CreatePath, cur.ID)
		return it
	}
	res, err := upload.Asset(ctx, upload.DepsFromEnv(env), upload.AssetSpec{
		CreatePath: k.CreatePath, Type: k.Type,
		Parent:   upload.Relationship{Name: k.ParentRel, Type: k.ParentType, ID: parentID},
		FileName: filepath.Base(path), FileSize: fi.Size(), File: f,
	})
	if res != nil {
		it.ID = res.ID
	}
	switch {
	case err != nil:
		it.Action, it.Error = actionFailed, err.Error()
	case res.StillProcessing:
		it.Action, it.Note = actionCreated, "uploaded; Apple is still processing it"
	default:
		it.Action = actionCreated
	}
	return it
}

// allUnchanged reports whether no item changed anything remotely.
func allUnchanged(items []upsertItem) bool {
	for _, it := range items {
		if it.Action != actionUnchanged && it.Action != actionAlready {
			return false
		}
	}
	return true
}

// failures counts failed items.
func failures(items []upsertItem) int {
	n := 0
	for _, it := range items {
		if it.Action == actionFailed {
			n++
		}
	}
	return n
}
