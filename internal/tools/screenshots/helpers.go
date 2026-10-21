package screenshots

import (
	"context"
	"fmt"
	"net/url"
	"slices"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/resolve"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// platformEnum lists the App Store Connect platforms accepted by `platform`.
var platformEnum = []any{string(tool.IOS), string(tool.MacOS), string(tool.TVOS), string(tool.VisionOS)}

// Apple's per-set limits.
const (
	maxScreenshotsPerSet = 10
	maxPreviewsPerSet    = 3
)

// errNoEditableVersion is returned when plan/sync/upload need an editable
// version and the app has none.
var errNoEditableVersion = &tool.Error{
	Code:    "no_editable_version",
	Message: "the app has no editable App Store version",
	Hint:    "create the next version with release_prepare, then plan again",
}

// errMissingLocalization is returned when a locale has no version localization.
var errMissingLocalization = &tool.Error{
	Code:    "missing_localization",
	Message: "the version has no localization for this locale",
	Hint:    "add the locale's listing with metadata_plan/metadata_apply first",
}

type appOut struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BundleID string `json:"bundle_id"`
}

type versionOut struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	State   string `json:"state"`
}

// target is a resolved app, platform and version.
type target struct{ resolve.Resolved }

func (t *target) appOut() appOut {
	return appOut{ID: t.App.ID, Name: t.App.Name, BundleID: t.App.BundleID}
}

func (t *target) versionOut() versionOut {
	return versionOut{ID: t.Version.ID, Version: t.Version.String, State: t.Version.State}
}

// resolveTarget resolves app, platform and version. With editableOnly an
// unspecified version must be the editable one (else no_editable_version);
// otherwise it falls back to the live version.
func resolveTarget(ctx context.Context, env *tool.Env, appRef tool.AppRef, platform tool.Platform, ref tool.VersionRef, editableOnly bool) (*target, error) {
	fb := resolve.Fallback{Live: true}
	if editableOnly {
		fb = resolve.Fallback{NoEditable: errNoEditableVersion}
	}
	r, err := resolve.Target(ctx, env, appRef, platform, ref, fb)
	if err != nil {
		return nil, err
	}
	return &target{*r}, nil
}

// localization is one appStoreVersionLocalization.
type localization struct {
	ID     string
	Locale string
}

// localizations lists the version's localizations keyed by locale code.
func localizations(ctx context.Context, env *tool.Env, versionID string) (map[string]localization, error) {
	res, err := env.ASC.List(ctx, "/v1/appStoreVersions/"+versionID+"/appStoreVersionLocalizations", tool.ListOptions{
		Fields:   map[string][]string{"appStoreVersionLocalizations": {"locale"}},
		Limit:    200,
		MaxItems: 200,
	})
	if err != nil {
		return nil, fmt.Errorf("list localizations: %w", err)
	}
	out := make(map[string]localization, len(res.Data))
	for _, r := range res.Data {
		l := r.Str("locale")
		out[l] = localization{ID: r.ID, Locale: l}
	}
	return out, nil
}

