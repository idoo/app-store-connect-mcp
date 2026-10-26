package resolve

import (
	"context"
	"errors"
	"fmt"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Fallback says what [Target] does when the version reference is "" or
// "editable" and the app has no editable version.
type Fallback struct {
	// Live falls back to the live version (Resolved.FellBack reports it).
	Live bool
	// NoEditable, when Live is false and non-nil, replaces the resolver's
	// not-found error: it is wrapped as "<app> <platform>: NoEditable" with a
	// release_prepare next step.
	NoEditable error
	// FirstVersion also reports whether the app has never had a live
	// version (one extra lookup when the target is not live itself).
	FirstVersion bool
}

// Resolved is an app, platform and existing version.
type Resolved struct {
	App      tool.App
	Platform tool.Platform
	Version  tool.Version
	// FirstVersion is true when the app has never had a live version (only
	// computed with Fallback.FirstVersion).
	FirstVersion bool
	// FellBack is true when no version was editable and the live one was used.
	FellBack bool
}

// Target resolves app, platform and version through env.Resolve, applying fb
// when nothing is editable. A version that does not exist yet (e.g. "next")
// is a not-found error pointing at release_prepare.
func Target(ctx context.Context, env *tool.Env, appRef tool.AppRef, platform tool.Platform, ref tool.VersionRef, fb Fallback) (*Resolved, error) {
	app, err := env.Resolve.App(ctx, appRef)
	if err != nil {
		return nil, err
	}
	t := &Resolved{App: app, Platform: app.Platform(platform)}
	v, err := env.Resolve.Version(ctx, app.ID, t.Platform, ref)
	var nf *tool.ErrNotFound
	if err != nil && (ref == "" || ref == tool.VersionEditable) && errors.As(err, &nf) {
		switch {
		case fb.Live:
			v, err = env.Resolve.Version(ctx, app.ID, t.Platform, tool.VersionLive)
			t.FellBack = err == nil
		case fb.NoEditable != nil:
			return nil, tool.WithNext(fmt.Errorf("%s %s: %w", app.Name, t.Platform, fb.NoEditable),
				tool.NextStep{Tool: "release_prepare", Args: map[string]any{"app": app.ID, "platform": string(t.Platform)}})
		}
	}
	if err != nil {
		return nil, err
	}
	if v.ID == "" {
		return nil, tool.WithNext(&tool.ErrNotFound{Kind: "version", Ref: v.String, Hint: "the version does not exist yet; create it with release_prepare"},
			tool.NextStep{Tool: "release_prepare", Args: map[string]any{"app": app.ID, "platform": string(t.Platform), "version": v.String}})
	}
	t.Version = v
	if fb.FirstVersion && v.State != stateLive {
		if _, err := env.Resolve.Version(ctx, app.ID, t.Platform, tool.VersionLive); err != nil {
			if !errors.As(err, &nf) {
				return nil, err
			}
			t.FirstVersion = true
		}
	}
	return t, nil
}
