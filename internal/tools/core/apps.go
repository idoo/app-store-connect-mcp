package core

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// Sparse fieldsets keep app responses small.
const (
	appListFields    = "name,bundleId,sku,primaryLocale,appStoreVersions"
	versionFields    = "platform,versionString,appVersionState,appStoreState,releaseType,createdDate"
	buildFields      = "version,uploadedDate,processingState,expired,minOsVersion,preReleaseVersion"
	appsListMax      = 2000
	appsListDefault  = 50
	appsListLimitMax = 200
	latestBuilds     = 5
)

var platformEnum = []any{string(tool.IOS), string(tool.MacOS), string(tool.TVOS), string(tool.VisionOS)}

type appsListIn struct {
	Query    string `json:"query,omitempty" jsonschema:"fuzzy match on name, bundle id, SKU or id; empty lists every app"`
	Platform string `json:"platform,omitempty" jsonschema:"only apps with App Store versions for this platform"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max apps returned (default 50, max 200)"`
}

type appSummary struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	BundleID      string   `json:"bundleId"`
	SKU           string   `json:"sku,omitempty"`
	PrimaryLocale string   `json:"primaryLocale,omitempty"`
	Platforms     []string `json:"platforms"`
}

type appsGetIn struct {
	App tool.AppRef `json:"app,omitempty" jsonschema:"app id, bundle id, SKU or name; default: pinned app"`
}

type versionSummary struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Version  string `json:"version"`
	State    string `json:"appVersionState"`
	Release  string `json:"releaseType,omitempty"`
	Created  string `json:"createdDate,omitempty"`
}

type buildSummary struct {
	ID       string `json:"id"`
	Number   string `json:"build"`
	Version  string `json:"version,omitempty"`
	Platform string `json:"platform,omitempty"`
	State    string `json:"processingState"`
	Expired  bool   `json:"expired"`
	Uploaded string `json:"uploadedDate,omitempty"`
	MinOS    string `json:"minOsVersion,omitempty"`
}

