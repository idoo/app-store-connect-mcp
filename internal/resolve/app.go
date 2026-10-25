package resolve

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// appFields is the sparse fieldset requested for apps.
const appFields = "name,bundleId,sku,primaryLocale"

// platformOrder fixes the order of App.Platforms and platform candidates.
var platformOrder = []tool.Platform{tool.IOS, tool.MacOS, tool.TVOS, tool.VisionOS}

// App implements [tool.Resolver]. A numeric ref is an app id, a ref with a
// dot a bundle id; anything else is matched against the cached app list by
// exact name, SKU, name prefix and finally name substring (all
// case-insensitive). An empty ref uses Options.DefaultApp, then the account's
// only app. The result's Platforms come from the app's App Store versions.
func (r *Resolver) App(ctx context.Context, ref tool.AppRef) (tool.App, error) {
	s := strings.TrimSpace(string(ref))
	if s == "" {
		s = strings.TrimSpace(r.defaultApp)
	}
	app, err := r.findApp(ctx, s)
	if err != nil {
		return tool.App{}, err
	}
	plats, err := r.appPlatforms(ctx, app.ID)
	if err != nil {
		return tool.App{}, err
	}
	app.Platforms = plats
	return app, nil
}

func (r *Resolver) findApp(ctx context.Context, s string) (tool.App, error) {
	if s == "" {
		return r.onlyApp(ctx)
	}
	if isDigits(s) {
		res, err := r.asc.Get(ctx, "/v1/apps/"+s, url.Values{"fields[apps]": {appFields}})
		switch {
		case err == nil:
			if first, ok := res.First(); ok {
				return appFromResource(first), nil
			}
		case !tool.IsNotFound(err):
			return tool.App{}, fmt.Errorf("resolve app %q: %w", s, err)
		}
		// A numeric SKU is matched against the list below.
	}
	if strings.Contains(s, ".") {
		res, err := r.asc.Get(ctx, "/v1/apps", url.Values{
			"filter[bundleId]": {s},
			"fields[apps]":     {appFields},
			"limit":            {"10"},
		})
		if err != nil {
			return tool.App{}, fmt.Errorf("resolve app %q: %w", s, err)
		}
		for _, d := range res.Data {
			if strings.EqualFold(d.Str("bundleId"), s) {
				return appFromResource(d), nil
			}
		}
		// Names may contain dots too; fall through to name matching.
	}
	apps, err := r.allApps(ctx)
	if err != nil {
		return tool.App{}, err
	}
	return matchApp(s, apps)
}

// onlyApp returns the account's single app, or a typed error.
func (r *Resolver) onlyApp(ctx context.Context) (tool.App, error) {
	apps, err := r.allApps(ctx)
	if err != nil {
		return tool.App{}, err
	}
	switch len(apps) {
	case 0:
		return tool.App{}, &tool.ErrNotFound{
			Kind: "app",
			Hint: "the account has no apps; create the app record in App Store Connect first",
		}
	case 1:
		return apps[0], nil
	}
	amb := ambiguousApps("", apps)
	amb.Next = next("apps_list", nil)
	return tool.App{}, amb
}

