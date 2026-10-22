package release

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

type reviewStatusIn struct {
	App            tool.AppRef   `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform       tool.Platform `json:"platform,omitempty"`
	Limit          int           `json:"limit,omitempty" jsonschema:"history, 1-20, default 5"`
	Wait           bool          `json:"wait,omitempty" jsonschema:"poll until review ends"`
	TimeoutSeconds int           `json:"timeout_seconds,omitempty" jsonschema:"default 120, max 900"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "release_review_status", Toolset: tool.Release, Class: tool.Read, Idempotent: true,
		Title: "App Review submission status",
		Description: "Current and recent App Review submissions and item states. Reviewer messages aren't in the API: " +
			"see the Resolution Center.",
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
	App              appOut                `json:"app"`
	Platform         tool.Platform         `json:"platform"`
	Current          *reviewSubmissionOut  `json:"current,omitempty"`
	History          []reviewSubmissionOut `json:"history"`
	Rejection        *rejectionOut         `json:"rejection,omitempty"`
	ResolutionCenter string                `json:"resolution_center"`
	Polls            int                   `json:"polls,omitempty"`
	WaitedSeconds    int                   `json:"waited_seconds,omitempty"`
}

// Review wait bounds.
const (
	reviewWaitDefault  = 120 * time.Second
	reviewWaitMax      = 900 * time.Second
	reviewWaitInterval = 30 * time.Second
)

// inReview reports a submission App Review has not finished.
func inReview(cur *reviewSubmissionOut) bool {
	return cur != nil && (cur.State == "WAITING_FOR_REVIEW" || cur.State == "IN_REVIEW")
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
	out, err := loadReviewStatus(ctx, env, app, platform, limit)
	if err != nil {
		return nil, err
	}
	if in.Wait && inReview(out.Current) {
		timeout := reviewWaitDefault
		if in.TimeoutSeconds > 0 {
			timeout = min(time.Duration(in.TimeoutSeconds)*time.Second, reviewWaitMax)
		}
		if !env.HasProgress() {
			timeout = min(timeout, tool.NoProgressWaitCap)
		}
		start := env.Clock.Now()
		for polls := 1; ; polls++ {
			waited := env.Clock.Now().Sub(start)
			out.Polls, out.WaitedSeconds = polls, int(waited/time.Second)
			state := "none"
			if out.Current != nil {
				state = out.Current.State
			}
			env.Progress(waited.Seconds(), timeout.Seconds(), "review submission: "+state)
			if !inReview(out.Current) {
				break
			}
			remaining := timeout - waited
			if remaining <= 0 {
				args := map[string]any{"app": app.ID, "platform": string(platform), "wait": true, "timeout_seconds": int(timeout / time.Second)}
				return tool.OK(map[string]any{
					"status":      "still_in_review",
					"resume_hint": "call release_review_status again with wait:true; review often takes 24-48 h, so for longer waits use webhooks_upsert",
					"result":      out,
				}).Then(tool.NextStep{Tool: "release_review_status", Args: args}), nil
			}
			if err := env.Clock.Sleep(ctx, min(reviewWaitInterval, remaining)); err != nil {
				return nil, err
			}
			if out, err = loadReviewStatus(ctx, env, app, platform, limit); err != nil {
				return nil, err
			}
		}
	}
	if out.Current == nil {
		return tool.OK(out).Then(statusNext(app, platform, "", nil)), nil
	}
	return tool.OK(out).Then(reviewNext(app, platform, out.Current)...), nil
}

// loadReviewStatus reads the newest limit submissions; the newest is
// Current, with its items.
func loadReviewStatus(ctx context.Context, env *tool.Env, app tool.App, platform tool.Platform, limit int) (reviewStatusOut, error) {
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
		return reviewStatusOut{}, fmt.Errorf("list review submissions: %w", err)
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
	out := reviewStatusOut{App: newAppOut(app), Platform: platform, History: []reviewSubmissionOut{}, ResolutionCenter: rc}
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
				return reviewStatusOut{}, err
			}
			v.ItemCount = len(v.Items)
			cur := v
			out.Current = &cur
			continue
		}
		out.History = append(out.History, v)
	}
	if out.Current != nil {
		out.Rejection = rejection(out.Current, rc)
	}
	return out, nil
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
