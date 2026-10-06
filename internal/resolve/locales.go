package resolve

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Locales implements [tool.Resolver]. "all" returns the locales of the app's
// editable version, falling back to its App Info localizations. An explicit
// list is normalized to ASC codes ("German" → "de-DE") and deduplicated in
// order; it may name locales the app does not have yet.
func (r *Resolver) Locales(ctx context.Context, appID string, set tool.LocaleSet) ([]string, error) {
	if set.IsAll() {
		return r.appLocales(ctx, appID)
	}
	out := make([]string, 0, len(set))
	for _, in := range set {
		code, err := applemeta.NormalizeLocale(in)
		if err != nil {
			valid := applemeta.Locales()
			if le := (*applemeta.LocaleError)(nil); errors.As(err, &le) {
				valid = le.Valid
			}
			return nil, &tool.ErrNotFound{
				Kind: "locale", Ref: in,
				Hint: "valid ASC locales: " + strings.Join(valid, ", "),
			}
		}
		if !slices.Contains(out, code) {
			out = append(out, code)
		}
	}
	return out, nil
}

func (r *Resolver) appLocales(ctx context.Context, appID string) ([]string, error) {
	vs, err := r.versions(ctx, appID, "", url.Values{"filter[appVersionState]": {strings.Join(editableStates, ",")}})
	if err != nil {
		return nil, err
	}
	if v, ok := newest(vs, editableStates...); ok {
		locs, err := r.listLocales(ctx, "/v1/appStoreVersions/"+v.ID+"/appStoreVersionLocalizations",
			map[string][]string{"appStoreVersionLocalizations": {"locale"}})
		if err != nil || len(locs) > 0 {
			return locs, err
		}
	}
	res, err := r.asc.Get(ctx, "/v1/apps/"+appID+"/appInfos", url.Values{
		"fields[appInfos]": {"state,appStoreState"},
		"limit":            {"10"},
	})
	if err != nil {
		return nil, fmt.Errorf("list app infos of app %s: %w", appID, err)
	}
	if info, ok := editableInfo(res.Data); ok {
		locs, err := r.listLocales(ctx, "/v1/appInfos/"+info.ID+"/appInfoLocalizations", nil)
		if err != nil || len(locs) > 0 {
			return locs, err
		}
	}
	return nil, &tool.ErrNotFound{
		Kind: "locales", Ref: "all", Hint: "the app has no localizations yet; name the locales explicitly",
		Next: next("apps_get", map[string]any{"app": appID}),
	}
}

// editableInfo prefers the App Info that is not the live one.
func editableInfo(infos []tool.Resource) (tool.Resource, bool) {
	for _, d := range infos {
		state := d.Str("state")
		if state == "" {
			state = d.Str("appStoreState")
		}
		if state != stateLive && state != "READY_FOR_SALE" {
			return d, true
		}
	}
	if len(infos) > 0 {
		return infos[0], true
	}
	return tool.Resource{}, false
}

// listLocales returns the sorted, unique "locale" attributes of a collection.
func (r *Resolver) listLocales(ctx context.Context, path string, fields map[string][]string) ([]string, error) {
	res, err := r.asc.List(ctx, path, tool.ListOptions{Fields: fields, Limit: 200, MaxItems: 200})
	if err != nil {
		return nil, fmt.Errorf("list locales: %w", err)
	}
	var out []string
	for _, d := range res.Data {
		if l := d.Str("locale"); l != "" && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out, nil
}
