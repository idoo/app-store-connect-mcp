package resolve

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/tool"
)

// VersionNext is the VersionRef for the patch bump of the live version. When
// no such version exists yet, Version returns it with an empty ID.
const VersionNext tool.VersionRef = "next"

// versionFields is the sparse fieldset requested for App Store versions.
const versionFields = "platform,versionString,appVersionState,appStoreState,createdDate"

// stateLive is the AppVersionState of the version currently on the store.
const stateLive = "READY_FOR_DISTRIBUTION"

// editableStates are the AppVersionState values of a version that is neither
// released nor with App Review, so its metadata and build can still change.
var editableStates = []string{
	"PREPARE_FOR_SUBMISSION",
	"DEVELOPER_REJECTED",
	"REJECTED",
	"METADATA_REJECTED",
	"INVALID_BINARY",
	"READY_FOR_REVIEW",
	"WAITING_FOR_EXPORT_COMPLIANCE",
}

// inFlightStates are submitted-but-unreleased states (locked for editing).
var inFlightStates = []string{
	"WAITING_FOR_REVIEW",
	"IN_REVIEW",
	"ACCEPTED",
	"PENDING_DEVELOPER_RELEASE",
	"PENDING_APPLE_RELEASE",
	"PROCESSING_FOR_DISTRIBUTION",
}

var versionString = regexp.MustCompile(`^\d+(\.\d+){0,3}$`)

// Version implements [tool.Resolver]. ref is "" or "editable" (the version
// that can still be changed), "live", "next" (patch bump of live), a version
// string such as "2.1", or an appStoreVersion id. An empty platform is
// inferred from the app's versions.
func (r *Resolver) Version(ctx context.Context, appID string, platform tool.Platform, ref tool.VersionRef) (tool.Version, error) {
	plat, err := r.platform(ctx, appID, platform)
	if err != nil {
		return tool.Version{}, err
	}
	s := strings.TrimSpace(string(ref))
	switch kw := tool.VersionRef(strings.ToLower(s)); {
	case kw == "" || kw == tool.VersionEditable:
		return r.editableVersion(ctx, appID, plat)
	case kw == tool.VersionLive:
		vs, err := r.versions(ctx, appID, plat, nil)
		if err != nil {
			return tool.Version{}, err
		}
		if v, ok := newest(vs, stateLive); ok {
			return v, nil
		}
		return tool.Version{}, &tool.ErrNotFound{
			Kind: "version", Ref: s, Hint: "the app has no version on the App Store" + forPlatform(plat),
			Next: next("apps_get", map[string]any{"app": appID}),
		}
	case kw == VersionNext:
		return r.nextVersion(ctx, appID, plat)
	case versionString.MatchString(s):
		vs, err := r.versions(ctx, appID, plat, url.Values{"filter[versionString]": {s}})
		if err != nil {
			return tool.Version{}, err
		}
		if len(vs) > 0 {
			return vs[0].Version, nil
		}
		return tool.Version{}, &tool.ErrNotFound{
			Kind: "version", Ref: s, Hint: "no App Store version with this version string" + forPlatform(plat),
			Next: next("release_prepare", map[string]any{"app": appID, "version": s}),
		}
	default:
		return r.versionByID(ctx, appID, s)
	}
}

func (r *Resolver) editableVersion(ctx context.Context, appID string, plat tool.Platform) (tool.Version, error) {
	vs, err := r.versions(ctx, appID, plat, nil)
	if err != nil {
		return tool.Version{}, err
	}
	if v, ok := newest(vs, editableStates...); ok {
		return v, nil
	}
	if v, ok := newest(vs, inFlightStates...); ok {
		return tool.Version{}, &tool.ErrNotFound{
			Kind: "editable version",
			Hint: fmt.Sprintf("version %s is %s and cannot be edited; cancel the submission or wait", v.String, v.State),
			Next: next("release_review_status", map[string]any{"app": appID}),
		}
	}
	return tool.Version{}, &tool.ErrNotFound{
		Kind: "editable version",
		Hint: "no version is being prepared" + forPlatform(plat),
		Next: next("release_prepare", map[string]any{"app": appID}),
	}
}

