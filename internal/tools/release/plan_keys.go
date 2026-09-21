package release

// Shared plan plumbing for release_status (which issues plans) and
// release_prepare / release_submit (which verify them).
//
// release_status is a Read tool, so it never mutates; when given `desired`
// it proposes plans on behalf of the executing tools. For a plan to verify,
// the executing tool MUST bind it to exactly the same canonical args and
// fingerprint:
//
//	key, err := prepareArgsKey(app, platform, versionString, buildID, in.releaseDesired)
//	snap, err := loadReleaseSnapshot(ctx, env, app.ID, platform, versionRef)
//	fp, err := snap.State.fingerprint()
//	wctx, res := tool.Preview(ctx, env, in.PlanID, key, fp, preview)
//
// and release_submit uses submitArgsKey + the snapshot fingerprint the same way.
// Keys are built from RESOLVED values (app id, platform, version string,
// build id), so "Hjul"/"latest" and the ids they resolve to share a plan.
//
// The release_submit plan proposed by release_status is fingerprinted over
// the state release_prepare is expected to leave behind (intended build and
// What's New), so it stays valid after a confirmed prepare but turns stale if
// anything else changes.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
)

// Release types (AppStoreVersion.releaseType).
const (
	releaseManual        = "MANUAL"
	releaseAfterApproval = "AFTER_APPROVAL"
	releaseScheduled     = "SCHEDULED"
)

// releaseDesired is the intended release configuration. release_status takes
// it as its `desired` object; release_prepare embeds it (flat) in its input so
// both tools speak the same argument names.
type releaseDesired struct {
	Build       tool.BuildRef     `json:"build,omitempty" jsonschema:"latest, latest-valid, build number or id"`
	WhatsNew    map[string]string `json:"whats_new,omitempty" jsonschema:"What's New per locale; * = all existing"`
	ReleaseType string            `json:"release_type,omitempty"`
	// EarliestReleaseDate is RFC 3339, used with release_type SCHEDULED.
	EarliestReleaseDate     string `json:"earliest_release_date,omitempty" jsonschema:"RFC 3339, for SCHEDULED"`
	Phased                  *bool  `json:"phased,omitempty" jsonschema:"7-day phased release"`
	UsesNonExemptEncryption *bool  `json:"uses_non_exempt_encryption,omitempty" jsonschema:"export compliance answer"`
	Submit                  bool   `json:"submit,omitempty" jsonschema:"also plan release_submit"`
}

// releasePlanKey is the canonical plan argument set of release_prepare.
type releasePlanKey struct {
	Tool                    string            `json:"tool"`
	AppID                   string            `json:"app_id"`
	Platform                tool.Platform     `json:"platform"`
	Version                 string            `json:"version"`
	BuildID                 string            `json:"build_id,omitempty"`
	WhatsNew                map[string]string `json:"whats_new,omitempty"`
	ReleaseType             string            `json:"release_type,omitempty"`
	EarliestReleaseDate     string            `json:"earliest_release_date,omitempty"`
	Phased                  *bool             `json:"phased,omitempty"`
	UsesNonExemptEncryption *bool             `json:"uses_non_exempt_encryption,omitempty"`
}

// submitPlanKey is the canonical plan argument set of release_submit.
type submitPlanKey struct {
	Tool     string        `json:"tool"`
	AppID    string        `json:"app_id"`
	Platform tool.Platform `json:"platform"`
	Version  string        `json:"version"`
}

// prepareArgsKey returns the canonical args a release_prepare plan is bound
// to. versionString is the resolved version string (or the string to create),
// buildID the resolved build id ("" when no build is requested). What's New
// locale keys are normalized to ASC codes ("*" is kept); an unknown locale is
// an error. The Submit flag is not part of the key.
func prepareArgsKey(app tool.App, platform tool.Platform, versionString, buildID string, d releaseDesired) (releasePlanKey, error) {
	wn, err := normalizeWhatsNew(d.WhatsNew)
	if err != nil {
		return releasePlanKey{}, err
	}
	return releasePlanKey{
		Tool: "release_prepare", AppID: app.ID, Platform: platform, Version: versionString,
		BuildID: buildID, WhatsNew: wn, ReleaseType: strings.ToUpper(d.ReleaseType),
		EarliestReleaseDate: d.EarliestReleaseDate, Phased: d.Phased,
		UsesNonExemptEncryption: d.UsesNonExemptEncryption,
	}, nil
}