type appOverview struct {
	appSummary
	Live     []versionSummary `json:"live"`
	InFlight []versionSummary `json:"in_flight"`
	Builds   []buildSummary   `json:"latest_builds"`
	Locales  []string         `json:"locales"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "apps_list", Toolset: tool.Core, Class: tool.Read, Idempotent: true,
		Title: "List apps",
		Description: "List or find apps in the account. query fuzzy-matches name, bundle id, SKU or id (best first). " +
			"Returns id, name, bundleId, sku, primaryLocale and platforms. Other tools accept any of these as the app argument.",
		Enums: map[string][]any{"platform": platformEnum},
	}, appsList))
	tool.Register(tool.New(tool.Meta{
		Name: "apps_get", Toolset: tool.Core, Class: tool.Read, Idempotent: true,
		Title: "App overview",
		Description: "One app at a glance: live version per platform, in-flight versions with appVersionState, " +
			"the 5 latest builds with processingState, and listing locales. Use before release or TestFlight work.",
	}, appsGet))
}

func appsList(ctx context.Context, env *tool.Env, in appsListIn) (*tool.Result, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = appsListDefault
	}
	if limit > appsListLimitMax {
		return nil, fmt.Errorf("%w: limit must be at most %d", tool.ErrInvalidArgs, appsListLimitMax)
	}
	res, err := env.ASC.List(ctx, "/v1/apps", tool.ListOptions{
		Query: url.Values{
			"fields[apps]":             {appListFields},
			"fields[appStoreVersions]": {"platform"},
			"limit[appStoreVersions]":  {"10"},
		},
		Include:  []string{"appStoreVersions"},
		Limit:    50,
		MaxItems: appsListMax,
	})
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	platformOf := map[string]string{}
	for _, r := range res.Included {
		if r.Type == "appStoreVersions" {
			platformOf[r.ID] = r.Str("platform")
		}
	}
	type scored struct {
		app   appSummary
		score int
	}
	var hits []scored
	for _, r := range res.Data {
		a := summarizeApp(r, platformOf)
		if in.Platform != "" && !slices.Contains(a.Platforms, in.Platform) {
			continue
		}
		s := fuzzyScore(in.Query, a)
		if s == 0 {
			continue
		}
		hits = append(hits, scored{a, s})
	}
	slices.SortStableFunc(hits, func(x, y scored) int {
		if c := cmp.Compare(y.score, x.score); c != 0 {
			return c
		}
		return cmp.Compare(strings.ToLower(x.app.Name), strings.ToLower(y.app.Name))
	})
	matched := len(hits)
	apps := make([]appSummary, 0, min(matched, limit))
	for _, h := range hits[:min(matched, limit)] {
		apps = append(apps, h.app)
	}
	out := map[string]any{"apps": apps, "returned": len(apps), "matched": matched, "total": len(res.Data)}
	r := tool.OK(out)
	if res.NextCursor != "" {
		r.Warn(fmt.Sprintf("the account has more than %d apps; only the first %d were searched", appsListMax, appsListMax))
	}
	return r, nil
}

func summarizeApp(r tool.Resource, platformOf map[string]string) appSummary {
	a := appSummary{
		ID: r.ID, Name: r.Str("name"), BundleID: r.Str("bundleId"), SKU: r.Str("sku"),
		PrimaryLocale: r.Str("primaryLocale"), Platforms: []string{},
	}
	for _, id := range r.RelIDs("appStoreVersions") {
		if p := platformOf[id]; p != "" && !slices.Contains(a.Platforms, p) {
			a.Platforms = append(a.Platforms, p)
		}
	}
	slices.SortFunc(a.Platforms, func(x, y string) int {
		return cmp.Compare(slices.Index(platformEnum, any(x)), slices.Index(platformEnum, any(y)))
	})
	return a
}

// fuzzyScore ranks how well query matches an app; 0 means no match and an
// empty query matches everything equally.
func fuzzyScore(query string, a appSummary) int {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 1
	}
	name, bundle, sku := strings.ToLower(a.Name), strings.ToLower(a.BundleID), strings.ToLower(a.SKU)
	switch {
	case q == a.ID || q == name || q == bundle || (sku != "" && q == sku):
		return 100
	case strings.HasPrefix(name, q):
		return 80
	case strings.Contains(name, q):
		return 70
	case strings.Contains(bundle, q) || (sku != "" && strings.Contains(sku, q)):
		return 60
	}
	hay := name + " " + bundle + " " + sku
	allTokens := true
	for tok := range strings.FieldsSeq(q) {
		allTokens = allTokens && strings.Contains(hay, tok)
	}
	if allTokens {
		return 40
	}
	if isSubsequence(strings.ReplaceAll(q, " ", ""), name) {
		return 20
	}
	return 0
}

// isSubsequence reports whether the runes of q appear in s in order
// ("hjl" matches "Hjul").
func isSubsequence(q, s string) bool {
	rs := []rune(s)
	i := 0
	for _, c := range q {
		for i < len(rs) && rs[i] != c {
			i++
		}
		if i == len(rs) {
			return false
		}
		i++
	}
	return true
}

func appsGet(ctx context.Context, env *tool.Env, in appsGetIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+url.PathEscape(app.ID), url.Values{
		"include":                  {"appStoreVersions"},
		"fields[apps]":             {appListFields},
		"fields[appStoreVersions]": {versionFields},
		"limit[appStoreVersions]":  {"50"},
	})
	if err != nil {
		return nil, fmt.Errorf("get app: %w", err)
	}
	r, ok := resp.First()
	if !ok {
		return nil, fmt.Errorf("get app %s: empty response", app.ID)
	}
	builds, err := env.ASC.List(ctx, "/v1/builds", tool.ListOptions{
		Query: url.Values{
			"filter[app]":                {app.ID},
			"sort":                       {"-uploadedDate"},
			"fields[builds]":             {buildFields},
			"fields[preReleaseVersions]": {"version,platform"},
		},
		Include:  []string{"preReleaseVersion"},
		Limit:    latestBuilds,
		MaxItems: latestBuilds,
	})
	if err != nil {
		return nil, fmt.Errorf("list builds: %w", err)
	}
	infos, err := env.ASC.List(ctx, "/v1/apps/"+url.PathEscape(app.ID)+"/appInfos", tool.ListOptions{
		Query: url.Values{
			"fields[appInfos]":             {"appInfoLocalizations"},
			"fields[appInfoLocalizations]": {"locale"},
			"limit[appInfoLocalizations]":  {"50"},
		},
		Include:  []string{"appInfoLocalizations"},
		MaxItems: 10,
	})
	if err != nil {
		return nil, fmt.Errorf("list app infos: %w", err)
	}

	var versions []versionSummary
	platformOf := map[string]string{}
	for _, inc := range resp.Included {
		if inc.Type != "appStoreVersions" {
			continue
		}
		platformOf[inc.ID] = inc.Str("platform")
		state := inc.Str("appVersionState")
		if state == "" {
			state = inc.Str("appStoreState")
		}
		versions = append(versions, versionSummary{
			ID: inc.ID, Platform: inc.Str("platform"), Version: inc.Str("versionString"),
			State: state, Release: inc.Str("releaseType"), Created: inc.Str("createdDate"),
		})
	}
	out := appOverview{
		appSummary: summarizeApp(r, platformOf),
		Live:       []versionSummary{}, InFlight: []versionSummary{},
		Builds: summarizeBuilds(builds), Locales: localesOf(infos),
	}
	out.Live, out.InFlight = splitVersions(versions)
	return tool.OK(out), nil
}

// splitVersions returns the newest live version per platform and every
// version still moving through preparation, review or release.
func splitVersions(vs []versionSummary) (live, inFlight []versionSummary) {
	slices.SortStableFunc(vs, func(a, b versionSummary) int { return cmp.Compare(b.Created, a.Created) })
	live, inFlight = []versionSummary{}, []versionSummary{}
	seen := map[string]bool{}
	for _, v := range vs {
		switch v.State {
		case "READY_FOR_DISTRIBUTION", "READY_FOR_SALE":
			if !seen[v.Platform] {
				seen[v.Platform] = true
				live = append(live, v)
			}
		case "REPLACED_WITH_NEW_VERSION", "NOT_APPLICABLE", "REMOVED_FROM_SALE", "DEVELOPER_REMOVED_FROM_SALE":
		default:
			inFlight = append(inFlight, v)
		}
	}
	return live, inFlight
}

func summarizeBuilds(res *tool.ListResult) []buildSummary {
	pre := map[string]tool.Resource{}
	for _, inc := range res.Included {
		if inc.Type == "preReleaseVersions" {
			pre[inc.ID] = inc
		}
	}
	out := make([]buildSummary, 0, len(res.Data))
	for _, b := range res.Data {
		expired, _ := b.Bool("expired")
		s := buildSummary{
			ID: b.ID, Number: b.Str("version"), State: b.Str("processingState"), Expired: expired,
			Uploaded: b.Str("uploadedDate"), MinOS: b.Str("minOsVersion"),
		}
		if p, ok := pre[b.RelID("preReleaseVersion")]; ok {
			s.Version, s.Platform = p.Str("version"), p.Str("platform")
		}
		out = append(out, s)
	}
	return out
}

func localesOf(res *tool.ListResult) []string {
	locales := []string{}
	for _, inc := range res.Included {
		if l := inc.Str("locale"); inc.Type == "appInfoLocalizations" && l != "" && !slices.Contains(locales, l) {
			locales = append(locales, l)
		}
	}
	slices.Sort(locales)
	return locales
}