// selectLocales returns the sorted locale codes of locs, restricted to set
// when it is non-empty and not "all".
func selectLocales(ctx context.Context, env *tool.Env, appID string, set tool.LocaleSet, locs map[string]localization) ([]string, error) {
	var want []string
	if len(set) > 0 && !set.IsAll() {
		resolved, err := env.Resolve.Locales(ctx, appID, set)
		if err != nil {
			return nil, err
		}
		want = resolved
	}
	out := make([]string, 0, len(locs))
	for l := range locs {
		if want == nil || slices.Contains(want, l) {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out, nil
}

// remoteShot is one screenshot as App Store Connect has it.
type remoteShot struct {
	ID       string `json:"id"`
	FileName string `json:"file_name"`
	Order    int    `json:"order"`
	State    string `json:"state,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Checksum string `json:"checksum,omitempty"`
	template string
}

// remoteSet is one appScreenshotSet with its screenshots in display order.
type remoteSet struct {
	ID      string                `json:"id"`
	Display applemeta.DisplayType `json:"display"`
	Shots   []remoteShot          `json:"screenshots"`
}

// screenshotSets reads a localization's screenshot sets with their
// screenshots in one request (include=appScreenshots).
func screenshotSets(ctx context.Context, env *tool.Env, locID string) ([]remoteSet, error) {
	resp, err := env.ASC.Get(ctx, "/v1/appStoreVersionLocalizations/"+locID+"/appScreenshotSets", url.Values{
		"include":                   {"appScreenshots"},
		"fields[appScreenshotSets]": {"screenshotDisplayType,appScreenshots"},
		"fields[appScreenshots]":    {"fileName,sourceFileChecksum,imageAsset,assetDeliveryState"},
		"limit[appScreenshots]":     {"10"},
		"limit":                     {"50"},
	})
	if err != nil {
		return nil, fmt.Errorf("screenshot sets of localization %s: %w", locID, err)
	}
	shots := map[string]tool.Resource{}
	for _, r := range resp.Included {
		if r.Type == "appScreenshots" {
			shots[r.ID] = r
		}
	}
	out := make([]remoteSet, 0, len(resp.Data))
	for _, set := range resp.Data {
		rs := remoteSet{ID: set.ID, Display: applemeta.DisplayType(set.Str("screenshotDisplayType")), Shots: []remoteShot{}}
		for i, id := range set.RelIDs("appScreenshots") {
			r := shots[id]
			s := remoteShot{ID: id, FileName: r.Str("fileName"), Order: i + 1, Checksum: r.Str("sourceFileChecksum"), State: deliveryState(r, "assetDeliveryState")}
			if a, ok := tool.Unwrap(r.Attributes["imageAsset"]).(map[string]any); ok {
				s.template, _ = a["templateUrl"].(string)
				s.Width = intOf(a["width"])
				s.Height = intOf(a["height"])
			}
			rs.Shots = append(rs.Shots, s)
		}
		out = append(out, rs)
	}
	slices.SortFunc(out, func(a, b remoteSet) int { return cmpString(string(a.Display), string(b.Display)) })
	return out, nil
}

// remotePreview is one app preview.
type remotePreview struct {
	ID            string `json:"id"`
	FileName      string `json:"file_name"`
	Order         int    `json:"order"`
	State         string `json:"state,omitempty"`
	FrameTimeCode string `json:"preview_frame_time_code,omitempty"`
	Checksum      string `json:"checksum,omitempty"`
}

// remotePreviewSet is one appPreviewSet with its previews in order.
type remotePreviewSet struct {
	ID          string          `json:"id"`
	PreviewType string          `json:"preview_type"`
	Previews    []remotePreview `json:"previews"`
}

// previewSets reads a localization's preview sets (optionally one type)
// with their previews in one request.
func previewSets(ctx context.Context, env *tool.Env, locID, previewType string) ([]remotePreviewSet, error) {
	q := url.Values{
		"include":                {"appPreviews"},
		"fields[appPreviewSets]": {"previewType,appPreviews"},
		"fields[appPreviews]":    {"fileName,sourceFileChecksum,previewFrameTimeCode,videoDeliveryState,assetDeliveryState"},
		"limit[appPreviews]":     {"3"},
		"limit":                  {"50"},
	}
	if previewType != "" {
		q.Set("filter[previewType]", previewType)
	}
	resp, err := env.ASC.Get(ctx, "/v1/appStoreVersionLocalizations/"+locID+"/appPreviewSets", q)
	if err != nil {
		return nil, fmt.Errorf("preview sets of localization %s: %w", locID, err)
	}
	previews := map[string]tool.Resource{}
	for _, r := range resp.Included {
		if r.Type == "appPreviews" {
			previews[r.ID] = r
		}
	}
	out := make([]remotePreviewSet, 0, len(resp.Data))
	for _, set := range resp.Data {
		ps := remotePreviewSet{ID: set.ID, PreviewType: set.Str("previewType"), Previews: []remotePreview{}}
		for i, id := range set.RelIDs("appPreviews") {
			r := previews[id]
			state := deliveryState(r, "videoDeliveryState")
			if state == "" {
				state = deliveryState(r, "assetDeliveryState")
			}
			ps.Previews = append(ps.Previews, remotePreview{
				ID: id, FileName: r.Str("fileName"), Order: i + 1, State: state,
				FrameTimeCode: r.Str("previewFrameTimeCode"), Checksum: r.Str("sourceFileChecksum"),
			})
		}
		out = append(out, ps)
	}
	slices.SortFunc(out, func(a, b remotePreviewSet) int { return cmpString(a.PreviewType, b.PreviewType) })
	return out, nil
}

// deliveryState reads {state} from an AppMediaAssetState-like attribute.
func deliveryState(r tool.Resource, key string) string {
	m, _ := tool.Unwrap(r.Attributes[key]).(map[string]any)
	s, _ := m["state"].(string)
	return s
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
