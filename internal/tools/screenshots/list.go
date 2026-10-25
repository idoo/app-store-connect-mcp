package screenshots

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
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
	Display    string          `json:"display,omitempty" jsonschema:"only this display type, e.g. APP_IPHONE_67, 6.9, ipad-13, mac"`
	Thumbnails bool            `json:"thumbnails,omitempty" jsonschema:"attach up to 4 small screenshot images"`
}

type localeOut struct {
	Locale         string             `json:"locale"`
	LocalizationID string             `json:"localization_id"`
	ScreenshotSets []remoteSet        `json:"screenshot_sets"`
	PreviewSets    []remotePreviewSet `json:"preview_sets"`
}

type listOut struct {
	App      appOut        `json:"app"`
	Platform tool.Platform `json:"platform"`
	Version  versionOut    `json:"version"`
	Coverage *coverageOut  `json:"coverage,omitempty"`
	Locales  []localeOut   `json:"locales"`
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
		Title: "List screenshots and previews",
		Description: "Screenshot and app preview sets per locale and display type for a version (default editable): ids, order, " +
			"file names, checksums, delivery states, sizes, and coverage of required display types (empty locales fall back " +
			"to the primary). thumbnails:true attaches up to 4 small images.",
		Enums: map[string][]any{"platform": platformEnum},
	}, screenshotsList))
}

func screenshotsList(ctx context.Context, env *tool.Env, in listIn) (*tool.Result, error) {
	var display applemeta.DisplayType
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
	locs, err := localizations(ctx, env, t.Version.ID)
	if err != nil {
		return nil, err
	}
	codes, err := selectLocales(ctx, env, t.App.ID, in.Locales, locs)
	if err != nil {
		return nil, err
	}
	out := listOut{App: t.appOut(), Platform: t.Platform, Version: t.versionOut(), Locales: []localeOut{}}
	for _, code := range codes {
		loc := locs[code]
		sets, err := screenshotSets(ctx, env, loc.ID)
		if err != nil {
			return nil, err
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
		if display == "" || pvType != "" {
			if previews, err = previewSets(ctx, env, loc.ID, pvType); err != nil {
				return nil, err
			}
		}
		if previews == nil {
			previews = []remotePreviewSet{}
		}
		out.Locales = append(out.Locales, localeOut{Locale: code, LocalizationID: loc.ID, ScreenshotSets: sets, PreviewSets: previews})
	}
	if display == "" {
		if out.Coverage, err = coverage(ctx, env, t, out.Locales); err != nil {
			return nil, err
		}
	}
	res := tool.OK(out)
	if in.Thumbnails {
		addThumbnails(ctx, env, res, out.Locales)
	}
	return res, nil
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
		if len(has) == 0 && l.Locale != c.PrimaryLocale {
			c.FallsBackToPrimary = append(c.FallsBackToPrimary, l.Locale)
			continue
		}
		for _, group := range c.Required {
			if !slices.ContainsFunc(group, func(d applemeta.DisplayType) bool { return has[d] }) {
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
