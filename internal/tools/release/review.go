package release

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"

	"github.com/idoo/hamn/internal/tool"
)

type reviewStatusIn struct {
	App      tool.AppRef   `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform tool.Platform `json:"platform,omitempty"`
	Limit    int           `json:"limit,omitempty" jsonschema:"past submissions, 1-20, default 5"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "release_review_status", Toolset: tool.Release, Class: tool.Read, Idempotent: true,
		Title: "App Review submission status",
		Description: "Current and recent App Review submissions with items and states. Reviewer messages aren't in the " +
			"API: read the Resolution Center link, fix, then release_submit.",
		Enums: map[string][]any{"platform": platformEnum},
	}, releaseReviewStatus))
}

// reviewSubmissionOut is one review submission in release_review_status.
type reviewSubmissionOut struct {
	ID            string           `json:"id"`
	State         string           `json:"state"`
	SubmittedDate string           `json:"submitted_date,omitempty"`
	Version       *versionView     `json:"version,omitempty"`
	ItemCount     int              `json:"item_count"`
	ItemStates    map[string]int   `json:"item_states,omitempty"`
	Items         []submissionItem `json:"items,omitempty"`
}

// rejectionOut explains a rejection the API can only flag, not describe.
type rejectionOut struct {
	RejectedItems    []string `json:"rejected_items,omitempty"`
	Note             string   `json:"note"`
	ResolutionCenter string   `json:"resolution_center"`
}

type reviewStatusOut struct {
	App              tool.App              `json:"app"`
	Platform         tool.Platform         `json:"platform"`
	Current          *reviewSubmissionOut  `json:"current,omitempty"`
	History          []reviewSubmissionOut `json:"history"`
	Rejection        *rejectionOut         `json:"rejection,omitempty"`
	ResolutionCenter string                `json:"resolution_center"`
}

// rejectedVersionStates are appVersionState values after an App Review rejection.
var rejectedVersionStates = []string{"REJECTED", "METADATA_REJECTED", "INVALID_BINARY"}

func releaseReviewStatus(ctx context.Context, env *tool.Env, in reviewStatusIn) (*tool.Result, error) {
	app, err := env.Resolve.App(ctx, in.App)
	if err != nil {
		return nil, err
	}
	platform := app.Platform(in.Platform)
	limit := in.Limit
	if limit <= 0 {
		limit = 5
	}
	limit = min(limit, 20)
	resp, err := env.ASC.Get(ctx, "/v1/reviewSubmissions", url.Values{
		"filter[app]":                   {app.ID},
		"filter[platform]":              {string(platform)},
		"include":                       {"appStoreVersionForReview,items"},
		"fields[reviewSubmissions]":     {"platform,state,submittedDate,appStoreVersionForReview,items"},
		"fields[appStoreVersions]":      {"versionString,appVersionState,releaseType"},
		"fields[reviewSubmissionItems]": {"state"},
		"limit[items]":                  {"50"},
		"limit":                         {"50"},
	})
	if err != nil {
		return nil, fmt.Errorf("list review submissions: %w", err)
	}
	subs := slices.Clone(resp.Data)
	// Unsent drafts have no submittedDate and are the newest; then newest first.
	slices.SortStableFunc(subs, func(a, b tool.Resource) int {
		da, db := a.Str("submittedDate"), b.Str("submittedDate")
		if (da == "") != (db == "") {
			if da == "" {
				return -1
			}
			return 1
		}
		return cmp.Compare(db, da)
	})
	if len(subs) > limit {
		subs = subs[:limit]
	}
	rc := ascURL(app.ID, "distribution/reviewsubmissions")
	out := reviewStatusOut{App: app, Platform: platform, History: []reviewSubmissionOut{}, ResolutionCenter: rc}
	byKey := map[string]tool.Resource{}
	for _, r := range resp.Included {
		byKey[r.Type+":"+r.ID] = r
	}
	for i, s := range subs {
		v := reviewSubmissionOut{ID: s.ID, State: s.Str("state"), SubmittedDate: s.Str("submittedDate")}
		if vr, ok := byKey["appStoreVersions:"+s.RelID("appStoreVersionForReview")]; ok {
			vv := viewVersion(vr)
			v.Version = &vv
		}
		ids := s.RelIDs("items")
		v.ItemCount = len(ids)
		for _, id := range ids {
			if it, ok := byKey["reviewSubmissionItems:"+id]; ok {
				if v.ItemStates == nil {
					v.ItemStates = map[string]int{}
				}
				v.ItemStates[it.Str("state")]++
			}
		}
		if i == 0 {
			if v.Items, err = fetchSubmissionItems(ctx, env, s.ID); err != nil {
				return nil, err
			}
			v.ItemCount = len(v.Items)
			cur := v
			out.Current = &cur
			continue
		}
		out.History = append(out.History, v)
	}
	if out.Current == nil {
		return tool.OK(out).Then(statusNext(app, platform, "", nil)), nil
	}
	out.Rejection = rejection(out.Current, rc)
	return tool.OK(out).Then(reviewNext(app, platform, out.Current)...), nil
}

// rejection reports a rejected current submission (nil when not rejected).
func rejection(cur *reviewSubmissionOut, rc string) *rejectionOut {
	var items []string
	for _, it := range cur.Items {
		if it.State == "REJECTED" {
			items = append(items, it.Kind+" "+it.Target)
		}
	}
	versionRejected := cur.Version != nil && slices.Contains(rejectedVersionStates, cur.Version.State)
	if cur.State != "UNRESOLVED_ISSUES" && len(items) == 0 && !versionRejected {
		return nil
	}
	return &rejectionOut{
		RejectedItems:    items,
		Note:             "the App Store Connect API does not expose the reviewer's message or guideline; read it in the Resolution Center, fix, then resubmit with release_submit",
		ResolutionCenter: rc,
	}
}

// reviewNext suggests the follow-up for the current submission.
func reviewNext(app tool.App, platform tool.Platform, cur *reviewSubmissionOut) []tool.NextStep {
	version := ""
	if cur.Version != nil {
		version = cur.Version.String
		if cur.Version.State == "PENDING_DEVELOPER_RELEASE" {
			return []tool.NextStep{{Tool: "release_publish", Args: versionArgs(app, platform, version)}}
		}
	}
	base := map[string]any{"app": app.ID, "platform": string(platform)}
	switch cur.State {
	case "WAITING_FOR_REVIEW", "IN_REVIEW":
		return []tool.NextStep{{Tool: "release_cancel", Args: base}}
	case "UNRESOLVED_ISSUES":
		return []tool.NextStep{statusNext(app, platform, version, nil), {Tool: "release_submit", Args: base}}
	case "READY_FOR_REVIEW":
		return []tool.NextStep{{Tool: "release_submit", Args: base}}
	}
	return []tool.NextStep{statusNext(app, platform, "", nil)}
}
