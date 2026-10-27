package screenshots

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tools/assetlib"
)

// Thumbnail limits for screenshots_list.
const (
	maxThumbnails   = 4
	thumbnailWidth  = 300
	maxThumbnailLen = 1 << 20
)

type listIn struct {
	App        tool.AppRef     `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform   tool.Platform   `json:"platform,omitempty"`
	Version    tool.VersionRef `json:"version,omitempty" jsonschema:"editable (default, else live), live or a version string"`
	Locales    tool.LocaleSet  `json:"locales,omitempty" jsonschema:"locales to show, or [\"all\"] (default)"`
	Display    string          `json:"display,omitempty" jsonschema:"e.g. APP_IPHONE_67, 6.9, ipad-13, mac"`
	Thumbnails bool            `json:"thumbnails,omitempty"`
	Source     string          `json:"source,omitempty" jsonschema:"default both"`
}

type localeOut struct {
	Locale         string             `json:"locale"`
	LocalizationID string             `json:"localization_id"`
	ScreenshotSets []remoteSet        `json:"screenshot_sets"`
	PreviewSets    []remotePreviewSet `json:"preview_sets"`
	// Library is the Asset Library placements by placement group;
	// Creatives are its non-screenshot/preview placements (header,
	// search-result art).
	Library   []libraryGroup       `json:"library,omitempty"`
	Creatives []assetlib.Placement `json:"creatives,omitempty"`
	// LibraryDeduped counts library screenshots already listed in a set
	// (same asset id), shown once.
	LibraryDeduped int `json:"library_deduped,omitempty"`
	// placements are the raw library placements (coverage).
	placements []assetlib.Placement
}

// libraryGroup is the library screenshots/previews of one placement group.
type libraryGroup struct {
	Group      string               `json:"group"`
	Placements []assetlib.Placement `json:"placements"`
}

// Screenshot sources of screenshots_list.
const (
	sourceSets    = "sets"
	sourceLibrary = "library"
	sourceBoth    = "both"
)

type listOut struct {
	App      appOut        `json:"app"`
	Platform tool.Platform `json:"platform"`
	Version  versionOut    `json:"version"`
	Coverage *coverageOut  `json:"coverage,omitempty"`
	Locales  []localeOut   `json:"locales"`
	// LibraryError is set when the Asset Library could not be read.
	LibraryError string `json:"library_error,omitempty"`
	// VersionSource is "live_fallback" when no version is editable and the
	// live one is shown instead; Note says what that means for changes.
	VersionSource string `json:"version_source,omitempty"`
	Note          string `json:"note,omitempty"`
}

// missingDisplay is a locale lacking every slot of one required group.
type missingDisplay struct {
	Locale string                  `json:"locale"`
	OneOf  []applemeta.DisplayType `json:"one_of"`
}

// coverageOut says whether the shown locales have the screenshots App Review
// requires. Locales with no screenshots at all use the primary locale's.
type coverageOut struct {
	PrimaryLocale      string                    `json:"primary_locale,omitempty"`
	Required           [][]applemeta.DisplayType `json:"required"`
	MissingRequired    []missingDisplay          `json:"missing_required"`
	FallsBackToPrimary []string                  `json:"falls_back_to_primary,omitempty"`
	Note               string                    `json:"note,omitempty"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "screenshots_list", Toolset: tool.Screenshots, Class: tool.Read, Idempotent: true,
		Title: "List screenshots and previews", UI: "screenshots",
		Description: "Screenshot and preview sets and Asset Library placements per locale and display for a version (default editable): ids, order, file names, " +
			"checksums, states, sizes, required-display coverage (empty locales fall back to primary). thumbnails:true attaches ≤4 images.",
		Enums: map[string][]any{"platform": platformEnum, "source": {sourceSets, sourceLibrary, sourceBoth}},
	}, screenshotsList))
}

