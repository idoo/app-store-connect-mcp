package release

// Shared plumbing of the executing release tools (release_prepare,
// release_submit, release_cancel).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/idoo/hamn/internal/tool"
)

// releaseSnapshot is the remote state a release plan is fingerprinted over,
// plus the resources it was read from. It performs exactly the reads of
// [loadReleaseState], so its fingerprint equals [releaseFingerprint].
type releaseSnapshot struct {
	State      releaseState
	Detail     *versionDetail // nil when the version does not exist yet
	Submission *tool.Resource // the open review submission, if any
}

// loadReleaseSnapshot mirrors [loadReleaseState] and keeps the resources.
func loadReleaseSnapshot(ctx context.Context, env *tool.Env, appID string, platform tool.Platform, ref tool.VersionRef) (*releaseSnapshot, error) {
	if ref == "" {
		ref = tool.VersionEditable
	}
	s := &releaseSnapshot{}
	ver, err := env.Resolve.Version(ctx, appID, platform, ref)
	switch {
	case err == nil:
		if s.Detail, err = fetchVersionDetail(ctx, env, ver.ID); err != nil {
			return nil, err
		}
	case isNotFound(err) && concreteVersion(ref):
		// the version does not exist yet
	default:
		return nil, err
	}
	sub, ok, err := fetchOpenSubmission(ctx, env, appID, platform)
	if err != nil {
		return nil, err
	}
	subState := ""
	if ok {
		s.Submission = &sub
		subState = sub.Str("state")
	}
	s.State = stateFromVersion(string(ref), platform, s.Detail, subState)
	return s, nil
}

// versionID returns the id of the snapshot's version ("" when missing).
func (s *releaseSnapshot) versionID() string {
	if s.Detail == nil {
		return ""
	}
	return s.Detail.Version.ID
}

// attachedBuild returns the build attached to the version, if any.
func (s *releaseSnapshot) attachedBuild() (tool.Resource, bool) {
	if s.Detail == nil {
		return tool.Resource{}, false
	}
	return s.Detail.one("build", "builds")
}

// doWrite performs one mutation; ctx must carry the write capability.
func doWrite(ctx context.Context, env *tool.Env, method, path string, body any) (*tool.Response, error) {
	resp, err := env.ASC.Do(ctx, tool.Request{Method: method, Path: path, Body: body, Write: true})
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

// jsonapiBody builds a JSON:API request document. id is omitted when empty;
// rels maps relationship name to "type:id".
func jsonapiBody(typ, id string, attrs map[string]any, rels map[string]string) map[string]any {
	data := map[string]any{"type": typ}
	if id != "" {
		data["id"] = id
	}
	if len(attrs) > 0 {
		data["attributes"] = attrs
	}
	if len(rels) > 0 {
		r := make(map[string]any, len(rels))
		for name, ref := range rels {
			t, rid, _ := strings.Cut(ref, ":")
			r[name] = map[string]any{"data": map[string]any{"type": t, "id": rid}}
		}
		data["relationships"] = r
	}
	return map[string]any{"data": data}
}

// linkageBody builds a to-one relationship linkage document.
func linkageBody(typ, id string) map[string]any {
	return map[string]any{"data": map[string]any{"type": typ, "id": id}}
}

// Review submission item kinds: the relationship naming the reviewed object.
const (
	kindAppStoreVersion = "appStoreVersion"
	kindIAPVersion      = "inAppPurchaseVersion"
	kindSubVersion      = "subscriptionVersion"
	kindGroupVersion    = "subscriptionGroupVersion"
)

// itemKinds lists the item relationships release tools understand.
var itemKinds = []string{kindAppStoreVersion, kindIAPVersion, kindSubVersion, kindGroupVersion}

// submissionItem is one reviewSubmissionItem.
type submissionItem struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Kind   string `json:"kind"`
	Target string `json:"target_id,omitempty"`
}

// fetchSubmissionItems lists a review submission's items with the object
// each one reviews (include= is needed to see the relationship data).
func fetchSubmissionItems(ctx context.Context, env *tool.Env, submissionID string) ([]submissionItem, error) {
	resp, err := env.ASC.Get(ctx, "/v1/reviewSubmissions/"+submissionID+"/items", url.Values{
		"include":                       {strings.Join(itemKinds, ",")},
		"fields[reviewSubmissionItems]": {"state," + strings.Join(itemKinds, ",")},
		"limit":                         {"50"},
	})
	if err != nil {
		return nil, fmt.Errorf("list review submission items: %w", err)
	}
	out := make([]submissionItem, 0, len(resp.Data))
	for _, r := range resp.Data {
		it := submissionItem{ID: r.ID, State: r.Str("state"), Kind: "other"}
		for _, k := range itemKinds {
			if id := r.RelID(k); id != "" {
				it.Kind, it.Target = k, id
				break
			}
		}
		out = append(out, it)
	}
	return out, nil
}

// submissionView is the compact form of a review submission in results.
type submissionView struct {
	ID    string           `json:"id"`
	State string           `json:"state"`
	Items []submissionItem `json:"items"`
}

// reviewStatusNext points at the tool that follows a submission.
func reviewStatusNext(app tool.App, platform tool.Platform) tool.NextStep {
	return tool.NextStep{Tool: "release_review_status", Args: map[string]any{"app": app.ID, "platform": string(platform)}}
}

// statusNext points at release_status for the version.
func statusNext(app tool.App, platform tool.Platform, version string, desired map[string]any) tool.NextStep {
	args := map[string]any{"app": app.ID, "platform": string(platform)}
	if version != "" {
		args["version"] = version
	}
	if desired != nil {
		args["desired"] = desired
	}
	return tool.NextStep{Tool: "release_status", Args: args}
}

// errNoVersion is returned when a tool needs an existing version.
func errNoVersion(app tool.App, platform tool.Platform, ref tool.VersionRef) error {
	return tool.WithNext(
		fmt.Errorf("%w: %s has no %s App Store version %q; create it with release_prepare", tool.ErrInvalidArgs, app.Name, platform, ref),
		statusNext(app, platform, "", nil),
	)
}

// goneOK reports a 404, which a DELETE treats as already done.
func goneOK(err error) bool {
	var ae *tool.APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}
