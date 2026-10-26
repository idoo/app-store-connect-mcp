package resolve

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// buildFields is the sparse fieldset requested for builds.
const buildFields = "version,processingState,expired,uploadedDate"

// latestPrefix introduces "latest:<marketing version>".
const latestPrefix = "latest:"

// latestValidPrefix introduces "latest-valid:<marketing version>": the newest
// non-expired VALID build of that version.
const latestValidPrefix = "latest-valid:"

var buildNumber = regexp.MustCompile(`^\d+(\.\d+)*$`)

// Build implements [tool.Resolver]. ref is "" or "latest" (newest
// non-expired upload), "latest-valid" (newest non-expired VALID build),
// "latest:<marketing version>", "latest-valid:<marketing version>", a build
// number (CFBundleVersion) or a build id. An empty platform is inferred from
// the app's versions.
func (r *Resolver) Build(ctx context.Context, appID string, platform tool.Platform, ref tool.BuildRef) (tool.Build, error) {
	s := strings.TrimSpace(string(ref))
	low := strings.ToLower(s)
	isKeyword := low == "" || low == string(tool.BuildLatest) || low == string(tool.BuildLatestValid) ||
		strings.HasPrefix(low, latestPrefix) || strings.HasPrefix(low, latestValidPrefix)
	if !isKeyword && !buildNumber.MatchString(s) {
		return r.buildByID(ctx, appID, s)
	}
	marketing, valid := "", low == string(tool.BuildLatestValid)
	for _, p := range []string{latestPrefix, latestValidPrefix} {
		if !strings.HasPrefix(low, p) {
			continue
		}
		valid = p == latestValidPrefix
		if marketing = strings.TrimSpace(s[len(p):]); marketing == "" {
			return tool.Build{}, &tool.ErrNotFound{
				Kind: "build", Ref: s, Hint: "use " + p + "<marketing version>, e.g. " + p + "2.1",
				Next: next("builds_list", map[string]any{"app": appID}),
			}
		}
	}
	plat, err := r.platform(ctx, appID, platform)
	if err != nil {
		return tool.Build{}, err
	}
	q := url.Values{
		"filter[app]":    {appID},
		"sort":           {"-uploadedDate"},
		"fields[builds]": {buildFields},
		"limit":          {"1"},
	}
	if plat != "" {
		q.Set("filter[preReleaseVersion.platform]", string(plat))
	}
	switch {
	case !isKeyword:
		// Build numbers are unique per marketing version only: never guess.
		q.Set("filter[version]", s)
		q.Set("limit", "5")
	default:
		q.Set("filter[expired]", "false")
		if valid {
			q.Set("filter[processingState]", "VALID")
		}
		if marketing != "" {
			q.Set("filter[preReleaseVersion.version]", marketing)
		}
	}
	res, err := r.asc.Get(ctx, "/v1/builds", q)
	if err != nil {
		return tool.Build{}, fmt.Errorf("resolve build %q: %w", s, err)
	}
	switch len(res.Data) {
	case 0:
		return tool.Build{}, &tool.ErrNotFound{
			Kind: "build", Ref: s, Hint: notFoundHint(s, valid, marketing) + forPlatform(plat) + "; uploads take a few minutes to appear",
			Next: next("builds_list", map[string]any{"app": appID}),
		}
	case 1:
		return buildFromResource(res.Data[0]), nil
	}
	amb := &tool.ErrAmbiguous{Kind: "build", Ref: s, Next: next("builds_list", map[string]any{"app": appID})}
	for _, d := range res.Data[:min(len(res.Data), maxCandidates)] {
		amb.Candidates = append(amb.Candidates, tool.Candidate{
			ID: d.ID, Label: "build " + d.Str("version") + " uploaded " + d.Str("uploadedDate"),
		})
	}
	return tool.Build{}, amb
}

// notFoundHint echoes what the user asked for.
func notFoundHint(ref string, valid bool, marketing string) string {
	if ref == "" {
		ref = string(tool.BuildLatest)
	}
	h := "no matching build for " + strconv.Quote(ref)
	switch {
	case valid && marketing != "":
		h += " (no non-expired VALID build of version " + marketing + ")"
	case valid:
		h += " (no non-expired VALID build)"
	case marketing != "":
		h += " (no non-expired build of version " + marketing + ")"
	}
	return h
}

func (r *Resolver) buildByID(ctx context.Context, appID, id string) (tool.Build, error) {
	res, err := r.asc.Get(ctx, "/v1/builds/"+id, url.Values{"fields[builds]": {buildFields}})
	if err != nil {
		if tool.IsNotFound(err) {
			return tool.Build{}, &tool.ErrNotFound{
				Kind: "build", Ref: id, Hint: "use latest, latest-valid, latest:<version>, latest-valid:<version>, a build number or a build id",
				Next: next("builds_list", map[string]any{"app": appID}),
			}
		}
		return tool.Build{}, fmt.Errorf("resolve build %q: %w", id, err)
	}
	first, ok := res.First()
	if !ok {
		return tool.Build{}, &tool.ErrNotFound{Kind: "build", Ref: id, Next: next("builds_list", map[string]any{"app": appID})}
	}
	return buildFromResource(first), nil
}

func buildFromResource(d tool.Resource) tool.Build {
	expired, _ := d.Bool("expired")
	return tool.Build{ID: d.ID, Number: d.Str("version"), ProcessingState: d.Str("processingState"), Expired: expired}
}