// matchApp applies the name/SKU tiers; the first tier with hits decides.
func matchApp(s string, apps []tool.App) (tool.App, error) {
	low := strings.ToLower(s)
	tiers := []func(tool.App) bool{
		func(a tool.App) bool { return strings.EqualFold(a.Name, s) },
		func(a tool.App) bool { return a.SKU != "" && strings.EqualFold(a.SKU, s) },
		func(a tool.App) bool { return strings.HasPrefix(strings.ToLower(a.Name), low) },
		func(a tool.App) bool { return strings.Contains(strings.ToLower(a.Name), low) },
	}
	for _, match := range tiers {
		var hits []tool.App
		for _, a := range apps {
			if match(a) {
				hits = append(hits, a)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		}
		amb := ambiguousApps(s, hits)
		amb.Next = next("apps_list", map[string]any{"query": s})
		return tool.App{}, amb
	}
	return tool.App{}, &tool.ErrNotFound{
		Kind: "app",
		Ref:  s,
		Hint: "no app matches by id, bundle id, SKU or name; apps_list shows every app",
		Next: next("apps_list", nil),
	}
}

func ambiguousApps(ref string, apps []tool.App) *tool.ErrAmbiguous {
	amb := &tool.ErrAmbiguous{Kind: "app", Ref: ref}
	for _, a := range apps[:min(len(apps), maxCandidates)] {
		amb.Candidates = append(amb.Candidates, tool.Candidate{ID: a.ID, Label: a.Name + " (" + a.BundleID + ")"})
	}
	return amb
}

// allApps returns every app of the account, cached for cacheTTL.
func (r *Resolver) allApps(ctx context.Context) ([]tool.App, error) {
	r.mu.Lock()
	if r.fresh(r.appsAt) {
		apps := r.apps
		r.mu.Unlock()
		return apps, nil
	}
	r.mu.Unlock()
	res, err := r.asc.List(ctx, "/v1/apps", tool.ListOptions{
		Fields:   map[string][]string{"apps": strings.Split(appFields, ",")},
		Limit:    200,
		MaxItems: 5000,
	})
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	apps := make([]tool.App, 0, len(res.Data))
	for _, d := range res.Data {
		apps = append(apps, appFromResource(d))
	}
	r.mu.Lock()
	r.apps, r.appsAt = apps, r.clock.Now()
	r.mu.Unlock()
	return apps, nil
}

// appPlatforms returns the platforms the app has App Store versions for,
// in platformOrder, cached per app for cacheTTL.
func (r *Resolver) appPlatforms(ctx context.Context, appID string) ([]tool.Platform, error) {
	r.mu.Lock()
	if e, ok := r.platforms[appID]; ok && r.fresh(e.at) {
		r.mu.Unlock()
		return e.list, nil
	}
	r.mu.Unlock()
	res, err := r.asc.Get(ctx, "/v1/apps/"+appID+"/appStoreVersions", url.Values{
		"fields[appStoreVersions]": {"platform,versionString,appVersionState"},
		"limit":                    {"50"},
	})
	if err != nil {
		return nil, fmt.Errorf("list platforms of app %s: %w", appID, err)
	}
	var plats []tool.Platform
	for _, d := range res.Data {
		if p := tool.Platform(d.Str("platform")); p != "" && !slices.Contains(plats, p) {
			plats = append(plats, p)
		}
	}
	slices.SortStableFunc(plats, func(a, b tool.Platform) int {
		return platformRank(a) - platformRank(b)
	})
	r.mu.Lock()
	r.platforms[appID] = platformEntry{list: plats, at: r.clock.Now()}
	r.mu.Unlock()
	return plats, nil
}

func platformRank(p tool.Platform) int {
	if i := slices.Index(platformOrder, p); i >= 0 {
		return i
	}
	return len(platformOrder)
}

// platform returns want when set; otherwise the app's only platform, "" for
// an app without versions (no platform filter), or *tool.ErrAmbiguous.
func (r *Resolver) platform(ctx context.Context, appID string, want tool.Platform) (tool.Platform, error) {
	if want != "" {
		return want, nil
	}
	plats, err := r.appPlatforms(ctx, appID)
	if err != nil {
		return "", err
	}
	switch len(plats) {
	case 0:
		return "", nil
	case 1:
		return plats[0], nil
	}
	amb := &tool.ErrAmbiguous{Kind: "platform", Next: next("apps_get", map[string]any{"app": appID})}
	for _, p := range plats {
		amb.Candidates = append(amb.Candidates, tool.Candidate{ID: string(p), Label: string(p)})
	}
	return "", amb
}

func appFromResource(d tool.Resource) tool.App {
	return tool.App{ID: d.ID, BundleID: d.Str("bundleId"), Name: d.Str("name"), SKU: d.Str("sku")}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
