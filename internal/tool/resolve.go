package tool

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// AppRef names an app: id, bundle id, SKU or fuzzy name. Empty means the
// pinned/default app of the profile.
type AppRef string

// AllApps is the reserved [AppRef] for every app of the account. Only the
// tools in [AllAppsTools] accept it (they check [AppRef.IsAll] before
// resolving); the shared resolvers refuse it with [ErrAllAppsUnsupported].
const AllApps AppRef = "all"

// AllAppsTools are the tools that accept app:"all".
var AllAppsTools = []string{"reviews_list", "digest_summary", "insights_summary"}

// IsAll reports whether ref is the reserved keyword "all".
func (r AppRef) IsAll() bool {
	return strings.EqualFold(strings.TrimSpace(string(r)), string(AllApps))
}

// ErrAllAppsUnsupported is the invalid_args error for app:"all" passed to a
// tool that works on one app.
func ErrAllAppsUnsupported() error {
	return InvalidArgs(`app "all" works only in %s — pass one app`, strings.Join(AllAppsTools, ", "))
}

// VersionRef names an App Store version: "" or "editable" (the in-flight
// version), "live", or a version string such as "2.1".
type VersionRef string

// Version reference keywords.
const (
	VersionEditable VersionRef = "editable"
	VersionLive     VersionRef = "live"
)

// BuildRef names a build: "latest", "latest-valid", a build number or an id.
type BuildRef string

// Build reference keywords.
const (
	BuildLatest      BuildRef = "latest"
	BuildLatestValid BuildRef = "latest-valid"
)

// LocaleSet is a list of locales or aliases ("de", "German", "de-DE"), or the
// single keyword "all".
type LocaleSet []string

// IsAll reports whether the set is the keyword "all".
func (l LocaleSet) IsAll() bool {
	return len(l) == 1 && strings.EqualFold(l[0], "all")
}

// GroupRef names a TestFlight beta group by name or id.
type GroupRef string

// Platform is an App Store Connect platform. Empty means "infer from the app".
type Platform string

// Platforms.
const (
	IOS      Platform = "IOS"
	MacOS    Platform = "MAC_OS"
	TVOS     Platform = "TV_OS"
	VisionOS Platform = "VISION_OS"
)

// App is a resolved app.
type App struct {
	ID        string     `json:"id"`
	BundleID  string     `json:"bundle_id"`
	Name      string     `json:"name"`
	SKU       string     `json:"sku,omitempty"`
	Platforms []Platform `json:"platforms,omitempty"`
}

// Platform returns want when set, otherwise IOS if the app supports it, else
// the app's first platform, else IOS.
func (a App) Platform(want Platform) Platform {
	switch {
	case want != "":
		return want
	case len(a.Platforms) == 0 || slices.Contains(a.Platforms, IOS):
		return IOS
	default:
		return a.Platforms[0]
	}
}

// Version is a resolved App Store version.
type Version struct {
	ID       string   `json:"id"`
	String   string   `json:"version"`
	Platform Platform `json:"platform"`
	State    string   `json:"state"`
}

// Build is a resolved build.
type Build struct {
	ID              string `json:"id"`
	Number          string `json:"number"`
	ProcessingState string `json:"processing_state"`
	Expired         bool   `json:"expired,omitempty"`
}

// Group is a resolved beta group.
type Group struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Internal bool   `json:"internal"`
}

// Resolver turns human references into App Store Connect ids. Failures are
// *ErrNotFound or *ErrAmbiguous (both carry Next suggestions). The
// implementation lives in internal/resolve; tests use tooltest.FakeResolver.
type Resolver interface {
	App(ctx context.Context, ref AppRef) (App, error)
	Version(ctx context.Context, appID string, platform Platform, ref VersionRef) (Version, error)
	Build(ctx context.Context, appID string, platform Platform, ref BuildRef) (Build, error)
	// Locales returns canonical locale codes (e.g. "de-DE"); "all" means every
	// locale the app has.
	Locales(ctx context.Context, appID string, set LocaleSet) ([]string, error)
	Group(ctx context.Context, appID string, ref GroupRef) (Group, error)
}

// Candidate is one possible match of an ambiguous reference.
type Candidate struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ErrAmbiguous reports a reference matching several objects.
type ErrAmbiguous struct {
	Kind       string      `json:"kind"`
	Ref        string      `json:"ref"`
	Candidates []Candidate `json:"candidates"`
	// Total is the true number of matches; Candidates may be capped.
	Total int        `json:"total,omitempty"`
	Next  []NextStep `json:"next,omitempty"`
}

// Error implements error.
func (e *ErrAmbiguous) Error() string {
	n := max(e.Total, len(e.Candidates))
	if e.Kind == "app" && e.Ref == "" {
		return fmt.Sprintf("which app? you have %d apps; pass app (name, bundle id or id), "+
			"or set default_app (`app-store-connect-mcp auth set <profile> default_app=…`) "+
			"or pin one with app = \"…\" in .app-store-connect-mcp.toml", n)
	}
	return fmt.Sprintf("%s %q is ambiguous (%d matches); pass an id", e.Kind, e.Ref, n)
}

// allAppsHint extends a "which app?" error of a tool that accepts app:"all".
type allAppsHint struct{ error }

func (e allAppsHint) Error() string {
	return e.error.Error() + `; or pass app:"all" to cover every app in one call`
}

func (e allAppsHint) Unwrap() error { return e.error }

// offerAllApps marks a "which app?" ambiguity raised by a tool in
// [AllAppsTools] so its message and next steps offer app:"all".
func offerAllApps(name string, err error) error {
	var amb *ErrAmbiguous
	if !slices.Contains(AllAppsTools, name) || !errors.As(err, &amb) || amb.Kind != "app" || amb.Ref != "" {
		return err
	}
	amb.Next = append([]NextStep{{Tool: name, Args: map[string]any{"app": string(AllApps)}}}, amb.Next...)
	return allAppsHint{err}
}

// NextSteps implements [NextStepper].
func (e *ErrAmbiguous) NextSteps() []NextStep { return e.Next }

// ErrNotFound reports a reference that matched nothing.
type ErrNotFound struct {
	Kind string     `json:"kind"`
	Ref  string     `json:"ref"`
	Hint string     `json:"hint,omitempty"`
	Next []NextStep `json:"next,omitempty"`
}

// Error implements error.
func (e *ErrNotFound) Error() string {
	if e.Ref == "" {
		return e.Kind + " not found"
	}
	return fmt.Sprintf("%s %q not found", e.Kind, e.Ref)
}

// NextSteps implements [NextStepper].
func (e *ErrNotFound) NextSteps() []NextStep { return e.Next }
