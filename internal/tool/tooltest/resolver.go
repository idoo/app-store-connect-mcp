package tooltest

import (
	"context"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// FakeResolver is a data-driven [tool.Resolver]. Fill its fields with the
// objects a test needs; lookups follow the real resolver's matching rules in
// simplified form (exact id / bundle id / SKU, then case-insensitive name
// substring; locale aliases by language prefix).
type FakeResolver struct {
	Apps []tool.App
	// DefaultApp is the app id used for an empty AppRef (else Apps[0]).
	DefaultApp string
	// Versions maps a VersionRef ("editable", "live", "2.1") to a version.
	Versions map[tool.VersionRef]tool.Version
	// Builds maps a BuildRef ("latest", "latest-valid", number or id) to a build.
	Builds map[tool.BuildRef]tool.Build
	// Locales are the app's locales returned for "all".
	AppLocales []string
	Groups     []tool.Group
}

// App implements tool.Resolver.
func (r *FakeResolver) App(_ context.Context, ref tool.AppRef) (tool.App, error) {
	s := string(ref)
	if s == "" {
		s = r.DefaultApp
		if s == "" && len(r.Apps) > 0 {
			return r.Apps[0], nil
		}
	}
	for _, a := range r.Apps {
		if a.ID == s || a.BundleID == s || (a.SKU != "" && a.SKU == s) {
			return a, nil
		}
	}
	var hits []tool.App
	for _, a := range r.Apps {
		if strings.Contains(strings.ToLower(a.Name), strings.ToLower(s)) {
			hits = append(hits, a)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return tool.App{}, &tool.ErrNotFound{Kind: "app", Ref: s, Next: []tool.NextStep{{Tool: "apps_list"}}}
	}
	amb := &tool.ErrAmbiguous{Kind: "app", Ref: s}
	for _, a := range hits {
		amb.Candidates = append(amb.Candidates, tool.Candidate{ID: a.ID, Label: a.Name + " (" + a.BundleID + ")"})
	}
	return tool.App{}, amb
}

// Version implements tool.Resolver.
func (r *FakeResolver) Version(_ context.Context, appID string, _ tool.Platform, ref tool.VersionRef) (tool.Version, error) {
	if ref == "" {
		ref = tool.VersionEditable
	}
	if v, ok := r.Versions[ref]; ok {
		return v, nil
	}
	return tool.Version{}, &tool.ErrNotFound{Kind: "version", Ref: string(ref), Next: []tool.NextStep{{Tool: "apps_get", Args: map[string]any{"app": appID}}}}
}

// Build implements tool.Resolver.
func (r *FakeResolver) Build(_ context.Context, appID string, _ tool.Platform, ref tool.BuildRef) (tool.Build, error) {
	if b, ok := r.Builds[ref]; ok {
		return b, nil
	}
	for _, b := range r.Builds {
		if b.ID == string(ref) || b.Number == string(ref) {
			return b, nil
		}
	}
	return tool.Build{}, &tool.ErrNotFound{Kind: "build", Ref: string(ref), Next: []tool.NextStep{{Tool: "builds_list", Args: map[string]any{"app": appID}}}}
}

// Locales implements tool.Resolver.
func (r *FakeResolver) Locales(_ context.Context, _ string, set tool.LocaleSet) ([]string, error) {
	if set.IsAll() {
		return append([]string(nil), r.AppLocales...), nil
	}
	out := make([]string, 0, len(set))
	for _, want := range set {
		got := ""
		for _, l := range r.AppLocales {
			if strings.EqualFold(l, want) || strings.HasPrefix(strings.ToLower(l), strings.ToLower(want)+"-") {
				got = l
				break
			}
		}
		if got == "" {
			return nil, &tool.ErrNotFound{Kind: "locale", Ref: want, Hint: "app locales: " + strings.Join(r.AppLocales, ", ")}
		}
		out = append(out, got)
	}
	return out, nil
}

// Group implements tool.Resolver.
func (r *FakeResolver) Group(_ context.Context, appID string, ref tool.GroupRef) (tool.Group, error) {
	for _, g := range r.Groups {
		if g.ID == string(ref) || strings.EqualFold(g.Name, string(ref)) {
			return g, nil
		}
	}
	return tool.Group{}, &tool.ErrNotFound{Kind: "beta group", Ref: string(ref), Next: []tool.NextStep{{Tool: "testflight_groups_list", Args: map[string]any{"app": appID}}}}
}
