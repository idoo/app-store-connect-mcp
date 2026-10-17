package reviews

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/idoo/hamn/internal/applemeta"
	"github.com/idoo/hamn/internal/tool"
)

const maxReplies = 20

type replyIn struct {
	tool.ConfirmFlag
	App     tool.AppRef `json:"app,omitempty" jsonschema:"app the reviews belong to (advances its reviews cursor); default: pinned app"`
	Replies []replyItem `json:"replies" jsonschema:"1-20 replies; a review that already has a response gets it replaced"`
}

type replyItem struct {
	ReviewID string `json:"review_id" jsonschema:"customer review id from reviews_list"`
	Body     string `json:"body" jsonschema:"public reply text"`
}

// replyPreview is one entry of the approval preview: the reply text comes
// first; the customer's review is shown separately as untrusted text.
type replyPreview struct {
	ReviewID string `json:"review_id"`
	Reply    string `json:"reply"`
	Rating   int    `json:"rating"`
	Replaces string `json:"replaces_existing_response,omitempty"`
}

// replyOutcome is the per-review result.
type replyOutcome struct {
	ReviewID   string `json:"review_id"`
	Status     string `json:"status"` // replied | replaced | failed
	ResponseID string `json:"response_id,omitempty"`
	State      string `json:"state,omitempty"`
	Deleted    string `json:"deleted_response_id,omitempty"`
	Error      any    `json:"error,omitempty"`
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "reviews_reply", Toolset: tool.Reviews, Class: tool.Destructive, Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Reply to customer reviews",
		Description: "Publish public replies to up to 20 reviews (review_id from reviews_list). An existing response is " +
			"deleted and replaced. Without confirm:true returns a preview; the human approves every reply text.",
	}, reviewsReply))
}

// validateReplies checks every reply before any request is made.
func validateReplies(replies []replyItem) error {
	if len(replies) == 0 || len(replies) > maxReplies {
		return fmt.Errorf("%w: replies must hold 1-%d items, got %d", tool.ErrInvalidArgs, maxReplies, len(replies))
	}
	var problems []string
	seen := map[string]bool{}
	for i, r := range replies {
		id := strings.TrimSpace(r.ReviewID)
		switch {
		case id == "":
			problems = append(problems, fmt.Sprintf("replies[%d]: review_id is empty", i))
		case seen[id]:
			problems = append(problems, fmt.Sprintf("replies[%d]: duplicate review_id %s", i, id))
		}
		seen[id] = true
		if strings.TrimSpace(r.Body) == "" {
			problems = append(problems, fmt.Sprintf("replies[%d]: body is empty", i))
		} else if err := applemeta.Check(applemeta.FieldReviewResponse, r.Body); err != nil {
			problems = append(problems, fmt.Sprintf("replies[%d] (%s): %v", i, id, err))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", tool.ErrInvalidArgs, strings.Join(problems, "; "))
	}
	return nil
}

func reviewsReply(ctx context.Context, env *tool.Env, in replyIn) (*tool.Result, error) {
	if err := validateReplies(in.Replies); err != nil {
		return nil, err
	}
	res := &tool.Result{}
	app, appErr := env.Resolve.App(ctx, in.App)
	if appErr != nil {
		if in.App != "" {
			return nil, appErr
		}
		res.Warn("no app resolved; the reviews cursor is not advanced (pass app)")
	}

	reviews := make([]reviewOut, len(in.Replies))
	previews := make([]replyPreview, len(in.Replies))
	untrusted := make([]string, len(in.Replies))
	affected := make([]string, len(in.Replies))
	replacing := 0
	for i, r := range in.Replies {
		id := strings.TrimSpace(r.ReviewID)
		rv, err := fetchReview(ctx, env, id)
		if err != nil {
			return nil, err
		}
		reviews[i] = rv
		previews[i] = replyPreview{ReviewID: id, Rating: rv.Rating, Reply: r.Body}
		untrusted[i] = fmt.Sprintf("review %s (%d★): %s", id, rv.Rating,
			excerpt(strings.TrimSpace(string(rv.Title)+" — "+string(rv.Body))))
		affected[i] = "review " + id
		if rv.Response != nil {
			previews[i].Replaces = excerpt(rv.Response.Body)
			affected[i] += " (replaces existing response " + rv.Response.ID + ")"
			replacing++
		}
	}
	summary := fmt.Sprintf("publish %d public App Store review replies", len(in.Replies))
	irreversible := []string{"replies are public on the App Store immediately after Apple processes them"}
	if replacing > 0 {
		summary += fmt.Sprintf(" (%d replace existing responses)", replacing)
		irreversible = append(irreversible, "replaced responses are deleted first and cannot be restored")
	}
	wctx, gate := tool.RequireConfirm(ctx, env, in.Confirm, tool.PlanPreview{
		Summary: summary, Intended: previews, Affected: affected, Irreversible: irreversible, Untrusted: untrusted,
	})
	if gate != nil {
		return gate, nil
	}

	outcomes := make([]replyOutcome, len(in.Replies))
	var newest time.Time
	var newestValue string
	failed := 0
	for i, r := range in.Replies {
		out := postReply(wctx, env, reviews[i], r.Body)
		outcomes[i] = out
		if out.Status == "failed" {
			failed++
			continue
		}
		if reviews[i].created.After(newest) {
			newest, newestValue = reviews[i].created, reviews[i].Created
		}
	}
	data := map[string]any{"replied": len(outcomes) - failed, "failed": failed, "results": outcomes}
	if appErr == nil {
		data["app"] = appRef{ID: app.ID, Name: app.Name, BundleID: app.BundleID}
		if newestValue != "" {
			cur, err := advanceCursor(env, app.ID, newest, newestValue)
			if err != nil {
				res.Warn(err.Error())
			}
			data["cursor"] = cur
		}
	}
	if failed > 0 {
		res.Warn(fmt.Sprintf("%d of %d replies failed; see results", failed, len(outcomes)))
	}
	res.Data = data
	return res, nil
}

// postReply replaces (DELETE then POST) or creates the response of one review.
func postReply(ctx context.Context, env *tool.Env, rv reviewOut, body string) replyOutcome {
	out := replyOutcome{ReviewID: rv.ID, Status: "replied"}
	if rv.Response != nil {
		_, err := env.ASC.Do(ctx, tool.Request{
			Method: http.MethodDelete, Path: "/v1/customerReviewResponses/" + rv.Response.ID, Write: true,
		})
		var apiErr *tool.APIError
		if err != nil && (!errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound) {
			out.Status, out.Error = "failed", tool.BodyFor(fmt.Errorf("delete existing response %s: %w", rv.Response.ID, err))
			return out
		}
		out.Status, out.Deleted = "replaced", rv.Response.ID
	}
	resp, err := env.ASC.Do(ctx, tool.Request{
		Method: http.MethodPost, Path: "/v1/customerReviewResponses", Write: true,
		Body: map[string]any{"data": map[string]any{
			"type":       responseType,
			"attributes": map[string]any{"responseBody": body},
			"relationships": map[string]any{
				"review": map[string]any{"data": map[string]any{"type": "customerReviews", "id": rv.ID}},
			},
		}},
	})
	if err != nil {
		msg := "post reply"
		if out.Deleted != "" {
			msg = "existing response was deleted but the new reply was not posted"
		}
		out.Status, out.Error = "failed", tool.BodyFor(fmt.Errorf("%s: %w", msg, err))
		return out
	}
	if r, ok := resp.First(); ok {
		out.ResponseID, out.State = r.ID, r.Str("state")
	}
	return out
}