func screenshotsList(ctx context.Context, env *tool.Env, in listIn) (*tool.Result, error) {
	var display applemeta.DisplayType
	source := cmp.Or(in.Source, sourceBoth)
	if !slices.Contains([]string{sourceSets, sourceLibrary, sourceBoth}, source) {
		return nil, fmt.Errorf("%w: source must be sets, library or both", tool.ErrInvalidArgs)
	}
	if in.Display != "" {
		d, err := applemeta.ParseDisplay(in.Display)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", tool.ErrInvalidArgs, err)
		}
		display = d
	}
	t, err := resolveTarget(ctx, env, in.App, in.Platform, in.Version, false)
	if err != nil {
		return nil, err
	}
	locs, err := localizationsWithSets(ctx, env, t.Version.ID)
	if err != nil {
		return nil, err
	}
	byCode := make(map[string]localization, len(locs))
	for code, l := range locs {
		byCode[code] = l.localization
	}
	codes, err := selectLocales(ctx, env, t.App.ID, in.Locales, byCode)
	if err != nil {
		return nil, err
	}
	out := listOut{App: t.appOut(), Platform: t.Platform, Version: t.versionOut(), Locales: []localeOut{}}
	if t.FellBack {
		out.VersionSource = "live_fallback"
		out.Note = fmt.Sprintf("no editable version: showing the live version %s (%s), which cannot be changed; "+
			"screenshots_plan and library_plan need an editable version, which release_prepare creates", t.Version.String, t.Version.State)
	}
	var placed []string
	for _, code := range codes {
		loc := locs[code]
		if loc.placed {
			placed = append(placed, loc.ID)
		}
		sets := []remoteSet{}
		var previews []remotePreviewSet
		if source != sourceLibrary {
			if sets, previews, err = localeSets(ctx, env, loc, display); err != nil {
				return nil, err
			}
		}
		if previews == nil {
			previews = []remotePreviewSet{}
		}
		out.Locales = append(out.Locales, localeOut{Locale: code, LocalizationID: loc.ID, ScreenshotSets: sets, PreviewSets: previews})
	}
	if source != sourceSets {
		addLibrary(ctx, env, &out, placed, display)
	}
	if display == "" && source == sourceBoth {
		if out.Coverage, err = coverage(ctx, env, t, out.Locales); err != nil {
			return nil, err
		}
	}
	res := tool.OK(out)
	if t.FellBack {
		res.Warn(out.Note).Then(tool.NextStep{Tool: "release_prepare", Args: map[string]any{"app": t.App.ID, "platform": string(t.Platform)}})
	}
	if usesLibrary(out.Locales) {
		// library_plan needs dir, remove or archive, none of which a listing
		// knows, so point at it in prose instead of a next step that fails.
		msg := "some screenshots here are Asset Library placements; change them with library_plan (pass dir, remove or archive), not screenshots_sync"
		if t.FellBack {
			msg += ", once release_prepare has created an editable version"
		}
		res.Warn(msg)
	}
	if in.Thumbnails {
		addThumbnails(ctx, env, res, out.Locales)
	}
	return res, nil
}

// usesLibrary reports whether any shown localization has Asset Library
// placements (then library_plan is the write path for it).
func usesLibrary(locs []localeOut) bool {
	return slices.ContainsFunc(locs, func(l localeOut) bool { return len(l.placements) > 0 })
}

// localeSets reads a localization's screenshot and preview sets, filtered
// to display when set.
func localeSets(ctx context.Context, env *tool.Env, loc localizationSets, display applemeta.DisplayType) ([]remoteSet, []remotePreviewSet, error) {
	sets := []remoteSet{}
	var err error
	if loc.screenshotSets > 0 {
		if sets, err = screenshotSets(ctx, env, loc.ID); err != nil {
			return nil, nil, err
		}
	}
	pvType := ""
	if display != "" {
		kept := sets[:0]
		for _, s := range sets {
			if s.Display == display {
				kept = append(kept, s)
			}
		}
		sets = kept
		spec, _ := applemeta.LookupDisplay(display)
		pvType = string(spec.Preview)
	}
	var previews []remotePreviewSet
	if loc.previewSets > 0 && (display == "" || pvType != "") {
		if previews, err = previewSets(ctx, env, loc.ID, pvType); err != nil {
			return nil, nil, err
		}
	}
	return sets, previews, nil
}