func (r *Resolver) nextVersion(ctx context.Context, appID string, plat tool.Platform) (tool.Version, error) {
	vs, err := r.versions(ctx, appID, plat, nil)
	if err != nil {
		return tool.Version{}, err
	}
	live, ok := newest(vs, stateLive)
	if !ok {
		return tool.Version{}, &tool.ErrNotFound{
			Kind: "version", Ref: string(VersionNext),
			Hint: "no live version to bump" + forPlatform(plat) + "; pass an explicit version string",
			Next: next("release_prepare", map[string]any{"app": appID}),
		}
	}
	bumped, ok := patchBump(live.String)
	if !ok {
		return tool.Version{}, &tool.ErrNotFound{
			Kind: "version", Ref: string(VersionNext),
			Hint: fmt.Sprintf("live version %q is not numeric; pass an explicit version string", live.String),
			Next: next("release_prepare", map[string]any{"app": appID}),
		}
	}
	for _, v := range vs {
		if v.String == bumped && v.Platform == live.Platform {
			return v.Version, nil
		}
	}
	return tool.Version{String: bumped, Platform: live.Platform}, nil
}

func (r *Resolver) versionByID(ctx context.Context, appID, id string) (tool.Version, error) {
	res, err := r.asc.Get(ctx, "/v1/appStoreVersions/"+id, url.Values{"fields[appStoreVersions]": {versionFields}})
	if err != nil {
		if isNotFound(err) {
			return tool.Version{}, &tool.ErrNotFound{
				Kind: "version", Ref: id, Hint: "use editable, live, next, a version string or an appStoreVersion id",
				Next: next("apps_get", map[string]any{"app": appID}),
			}
		}
		return tool.Version{}, fmt.Errorf("resolve version %q: %w", id, err)
	}
	first, ok := res.First()
	if !ok {
		return tool.Version{}, &tool.ErrNotFound{Kind: "version", Ref: id, Next: next("apps_get", map[string]any{"app": appID})}
	}
	return versionFromResource(first), nil
}

// version is a listed App Store version plus its creation time.
type version struct {
	tool.Version
	created time.Time
}

// versions lists the app's App Store versions (newest 50) for plat.
func (r *Resolver) versions(ctx context.Context, appID string, plat tool.Platform, extra url.Values) ([]version, error) {
	q := url.Values{"fields[appStoreVersions]": {versionFields}, "limit": {"50"}}
	if plat != "" {
		q.Set("filter[platform]", string(plat))
	}
	for k, v := range extra {
		q[k] = v
	}
	res, err := r.asc.Get(ctx, "/v1/apps/"+appID+"/appStoreVersions", q)
	if err != nil {
		return nil, fmt.Errorf("list versions of app %s: %w", appID, err)
	}
	out := make([]version, 0, len(res.Data))
	for _, d := range res.Data {
		created, _ := time.Parse(time.RFC3339, d.Str("createdDate"))
		out = append(out, version{Version: versionFromResource(d), created: created})
	}
	return out, nil
}

// newest returns the most recently created version in one of states. Equal
// or missing creation dates keep the API's order.
func newest(vs []version, states ...string) (tool.Version, bool) {
	var best version
	found := false
	for _, v := range vs {
		if !slices.Contains(states, v.State) {
			continue
		}
		if !found || v.created.After(best.created) {
			best, found = v, true
		}
	}
	return best.Version, found
}

func versionFromResource(d tool.Resource) tool.Version {
	return tool.Version{ID: d.ID, String: d.Str("versionString"), Platform: tool.Platform(d.Str("platform")), State: versionState(d)}
}

// versionState reads appVersionState, falling back to the deprecated
// appStoreState (whose READY_FOR_SALE means live).
func versionState(d tool.Resource) string {
	if s := d.Str("appVersionState"); s != "" {
		return s
	}
	if s := d.Str("appStoreState"); s != "READY_FOR_SALE" {
		return s
	}
	return stateLive
}

// patchBump returns v with its third component incremented ("2.1" →
// "2.1.1", "2.1.3" → "2.1.4"); versions with a fourth component bump it.
func patchBump(v string) (string, bool) {
	if !versionString.MatchString(v) {
		return "", false
	}
	parts := strings.Split(v, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	last := len(parts) - 1
	n, err := strconv.Atoi(parts[last])
	if err != nil {
		return "", false
	}
	parts[last] = strconv.Itoa(n + 1)
	return strings.Join(parts, "."), true
}

func forPlatform(p tool.Platform) string {
	if p == "" {
		return ""
	}
	return " for " + string(p)
}
