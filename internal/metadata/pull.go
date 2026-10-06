package metadata

import (
	"context"
	"fmt"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// maxLocalizations bounds one pull; App Store Connect supports about 40
// locales, so a larger collection means something is wrong.
const maxLocalizations = 500

// Pull reads the version localizations of versionID and the app-info
// localizations of appInfoID (all pages). An empty id skips that level.
// Free-text values wrapped as [tool.Untrusted] are returned as plain strings.
func Pull(ctx context.Context, asc tool.ASC, versionID, appInfoID string) (*Listing, error) {
	l := &Listing{
		App: AppInfoFields{
			VersionID:              versionID,
			AppInfoID:              appInfoID,
			VersionLocalizationIDs: map[string]string{},
			AppInfoLocalizationIDs: map[string]string{},
		},
		Locales: map[string]*LocaleFields{},
	}
	if versionID != "" {
		if err := pullLevel(ctx, asc, l, LevelVersion, "/v1/appStoreVersions/"+versionID+"/appStoreVersionLocalizations"); err != nil {
			return nil, err
		}
	}
	if appInfoID != "" {
		if err := pullLevel(ctx, asc, l, LevelAppInfo, "/v1/appInfos/"+appInfoID+"/appInfoLocalizations"); err != nil {
			return nil, err
		}
	}
	return l, nil
}

func pullLevel(ctx context.Context, asc tool.ASC, l *Listing, level Level, path string) error {
	res, err := asc.List(ctx, path, tool.ListOptions{Limit: 200, MaxItems: maxLocalizations})
	if err != nil {
		return fmt.Errorf("metadata: pull %s localizations: %w", level, err)
	}
	if res.NextCursor != "" {
		return fmt.Errorf("metadata: pull %s localizations: more than %d entries", level, maxLocalizations)
	}
	ids := l.App.VersionLocalizationIDs
	if level == LevelAppInfo {
		ids = l.App.AppInfoLocalizationIDs
	}
	for _, r := range res.Data {
		code, ok := attrString(r.Attributes["locale"])
		if !ok || code == "" {
			return fmt.Errorf("metadata: %s localization %s has no locale", level, r.ID)
		}
		ids[code] = r.ID
		lf := l.locale(code)
		for _, f := range Fields {
			if f.Level != level {
				continue
			}
			if v, ok := attrString(r.Attributes[f.Key]); ok {
				if err := lf.Set(f.Key, v); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// attrString unwraps a string attribute, including [tool.Untrusted] values
// and their marshaled {"untrusted": text} form. Null is absent.
func attrString(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case tool.Untrusted:
		return string(s), true
	case map[string]any:
		if u, ok := s["untrusted"].(string); ok && len(s) == 1 {
			return u, true
		}
	}
	return "", false
}
