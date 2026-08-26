package screenshots

import (
	"context"
	"fmt"
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
	App        tool.AppRef     `json:"app,omitempty" jsonschema:"app id, bundle id, SKU or name; default: pinned app"`
	Platform   tool.Platform   `json:"platform,omitempty" jsonschema:"default: inferred from the app"`
	Version    tool.VersionRef `json:"version,omitempty" jsonschema:"editable (default, falls back to live), live or a version string"`
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
	Locales  []localeOut   `json:"locales"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "screenshots_list", Toolset: tool.Screenshots, Class: tool.Read, Idempotent: true,
		Title: "List screenshots and previews",
		Description: "Screenshot and app preview sets per locale and display type for a version (default editable): ids, order, " +
			"file names, checksums, delivery states, sizes. thumbnails:true attaches up to 4 small images.",
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
	res := tool.OK(out)
	if in.Thumbnails {
		addThumbnails(ctx, env, res, out.Locales)
	}
	return res, nil
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
