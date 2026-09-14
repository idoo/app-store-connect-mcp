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
	s12aCreated   = "created"
	s12aUpdated   = "updated"
	s12aUnchanged = "unchanged"
	s12aAlready   = "already"
	s12aSkipped   = "skipped"
	s12aFailed    = "failed"
)

// s12aOpenVersionStates are the product/group version states that still
// accept localization edits.
const s12aOpenVersionStates = "PREPARE_FOR_SUBMISSION,READY_FOR_REVIEW,REJECTED,DEVELOPER_REJECTED"

// s12aProductIDRE is Apple's product id alphabet.
var s12aProductIDRE = regexp.MustCompile(`^[A-Za-z0-9._]{1,100}$`)

// s12aLocIn is one product localization (display name and description).
type s12aLocIn struct {
	Name        string `json:"name" jsonschema:"display name shown to customers"`
	Description string `json:"description,omitempty"`
}

// s12aItem is the outcome of one object written by an upsert.
type s12aItem struct {
	Kind    string   `json:"kind"`
	ID      string   `json:"id,omitempty"`
	Locale  string   `json:"locale,omitempty"`
	Action  string   `json:"action"`
	Changed []string `json:"changed,omitempty"`
	Note    string   `json:"note,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// s12aVersionOut describes the version localizations were written to.
type s12aVersionOut struct {
	ID      string `json:"id"`
	State   string `json:"state,omitempty"`
	Created bool   `json:"created,omitempty"`
}

// s12aVersionKind describes a versioned product family (IAP, subscription,
// subscription group): where its versions and version localizations live.
type s12aVersionKind struct {
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
	s12aIAPVersions = s12aVersionKind{
		ListPath: "/v2/inAppPurchases/%s/versions", CreatePath: "/v1/inAppPurchaseVersions",
		Type: "inAppPurchaseVersions", ParentRel: "inAppPurchase", ParentType: "inAppPurchases",
		LocListPath: "/v1/inAppPurchaseVersions/%s/localizations", LocPath: "/v2/inAppPurchaseLocalizations",
		LocType: "inAppPurchaseLocalizations", LocFields: []string{"locale", "name", "description"},
	}
	s12aSubVersions = s12aVersionKind{
		ListPath: "/v1/subscriptions/%s/versions", CreatePath: "/v1/subscriptionVersions",
		Type: "subscriptionVersions", ParentRel: "subscription", ParentType: "subscriptions",
		LocListPath: "/v1/subscriptionVersions/%s/localizations", LocPath: "/v2/subscriptionLocalizations",
		LocType: "subscriptionLocalizations", LocFields: []string{"locale", "name", "description"},
	}
	s12aGroupVersions = s12aVersionKind{
		ListPath: "/v1/subscriptionGroups/%s/versions", CreatePath: "/v1/subscriptionGroupVersions",
		Type: "subscriptionGroupVersions", ParentRel: "subscriptionGroup", ParentType: "subscriptionGroups",
		LocListPath: "/v1/subscriptionGroupVersions/%s/localizations", LocPath: "/v2/subscriptionGroupLocalizations",
		LocType: "subscriptionGroupLocalizations", LocFields: []string{"locale", "name", "customAppName"},
	}
)

// s12aInvalid wraps a validation problem as invalid_arguments.
func s12aInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{tool.ErrInvalidArgs}, args...)...)
}

// s12aCheckProductID validates an App Store product id.
func s12aCheckProductID(id string) error {
	if !s12aProductIDRE.MatchString(id) {
		return s12aInvalid("product_id %q must be 1-100 letters, digits, '.' or '_'", id)
	}
	return nil
}

// s12aCheckLimit applies an applemeta length limit to a named argument.
func s12aCheckLimit(arg string, f applemeta.Field, v string) error {
	if err := applemeta.Check(f, v); err != nil {
		return s12aInvalid("%s: %v", arg, err)
	}
	return nil
}

// s12aNormLocs normalizes locale keys and validates name/description limits.
// It returns the canonical attribute sets keyed by ASC locale code.
func s12aNormLocs(arg string, in map[string]s12aLocIn) (map[string]map[string]any, error) {
	out := make(map[string]map[string]any, len(in))
	for k, l := range in {
		loc, err := applemeta.NormalizeLocale(k)
		if err != nil {
			return nil, s12aInvalid("%s: %v", arg, err)
		}
		if _, dup := out[loc]; dup {
			return nil, s12aInvalid("%s: locale %s given twice", arg, loc)
		}
		if strings.TrimSpace(l.Name) == "" {
			return nil, s12aInvalid("%s[%s].name is required", arg, loc)
		}
		if err := s12aCheckLimit(arg+"["+loc+"].name", applemeta.FieldIAPDisplayName, l.Name); err != nil {
			return nil, err
		}
		attrs := map[string]any{"name": l.Name}
		if l.Description != "" {
			if err := s12aCheckLimit(arg+"["+loc+"].description", applemeta.FieldIAPDescription, l.Description); err != nil {
				return nil, err
			}
			attrs["description"] = l.Description
		}
		out[loc] = attrs
	}
	return out, nil
}

// s12aCheckScreenshot validates a review screenshot path before any request.
func s12aCheckScreenshot(env *tool.Env, path string) error {
	if path == "" {
		return nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg":
	default:
		return s12aInvalid("review_screenshot %q must be a .png or .jpg file", path)
	}
	fi, err := env.Files.Stat(path)
	if err != nil {
		return s12aInvalid("review_screenshot: %v", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() == 0 {
		return s12aInvalid("review_screenshot %q is not a non-empty file", path)
	}
	return nil
}

// s12aBody builds a JSON:API request document. rels maps a relationship name
// to {type, id}.
func s12aBody(typ, id string, attrs map[string]any, rels map[string][2]string) map[string]any {
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

// s12aConflict reports a 409 from Apple (the object already exists).
func s12aConflict(err error) bool {
	var ae *tool.APIError
	return errors.As(err, &ae) && ae.Status == http.StatusConflict
}

// s12aSame reports whether a remote attribute equals the wanted value
// (compared as JSON so 1 and 1.0 match). Text the client wrapped as
// [tool.Untrusted] is compared unwrapped.
func s12aSame(remote, want any) bool {
	a, err1 := json.Marshal(tool.Unwrap(remote))
	b, err2 := json.Marshal(want)
	return err1 == nil && err2 == nil && string(a) == string(b)
}

// s12aDiff returns the wanted attributes that differ from r, and their
// sorted names. Family sharing can never be turned off once enabled.
func s12aDiff(r tool.Resource, want map[string]any) (map[string]any, []string, error) {
	if v, ok := want["familySharable"].(bool); ok && !v {
		if cur, ok := r.Bool("familySharable"); ok && cur {
			return nil, nil, s12aInvalid("family sharing cannot be turned off once enabled (%s %s)", r.Type, r.ID)
		}
	}
	changed := map[string]any{}
	var names []string
	for k, v := range want {
		if !s12aSame(r.Attributes[k], v) {
			changed[k] = v
			names = append(names, k)
		}
	}
	slices.Sort(names)
	return changed, names, nil
}

// s12aPatch updates attrs of one resource. ConfirmNone write tools carry the
// write capability in ctx before the handler runs.
func s12aPatch(ctx context.Context, env *tool.Env, path, typ, id string, attrs map[string]any) error {
	_, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPatch, Path: path, Write: true, Body: s12aBody(typ, id, attrs, nil)})
	return err
}

// s12aPost creates a resource and returns it.
func s12aPost(ctx context.Context, env *tool.Env, path, typ string, attrs map[string]any, rels map[string][2]string) (tool.Resource, error) {
	resp, err := env.ASC.Do(ctx, tool.Request{Method: http.MethodPost, Path: path, Write: true, Body: s12aBody(typ, "", attrs, rels)})
	if err != nil {
		return tool.Resource{}, err
	}
	r, ok := resp.First()
	if !ok || r.ID == "" {
		return tool.Resource{}, fmt.Errorf("create %s: response has no resource: %w", typ, tool.ErrOutcomeUnknown)
	}
	return r, nil
}

// s12aIncluded indexes included resources by type and id.
func s12aIncluded(inc []tool.Resource) map[string]tool.Resource {
	m := make(map[string]tool.Resource, len(inc))
	for _, r := range inc {
		m[r.Type+"/"+r.ID] = r
	}
	return m
}

// s12aRelated returns the included resources of r's to-many relationship.
func s12aRelated(r tool.Resource, rel, typ string, inc map[string]tool.Resource) []tool.Resource {
	var out []tool.Resource
	for _, id := range r.RelIDs(rel) {
		if x, ok := inc[typ+"/"+id]; ok {
			out = append(out, x)
		}
	}
	return out
}

// s12aLatestVersion returns the highest-numbered version among vs.
func s12aLatestVersion(vs []tool.Resource) (tool.Resource, bool) {
	var best tool.Resource
	found, bestN := false, -1
	for _, v := range vs {
		if n, _ := v.Int("version"); !found || n > bestN {
			best, bestN, found = v, n, true
		}
	}
	return best, found
}

// s12aOpenVersion finds the parent's editable version (with its
// localizations) or creates one. Localizations of a newly created version are
// read back, because Apple may copy them from the previous version.
func s12aOpenVersion(ctx context.Context, env *tool.Env, k s12aVersionKind, parentID string) (s12aVersionOut, []tool.Resource, error) {
	q := url.Values{
		"filter[state]":             {s12aOpenVersionStates},
		"include":                   {"localizations"},
		"fields[" + k.Type + "]":    {"version,state,localizations"},
		"fields[" + k.LocType + "]": {strings.Join(k.LocFields, ",")},
		"limit[localizations]":      {"50"},
	}
	resp, err := env.ASC.Get(ctx, fmt.Sprintf(k.ListPath, url.PathEscape(parentID)), q)
	if err != nil {
		return s12aVersionOut{}, nil, fmt.Errorf("read %s of %s: %w", k.Type, parentID, err)
	}
	if v, ok := s12aLatestVersion(resp.Data); ok {
		locs := s12aRelated(v, "localizations", k.LocType, s12aIncluded(resp.Included))
		return s12aVersionOut{ID: v.ID, State: v.Str("state")}, locs, nil
	}
	v, err := s12aPost(ctx, env, k.CreatePath, k.Type, nil, map[string][2]string{k.ParentRel: {k.ParentType, parentID}})
	if err != nil {
		return s12aVersionOut{}, nil, fmt.Errorf("create %s for %s: %w", k.Type, parentID, err)
	}
	out := s12aVersionOut{ID: v.ID, State: v.Str("state"), Created: true}
	lr, err := env.ASC.Get(ctx, fmt.Sprintf(k.LocListPath, url.PathEscape(v.ID)), url.Values{
		"fields[" + k.LocType + "]": {strings.Join(k.LocFields, ",")},
	})
	if err != nil {
		return out, nil, fmt.Errorf("read localizations of %s %s: %w", k.Type, v.ID, err)
	}
	return out, lr.Data, nil
}

// s12aUpsertLocs writes the wanted localizations on a version: PATCH the
// changed fields of existing locales, POST missing ones. Locales absent from
// want are never touched. Failures are recorded per item.
func s12aUpsertLocs(ctx context.Context, env *tool.Env, k s12aVersionKind, versionID string, existing []tool.Resource, want map[string]map[string]any) []s12aItem {
	byLocale := make(map[string]tool.Resource, len(existing))
	for _, r := range existing {
		byLocale[r.Str("locale")] = r
	}
	locales := make([]string, 0, len(want))
	for l := range want {
		locales = append(locales, l)
	}
	slices.Sort(locales)
	items := make([]s12aItem, 0, len(locales))
	for _, loc := range locales {
		attrs := want[loc]
		it := s12aItem{Kind: k.LocType, Locale: loc}
		if cur, ok := byLocale[loc]; ok {
			it.ID = cur.ID
			changed, names, err := s12aDiff(cur, attrs)
			switch {
			case err != nil:
				it.Action, it.Error = s12aFailed, err.Error()
			case len(changed) == 0:
				it.Action = s12aUnchanged
			default:
				if err := s12aPatch(ctx, env, k.LocPath+"/"+url.PathEscape(cur.ID), k.LocType, cur.ID, changed); err != nil {
					it.Action, it.Error = s12aFailed, err.Error()
				} else {
					it.Action, it.Changed = s12aUpdated, names
				}
			}
			items = append(items, it)
			continue
		}
		body := map[string]any{"locale": loc}
		for a, v := range attrs {
			body[a] = v
		}
		r, err := s12aPost(ctx, env, k.LocPath, k.LocType, body, map[string][2]string{"version": {k.Type, versionID}})
		switch {
		case err == nil:
			it.ID, it.Action = r.ID, s12aCreated
		case s12aConflict(err):
			it.Action, it.Note = s12aAlready, "Apple reports this locale exists; rerun to update it"
		default:
			it.Action, it.Error = s12aFailed, err.Error()
		}
		items = append(items, it)
	}
	return items
}

// s12aScreenshotKind describes where a product's review screenshot lives.
type s12aScreenshotKind struct {
	CreatePath string
	Type       string
	ParentRel  string
	ParentType string
}

var (
	s12aIAPShot = s12aScreenshotKind{
		CreatePath: "/v1/inAppPurchaseAppStoreReviewScreenshots",
		Type:       "inAppPurchaseAppStoreReviewScreenshots", ParentRel: "inAppPurchaseV2", ParentType: "inAppPurchases",
	}
	s12aSubShot = s12aScreenshotKind{
		CreatePath: "/v1/subscriptionAppStoreReviewScreenshots",
		Type:       "subscriptionAppStoreReviewScreenshots", ParentRel: "subscription", ParentType: "subscriptions",
	}
)

// s12aShotFields are the sparse fields read for an existing review screenshot.
const s12aShotFields = "fileName,sourceFileChecksum,assetDeliveryState"

// s12aScreenshot uploads the review screenshot at path unless the current one
// (cur, may be zero) has the same checksum. A different existing screenshot is
// never replaced: replacement deletes Apple's copy, which needs an explicit
// DELETE the user confirms.
func s12aScreenshot(ctx context.Context, env *tool.Env, k s12aScreenshotKind, parentID, path string, cur tool.Resource) s12aItem {
	it := s12aItem{Kind: k.Type}
	f, err := env.Files.Open(path)
	if err != nil {
		it.Action, it.Error = s12aFailed, err.Error()
		return it
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		it.Action, it.Error = s12aFailed, err.Error()
		return it
	}
	sum, err := upload.FileMD5(io.NewSectionReader(f, 0, fi.Size()))
	if err != nil {
		it.Action, it.Error = s12aFailed, err.Error()
		return it
	}
	if cur.ID != "" {
		it.ID = cur.ID
		if strings.EqualFold(cur.Str("sourceFileChecksum"), sum) {
			it.Action = s12aUnchanged
			return it
		}
		it.Action = s12aSkipped
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
		it.Action, it.Error = s12aFailed, err.Error()
	case res.StillProcessing:
		it.Action, it.Note = s12aCreated, "uploaded; Apple is still processing it"
	default:
		it.Action = s12aCreated
	}
	return it
}

// s12aAllUnchanged reports whether no item changed anything remotely.
func s12aAllUnchanged(items []s12aItem) bool {
	for _, it := range items {
		if it.Action != s12aUnchanged && it.Action != s12aAlready {
			return false
		}
	}
	return true
}

// s12aFailures counts failed items.
func s12aFailures(items []s12aItem) int {
	n := 0
	for _, it := range items {
		if it.Action == s12aFailed {
			n++
		}
	}
	return n
}