// submitArgsKey returns the canonical args a release_submit plan is bound to.
func submitArgsKey(app tool.App, platform tool.Platform, versionString string) submitPlanKey {
	return submitPlanKey{Tool: "release_submit", AppID: app.ID, Platform: platform, Version: versionString}
}

// normalizeWhatsNew maps locale keys to ASC codes, keeping "*".
func normalizeWhatsNew(in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if strings.TrimSpace(k) == "*" {
			out["*"] = v
			continue
		}
		loc, err := applemeta.NormalizeLocale(k)
		if err != nil {
			return nil, fmt.Errorf("whats_new: %w", err)
		}
		out[loc] = v
	}
	return out, nil
}

// releaseState is the remote state release plans depend on.
type releaseState struct {
	Version      string            `json:"version"`
	Platform     tool.Platform     `json:"platform"`
	VersionState string            `json:"version_state"`
	BuildID      string            `json:"build_id"`
	WhatsNew     map[string]string `json:"whats_new"`
	Submission   string            `json:"submission"`
}

// fingerprint hashes the state.
func (s releaseState) fingerprint() (string, error) { return tool.Fingerprint(s) }

// versionDetail is one App Store version with its included resources.
type versionDetail struct {
	Version  tool.Resource
	Included []tool.Resource
}

// included returns the included resources of type typ referenced by rel.
func (v *versionDetail) related(rel, typ string) []tool.Resource {
	ids := v.Version.RelIDs(rel)
	var out []tool.Resource
	for _, r := range v.Included {
		if r.Type == typ && slices.Contains(ids, r.ID) {
			out = append(out, r)
		}
	}
	return out
}

func (v *versionDetail) one(rel, typ string) (tool.Resource, bool) {
	rs := v.related(rel, typ)
	if len(rs) == 0 {
		return tool.Resource{}, false
	}
	return rs[0], true
}

// localizations returns the version localizations sorted by locale.
func (v *versionDetail) localizations() []tool.Resource {
	locs := v.related("appStoreVersionLocalizations", "appStoreVersionLocalizations")
	slices.SortFunc(locs, func(a, b tool.Resource) int { return strings.Compare(a.Str("locale"), b.Str("locale")) })
	return locs
}

// fetchVersionDetail GETs a version with localizations, build, review detail
// and phased release in one request.
func fetchVersionDetail(ctx context.Context, env *tool.Env, versionID string) (*versionDetail, error) {
	resp, err := env.ASC.Get(ctx, "/v1/appStoreVersions/"+versionID, url.Values{
		"include":                             {"appStoreVersionLocalizations,build,appStoreReviewDetail,appStoreVersionPhasedRelease"},
		"limit[appStoreVersionLocalizations]": {"50"},
	})
	if err != nil {
		return nil, fmt.Errorf("get version %s: %w", versionID, err)
	}
	v, ok := resp.First()
	if !ok {
		return nil, fmt.Errorf("get version %s: empty response: %w", versionID, tool.ErrInternal)
	}
	return &versionDetail{Version: v, Included: resp.Included}, nil
}

// openSubmissionStates are reviewSubmission states that block a new submission.
var openSubmissionStates = []string{"READY_FOR_REVIEW", "WAITING_FOR_REVIEW", "IN_REVIEW", "UNRESOLVED_ISSUES"}

// fetchOpenSubmission returns the app's open review submission for platform,
// if any.
func fetchOpenSubmission(ctx context.Context, env *tool.Env, appID string, platform tool.Platform) (tool.Resource, bool, error) {
	resp, err := env.ASC.Get(ctx, "/v1/reviewSubmissions", url.Values{
		"filter[app]":      {appID},
		"filter[platform]": {string(platform)},
		"filter[state]":    {strings.Join(openSubmissionStates, ",")},
		"limit":            {"5"},
	})
	if err != nil {
		return tool.Resource{}, false, fmt.Errorf("list review submissions: %w", err)
	}
	s, ok := resp.First()
	return s, ok, nil
}