// addLibrary adds the Asset Library placements of the shown locales,
// grouped by placement group, with creatives listed separately. A failed
// read (e.g. 403/404) is reported in library_error, not as a tool error.
func addLibrary(ctx context.Context, env *tool.Env, out *listOut, placed []string, display applemeta.DisplayType) {
	lib, err := assetlib.ForLocalizations(ctx, env, placed)
	if err != nil {
		out.LibraryError = "Asset Library could not be read"
		if st := tool.APIStatus(err); st != 0 {
			out.LibraryError += fmt.Sprintf(" (HTTP %d)", st)
		}
		return
	}
	for i := range out.Locales {
		l := &out.Locales[i]
		l.placements = lib.ByLocalization[l.LocalizationID]
		inSets := map[string]bool{}
		for _, s := range l.ScreenshotSets {
			for _, sh := range s.Shots {
				inSets[sh.ID] = true
			}
		}
		for _, p := range l.placements {
			switch {
			case p.Type == assetlib.TypeScreenshot && (inSets[p.AssetID] || inSets[p.ID]):
				// The same record is listed in its screenshot set.
				l.LibraryDeduped++
			case p.Creative():
				l.Creatives = append(l.Creatives, p)
			case display != "" && !assetlib.Covers([]assetlib.Placement{p}, display) && p.Type == assetlib.TypeScreenshot:
			case len(l.Library) > 0 && l.Library[len(l.Library)-1].Group == p.Group:
				g := &l.Library[len(l.Library)-1]
				g.Placements = append(g.Placements, p)
			default:
				l.Library = append(l.Library, libraryGroup{Group: p.Group, Placements: []assetlib.Placement{p}})
			}
		}
	}
}

// localizationSets is a version localization with how many screenshot and
// preview sets it has.
type localizationSets struct {
	localization
	screenshotSets, previewSets int
	placed                      bool // has Asset Library placements
}

// setsPerLocalization caps the sets included per localization; a version
// localization has at most about 40 display and preview types.
const setsPerLocalization = 50

// localizationsWithSets lists the version's localizations with their set ids
// in one request (include=appScreenshotSets,appPreviewSets), so per-locale
// set requests are made only for locales that have sets. A localization at
// the include cap is counted as having sets, so the per-locale request still
// reads them all.
func localizationsWithSets(ctx context.Context, env *tool.Env, versionID string) (map[string]localizationSets, error) {
	perLoc := strconv.Itoa(setsPerLocalization)
	resp, err := env.ASC.Get(ctx, "/v1/appStoreVersions/"+versionID+"/appStoreVersionLocalizations", url.Values{
		"fields[appStoreVersionLocalizations]": {"locale,appScreenshotSets,appPreviewSets,placements"},
		"fields[appScreenshotSets]":            {"screenshotDisplayType"},
		"fields[appPreviewSets]":               {"previewType"},
		"include":                              {"appScreenshotSets,appPreviewSets,placements"},
		"limit[placements]":                    {"1"},
		"limit[appScreenshotSets]":             {perLoc},
		"limit[appPreviewSets]":                {perLoc},
		"limit":                                {"200"},
	})
	if err != nil {
		return nil, fmt.Errorf("list localizations: %w", err)
	}
	out := make(map[string]localizationSets, len(resp.Data))
	for _, r := range resp.Data {
		l := r.Str("locale")
		out[l] = localizationSets{
			localization:   localization{ID: r.ID, Locale: l},
			screenshotSets: len(r.RelIDs("appScreenshotSets")),
			previewSets:    len(r.RelIDs("appPreviewSets")),
			placed:         len(r.RelIDs("placements")) > 0,
		}
	}
	return out, nil
}

