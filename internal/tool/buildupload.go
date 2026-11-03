package tool

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
)

// BuildUpload is one delivery of a build to App Store Connect (Xcode, Xcode
// Cloud, Transporter or builds_upload; /v1/apps/{id}/buildUploads) and
// Apple's verdict on it. A FAILED upload never becomes a build, so its
// errors are the only place the reason shows.
type BuildUpload struct {
	ID          string        `json:"id"`
	Platform    Platform      `json:"platform"`
	BuildNumber string        `json:"build_number"`
	Version     string        `json:"version,omitempty"`
	State       string        `json:"state"`
	Created     string        `json:"created,omitempty"`
	Uploaded    string        `json:"uploaded,omitempty"`
	Errors      []UploadIssue `json:"errors,omitempty"`
	Warnings    []UploadIssue `json:"warnings,omitempty"`
}

// UploadIssue is one error or warning of a build upload; Description is
// Apple's text about the uploaded bundle, which quotes the developer's
// strings, so it stays marked untrusted.
type UploadIssue struct {
	Code        string    `json:"code,omitempty"`
	Description Untrusted `json:"description,omitempty"`
}

// BuildUploadFields is the sparse fieldset ListBuildUploads reads.
const BuildUploadFields = "cfBundleShortVersionString,cfBundleVersion,platform,state,createdDate,uploadedDate"

// ListBuildUploads lists an app's build uploads matching q (filters, sort,
// limit; fields are set here), newest created first.
func ListBuildUploads(ctx context.Context, env *Env, appID string, q url.Values) ([]BuildUpload, error) {
	q = maps.Clone(q) // Set replaces whole slices, so a shallow copy keeps the caller's values
	if q == nil {
		q = url.Values{}
	}
	q.Set("fields[buildUploads]", BuildUploadFields)
	resp, err := env.ASC.Get(ctx, "/v1/apps/"+url.PathEscape(appID)+"/buildUploads", q)
	if err != nil {
		return nil, fmt.Errorf("list build uploads: %w", err)
	}
	out := make([]BuildUpload, 0, len(resp.Data))
	for _, r := range resp.Data {
		out = append(out, ParseBuildUpload(r))
	}
	slices.SortStableFunc(out, func(a, b BuildUpload) int { return cmp.Compare(b.Created, a.Created) })
	return out, nil
}

// ParseBuildUpload flattens a buildUploads resource.
func ParseBuildUpload(r Resource) BuildUpload {
	u := BuildUpload{
		ID: r.ID, Platform: Platform(r.Str("platform")), BuildNumber: r.Str("cfBundleVersion"),
		Version: r.Str("cfBundleShortVersionString"), Created: r.Str("createdDate"), Uploaded: r.Str("uploadedDate"),
	}
	st, _ := r.Attributes["state"].(map[string]any)
	u.State, _ = st["state"].(string)
	u.Errors, u.Warnings = uploadIssues(st["errors"]), uploadIssues(st["warnings"])
	return u
}

// uploadIssues decodes a BuildUpload state's errors or warnings. The client
// may already have wrapped descriptions as [Untrusted].
func uploadIssues(v any) []UploadIssue {
	list, _ := v.([]any)
	var out []UploadIssue
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		var is UploadIssue
		switch c := m["code"].(type) {
		case string:
			is.Code = c
		case float64:
			is.Code = strconv.FormatFloat(c, 'f', -1, 64)
		}
		switch d := m["description"].(type) {
		case string:
			is.Description = Untrusted(d)
		case Untrusted:
			is.Description = d
		}
		out = append(out, is)
	}
	return out
}