// stateFromVersion builds the plan state from a version detail (nil when the
// version does not exist yet) and the open submission state.
func stateFromVersion(versionString string, platform tool.Platform, v *versionDetail, submission string) releaseState {
	st := releaseState{Version: versionString, Platform: platform, Submission: submission, WhatsNew: map[string]string{}}
	if v == nil {
		return st
	}
	st.Version = v.Version.Str("versionString")
	st.VersionState = v.Version.Str("appVersionState")
	st.BuildID = v.Version.RelID("build")
	for _, l := range v.localizations() {
		st.WhatsNew[l.Str("locale")] = l.Str("whatsNew")
	}
	return st
}

// versionedBuildRef pins "latest" and "latest-valid" to the version's
// marketing version, so a newer upload of another version (a 2.4 TestFlight
// build while 2.3.1 is prepared) is never picked. Other refs are kept.
func versionedBuildRef(ref tool.BuildRef, versionString string) tool.BuildRef {
	r := strings.ToLower(strings.TrimSpace(string(ref)))
	if versionString == "" || (r != string(tool.BuildLatest) && r != string(tool.BuildLatestValid)) {
		return ref
	}
	return tool.BuildRef("latest:" + versionString)
}

// buildMarketingVersion returns the build's preReleaseVersion.version
// ("" when Apple has none).
func buildMarketingVersion(ctx context.Context, env *tool.Env, buildID string) (string, error) {
	resp, err := env.ASC.Get(ctx, "/v1/builds/"+buildID+"/preReleaseVersion", url.Values{"fields[preReleaseVersions]": {"version"}})
	switch {
	case err == nil:
		r, _ := resp.First()
		return r.Str("version"), nil
	case isNotFound(err):
		return "", nil
	default:
		return "", fmt.Errorf("get build %s version: %w", buildID, err)
	}
}

// readyProducts lists the ready products release_submit attaches by default.
func readyProducts(s *statusSnapshot) []string {
	out := make([]string, 0, len(s.ReadyIAPs)+len(s.ReadySubscriptions))
	for _, p := range s.ReadyIAPs {
		out = append(out, "attach iap "+p)
	}
	for _, p := range s.ReadySubscriptions {
		out = append(out, "attach subscription "+p)
	}
	slices.Sort(out)
	return out
}

// concreteVersion reports whether ref is a version string rather than a keyword.
func concreteVersion(ref tool.VersionRef) bool {
	return ref != "" && ref != tool.VersionEditable && ref != tool.VersionLive
}

// isNotFound reports a resolver miss or an HTTP 404.
func isNotFound(err error) bool {
	var nf *tool.ErrNotFound
	if errors.As(err, &nf) {
		return true
	}
	var api *tool.APIError
	return errors.As(err, &api) && api.Status == http.StatusNotFound
}

// intendedState applies the prepare changes to a current state.
func intendedState(cur releaseState, buildID string, whatsNew map[string]string) releaseState {
	out := cur
	out.WhatsNew = maps.Clone(cur.WhatsNew)
	if out.VersionState == "" {
		out.VersionState = "PREPARE_FOR_SUBMISSION"
	}
	if buildID != "" {
		out.BuildID = buildID
	}
	maps.Copy(out.WhatsNew, whatsNew)
	return out
}

// ascBase is the App Store Connect web app.
const ascBase = "https://appstoreconnect.apple.com/apps/"

// ascURL returns an App Store Connect deep link for an app section, e.g.
// "distribution/privacy".
func ascURL(appID, section string) string { return ascBase + url.PathEscape(appID) + "/" + section }

// versionURL returns the deep link of the in-flight version page.
func versionURL(appID string, platform tool.Platform) string {
	p := map[tool.Platform]string{tool.IOS: "ios", tool.MacOS: "macos", tool.TVOS: "tvos", tool.VisionOS: "visionos"}[platform]
	if p == "" {
		p = "ios"
	}
	return ascURL(appID, "distribution/"+p+"/version/inflight")
}