// coverage checks the required display types per shown locale, mirroring
// release_status: a non-primary locale without screenshots falls back to
// the primary locale, and iPad is required once any iPad set exists (the API
// does not say whether a build runs on iPad).
func coverage(ctx context.Context, env *tool.Env, t *target, locs []localeOut) (*coverageOut, error) {
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+t.App.ID, url.Values{"fields[apps]": {"primaryLocale"}})
	if err != nil {
		return nil, fmt.Errorf("read primary locale: %w", err)
	}
	c := &coverageOut{MissingRequired: []missingDisplay{}}
	if r, ok := resp.First(); ok {
		c.PrimaryLocale = r.Str("primaryLocale")
	}
	supportsIPad := false
	for _, l := range locs {
		for _, s := range l.ScreenshotSets {
			if strings.HasPrefix(string(s.Display), "APP_IPAD") && len(s.Shots) > 0 {
				supportsIPad = true
			}
		}
		for _, p := range l.placements {
			if p.IPad() {
				supportsIPad = true
			}
		}
	}
	c.Required = applemeta.RequiredDisplays(t.Platform, supportsIPad)
	if c.Required == nil {
		c.Required = [][]applemeta.DisplayType{}
	}
	for _, l := range locs {
		has := map[applemeta.DisplayType]bool{}
		for _, s := range l.ScreenshotSets {
			if len(s.Shots) > 0 {
				has[s.Display] = true
			}
		}
		libShots := slices.ContainsFunc(l.placements, func(p assetlib.Placement) bool { return p.Type == assetlib.TypeScreenshot })
		if len(has) == 0 && !libShots && l.Locale != c.PrimaryLocale {
			c.FallsBackToPrimary = append(c.FallsBackToPrimary, l.Locale)
			continue
		}
		for _, group := range c.Required {
			if !slices.ContainsFunc(group, func(d applemeta.DisplayType) bool { return has[d] || assetlib.Covers(l.placements, d) }) {
				c.MissingRequired = append(c.MissingRequired, missingDisplay{Locale: l.Locale, OneOf: group})
			}
		}
	}
	var notes []string
	if len(c.FallsBackToPrimary) > 0 {
		notes = append(notes, fmt.Sprintf("%d locale(s) have no screenshots and show the %s ones in the store; that is not an error", len(c.FallsBackToPrimary), c.PrimaryLocale))
	}
	if t.Platform == tool.IOS && !supportsIPad {
		notes = append(notes, "no iPad screenshots; if the build supports iPad, APP_IPAD_PRO_3GEN_129 is required too")
	}
	c.Note = strings.Join(notes, "; ")
	return c, nil
}

// addThumbnails fetches up to maxThumbnails rendered screenshots through the
// unauthenticated fetcher; failures become warnings.
func addThumbnails(ctx context.Context, env *tool.Env, res *tool.Result, locs []localeOut) {
	n := 0
	for _, l := range locs {
		for _, set := range l.ScreenshotSets {
			for _, s := range set.Shots {
				if n == maxThumbnails {
					return
				}
				u := thumbnailURL(s)
				if u == "" {
					continue
				}
				n++
				r, err := env.Fetch.Fetch(ctx, "GET", u, nil, nil, maxThumbnailLen)
				if err != nil || r.Status != 200 {
					res.Warn(fmt.Sprintf("thumbnail of %s %s %s unavailable", l.Locale, set.Display, s.ID))
					continue
				}
				res.WithImage("image/jpeg", r.Body)
			}
		}
	}
}

// thumbnailURL renders an imageAsset templateUrl ("…/{w}x{h}bb.{f}") at
// thumbnailWidth, keeping the aspect ratio.
func thumbnailURL(s remoteShot) string {
	if s.template == "" || s.Width <= 0 || s.Height <= 0 {
		return ""
	}
	h := s.Height * thumbnailWidth / s.Width
	return strings.NewReplacer("{w}", strconv.Itoa(thumbnailWidth), "{h}", strconv.Itoa(h), "{f}", "jpg").Replace(s.template)
}
