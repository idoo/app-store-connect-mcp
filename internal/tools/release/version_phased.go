package release

// Shared plumbing of the post-review release tools (release_review_status,
// release_publish, release_phased, builds_list, builds_wait).

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// errWrongState is returned when an object is not in a state that allows the
// requested action; the wrapping message names the current state.
var errWrongState = &tool.Error{
	Code:    "invalid_state",
	Message: "the object is not in a state that allows this action",
}

// platformEnum is the schema enum of every platform argument.
var platformEnum = []any{"IOS", "MAC_OS", "TV_OS", "VISION_OS"}

// versionView is the compact form of an App Store version in results.
type versionView struct {
	ID          string `json:"id"`
	String      string `json:"string"`
	State       string `json:"state"`
	ReleaseType string `json:"release_type,omitempty"`
}

func viewVersion(v tool.Resource) versionView {
	return versionView{ID: v.ID, String: v.Str("versionString"), State: v.Str("appVersionState"), ReleaseType: v.Str("releaseType")}
}

// phasedSchedule is Apple's 7-day phased release: share of users per day.
var phasedSchedule = []int{1, 2, 5, 10, 20, 50, 100}

// phasedPercent returns the share of users that get the update on day (0
// before the release starts).
func phasedPercent(day int) int {
	switch {
	case day <= 0:
		return 0
	case day > len(phasedSchedule):
		return 100
	}
	return phasedSchedule[day-1]
}

// phasedView is the compact form of an appStoreVersionPhasedRelease.
type phasedView struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	CurrentDay     int    `json:"current_day"`
	Percent        int    `json:"percent_of_users"`
	StartDate      string `json:"start_date,omitempty"`
	TotalPauseDays int    `json:"total_pause_days"`
}

func viewPhased(p tool.Resource) *phasedView {
	day, _ := p.Int("currentDayNumber")
	pause, _ := p.Int("totalPauseDuration")
	v := &phasedView{ID: p.ID, State: p.Str("phasedReleaseState"), CurrentDay: day, StartDate: p.Str("startDate"), TotalPauseDays: pause}
	switch v.State {
	case "COMPLETE":
		v.Percent = 100
	case "ACTIVE", "PAUSED":
		v.Percent = phasedPercent(day)
	}
	return v
}

// phasedOf returns the version's phased release, if any.
func phasedOf(v *versionDetail) *tool.Resource {
	p, ok := v.one("appStoreVersionPhasedRelease", "appStoreVersionPhasedReleases")
	if !ok {
		return nil
	}
	return &p
}

// versionPhasedQuery reads versions with their phased release in one request.
func versionPhasedQuery() url.Values {
	return url.Values{
		"include":                               {"appStoreVersionPhasedRelease"},
		"fields[appStoreVersions]":              {"versionString,platform,appVersionState,releaseType,createdDate,appStoreVersionPhasedRelease"},
		"fields[appStoreVersionPhasedReleases]": {"phasedReleaseState,currentDayNumber,startDate,totalPauseDuration"},
	}
}

// loadVersionPhased returns the version named by ref with its phased release
// (one GET after resolution). An empty ref lists the platform's recent
// versions (one GET) and picks the newest one prefer accepts, else the newest.
func loadVersionPhased(ctx context.Context, env *tool.Env, app tool.App, platform tool.Platform, ref tool.VersionRef, prefer func(*versionDetail) bool) (*versionDetail, error) {
	if ref != "" {
		ver, err := env.Resolve.Version(ctx, app.ID, platform, ref)
		if err != nil {
			return nil, err
		}
		resp, err := env.ASC.Get(ctx, "/v1/appStoreVersions/"+ver.ID, versionPhasedQuery())
		if err != nil {
			return nil, fmt.Errorf("get version %s: %w", ver.ID, err)
		}
		v, ok := resp.First()
		if !ok {
			return nil, fmt.Errorf("get version %s: empty response: %w", ver.ID, tool.ErrInternal)
		}
		return &versionDetail{Version: v, Included: resp.Included}, nil
	}
	q := versionPhasedQuery()
	q.Set("filter[platform]", string(platform))
	q.Set("limit", "10")
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+app.ID+"/appStoreVersions", q)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, errNoVersion(app, platform, ref)
	}
	vs := slices.Clone(resp.Data)
	slices.SortStableFunc(vs, func(a, b tool.Resource) int { return cmp.Compare(b.Str("createdDate"), a.Str("createdDate")) })
	for _, v := range vs {
		d := &versionDetail{Version: v, Included: resp.Included}
		if prefer(d) {
			return d, nil
		}
	}
	return &versionDetail{Version: vs[0], Included: resp.Included}, nil
}

// versionArgs are the app/platform/version arguments of a follow-up step.
func versionArgs(app tool.App, platform tool.Platform, version string) map[string]any {
	return map[string]any{"app": app.ID, "platform": string(platform), "version": version}
}
