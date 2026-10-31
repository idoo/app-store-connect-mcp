package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "release_review_reply", Toolset: tool.Web, Class: tool.Destructive,
		Confirm: tool.ConfirmByPlan, RequireHuman: true,
		Title: "Reply to App Review (Resolution Center)",
		Description: "Send the human's own reply text to App Review in the Resolution Center (web session; default the newest " +
			"rejection thread). Never copy reviewer text into it. First call previews with a plan_id; call again with it. No attachments.",
		Enums: map[string][]any{"platform": platformEnum},
	}, releaseReviewReply))
}

type reviewReplyIn struct {
	App        tool.AppRef   `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform   tool.Platform `json:"platform,omitempty"`
	Submission string        `json:"submission,omitempty" jsonschema:"review submission id; default the newest submitted one"`
	Thread     string        `json:"thread,omitempty" jsonschema:"Resolution Center thread id; default the newest rejection thread"`
	Text       string        `json:"text" jsonschema:"the reply, plain text written or approved by the human, at most 4000 characters"`
	tool.ConfirmPlan
}

// maxReplyRunes bounds a reply; Apple's web form takes about this much.
const maxReplyRunes = 4000

// Reply steps, in execution order.
const (
	replyCreateDraft = "create_draft"
	replyUpdateDraft = "update_draft"
	replySend        = "send"
)

type replyStep struct {
	Op      string          `json:"op"`
	Outcome string          `json:"outcome,omitempty"`
	ID      string          `json:"id,omitempty"`
	Error   *tool.ErrorBody `json:"error,omitempty"`
}

type replyThread struct {
	ID      string `json:"id"`
	Type    string `json:"type,omitempty"`
	Created string `json:"created,omitempty"`
}

type sentMessage struct {
	ID     string `json:"id"`
	Date   string `json:"date,omitempty"`
	Landed bool   `json:"landed"`
}

type reviewReplyOut struct {
	Status     string        `json:"status"`
	App        *appRef       `json:"app,omitempty"`
	Submission submissionRef `json:"submission"`
	Thread     replyThread   `json:"thread"`
	Guidelines []string      `json:"guidelines,omitempty"`
	Text       string        `json:"text"`
	Steps      []replyStep   `json:"steps"`
	Message    *sentMessage  `json:"message,omitempty"`
}

type replyPartial struct {
	tool.ErrorBody
	Partial reviewReplyOut `json:"partial"`
}

var errReviewNotOpen = &tool.Error{
	Code:    "review_not_open",
	Message: "App Review is not waiting for a reply on this submission",
	Hint:    "replies go to a submission in UNRESOLVED_ISSUES with an open thread; check release_review_status and release_review_messages",
}

// replyText validates and normalises the human's reply: trimmed, CR/LF to
// LF, no control characters except newlines and tabs, at most
// [maxReplyRunes]. Apple's web form sends the textarea value as is (it
// renders developer messages as plain text), so no HTML encoding.
func replyText(s string) (string, error) {
	s = strings.TrimSpace(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s))
	switch {
	case s == "":
		return "", tool.InvalidArgs("text is required: the reply the human wrote or approved")
	case !utf8.ValidString(s):
		return "", tool.InvalidArgs("text is not valid UTF-8")
	case utf8.RuneCountInString(s) > maxReplyRunes:
		return "", tool.InvalidArgs("text has %d characters; at most %d", utf8.RuneCountInString(s), maxReplyRunes)
	}
	if i := strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }); i >= 0 {
		return "", tool.InvalidArgs("text contains a control character; use plain text")
	}
	return s, nil
}

// replyTarget is everything a reply depends on, read fresh on every call.
type replyTarget struct {
	reviewTarget
	thread     tool.Resource
	read       reviewThread
	guidelines []string
	draftID    string
	draftText  string
}

// fingerprint is the remote state the plan depends on.
func (t replyTarget) fingerprint() (string, error) {
	var last string
	if n := len(t.read.Messages); n > 0 {
		last = t.read.Messages[n-1].ID
	}
	canAdd, _ := t.thread.Bool("canDeveloperAddNote")
	return tool.Fingerprint(map[string]any{
		"submission": t.Submission.ID, "state": t.Submission.State, "thread": t.thread.ID,
		"thread_state": t.thread.Str("state"), "can_add": canAdd, "messages": len(t.read.Messages), "last": last,
		"draft": t.draftID, "draft_text": t.draftText,
	})
}

// loadReplyTarget resolves the submission and thread and checks App Review
// is waiting for a reply there.
func loadReplyTarget(ctx context.Context, env *tool.Env, in reviewReplyIn) (replyTarget, error) {
	var t replyTarget
	var err error
	if t.reviewTarget, err = resolveReviewTarget(ctx, env, in.App, in.Platform, in.Submission); err != nil {
		return t, err
	}
	if err := fillSubmission(ctx, env, &t.Submission); err != nil {
		return t, err
	}
	threads, err := listReviewThreads(ctx, env, t.Submission.ID)
	if err != nil {
		return t, err
	}
	if t.thread, err = pickReplyThread(threads, strings.TrimSpace(in.Thread), t.Submission.ID); err != nil {
		return t, err
	}
	if t.Submission.State != submissionOpen {
		return t, fmt.Errorf("%w: submission %s is %s", errReviewNotOpen, t.Submission.ID, cmp.Or(t.Submission.State, "in an unknown state"))
	}
	if canAdd, ok := t.thread.Bool("canDeveloperAddNote"); ok && !canAdd {
		return t, fmt.Errorf("%w: thread %s does not take developer replies", errReviewNotOpen, t.thread.ID)
	}
	if st := strings.ToUpper(t.thread.Str("state")); st == "CLOSED" || st == "RESOLVED" {
		return t, fmt.Errorf("%w: thread %s is %s", errReviewNotOpen, t.thread.ID, st)
	}
	if t.read, err = readReviewThread(ctx, env, t.thread); err != nil {
		return t, err
	}
	set := map[string]bool{}
	threadGuidelines(t.read, set)
	t.guidelines = sortedKeys(set)
	resp, err := env.Web.Get(ctx, "/v1/resolutionCenterThreads/"+url.PathEscape(t.thread.ID)+"/resolutionCenterDraftMessage", nil)
	switch {
	case tool.APIStatus(err) == http.StatusNotFound:
	case err != nil:
		return t, fmt.Errorf("read the unsent draft of thread %s: %w", t.thread.ID, err)
	default:
		if d, ok := resp.First(); ok {
			t.draftID, t.draftText = d.ID, string(d.Text("messageBody"))
		}
	}
	return t, nil
}

// pickReplyThread is the thread named by want (it must belong to the
// submission), else the newest rejection thread, else the newest thread.
func pickReplyThread(threads []tool.Resource, want, sub string) (tool.Resource, error) {
	if want != "" {
		for _, th := range threads {
			if th.ID == want {
				return th, nil
			}
		}
		return tool.Resource{}, &tool.ErrNotFound{
			Kind: "Resolution Center thread", Ref: want, Hint: "it is not a thread of review submission " + sub,
			Next: []tool.NextStep{{Tool: "release_review_messages", Args: map[string]any{"submission": sub}}},
		}
	}
	if len(threads) == 0 {
		return tool.Resource{}, &tool.ErrNotFound{Kind: "Resolution Center thread", Ref: sub, Hint: "App Review has not written about this submission: there is nothing to reply to"}
	}
	sorted := slices.Clone(threads)
	slices.SortStableFunc(sorted, func(a, b tool.Resource) int {
		ra, rb := strings.HasPrefix(a.Str("threadType"), "REJECTION"), strings.HasPrefix(b.Str("threadType"), "REJECTION")
		if ra != rb {
			if ra {
				return -1
			}
			return 1
		}
		return cmp.Compare(b.Str("createdDate"), a.Str("createdDate"))
	})
	return sorted[0], nil
}

func releaseReviewReply(ctx context.Context, env *tool.Env, in reviewReplyIn) (*tool.Result, error) {
	text, err := replyText(in.Text)
	if err != nil {
		return nil, err
	}
	in.Text = text
	t, err := loadReplyTarget(ctx, env, in)
	if err != nil {
		return nil, err
	}
	fp, err := t.fingerprint()
	if err != nil {
		return nil, err
	}
	out := reviewReplyOut{
		App: t.App, Submission: t.Submission, Thread: replyThread{ID: t.thread.ID, Type: t.thread.Str("threadType"), Created: t.thread.Str("createdDate")},
		Guidelines: t.guidelines, Text: text,
	}
	draftOp := replyCreateDraft
	if t.draftID != "" {
		draftOp = replyUpdateDraft
	}
	out.Steps = []replyStep{{Op: draftOp, ID: t.draftID}, {Op: replySend}}
	appName := t.Submission.ID
	if t.App != nil {
		appName = string(t.App.Name) + " (" + t.App.ID + ")"
	}
	p := tool.PlanPreview{
		Summary: fmt.Sprintf("send a Resolution Center reply to App Review for %s, submission %s", appName, t.Submission.ID),
		Current: map[string]any{
			"submission": t.Submission, "thread": out.Thread, "guidelines": t.guidelines, "messages": len(t.read.Messages),
		},
		Intended: map[string]any{"text": text, "steps": []string{draftOp, replySend}},
		Affected: []string{
			"resolutionCenterThreads/" + t.thread.ID,
			"POST /iris/v1/resolutionCenterDraftMessages (or PATCH the unsent draft)",
			"POST /iris/v1/resolutionCenterMessages",
		},
		Irreversible: []string{"App Review receives the reply; a sent message cannot be edited or deleted"},
	}
	if t.draftID != "" {
		p.Warnings = append(p.Warnings, "replaces the unsent draft saved in App Store Connect with this text")
		p.Untrusted = append(p.Untrusted, "unsent draft: "+t.draftText)
	}
	wctx, res := tool.Preview(ctx, env, in.PlanID, reviewReplyIn{App: in.App, Platform: in.Platform, Submission: in.Submission, Thread: in.Thread, Text: text}, fp, p)
	if res != nil {
		return res, nil
	}

	fail := func(i int, err error) (*tool.Result, error) {
		out.Steps[i].Outcome = outcomeOf(err)
		b := tool.BodyFor(err)
		out.Steps[i].Error = &b
		for j := i + 1; j < len(out.Steps); j++ {
			out.Steps[j].Outcome = outcomeNotAttempted
		}
		out.Status = "partial"
		next := append([]tool.NextStep(nil), b.Next...)
		next = append(next, tool.NextStep{Tool: "release_review_messages", Args: map[string]any{"submission": t.Submission.ID}})
		return &tool.Result{Data: replyPartial{ErrorBody: b, Partial: out}, IsError: true, Next: next}, nil
	}
	draftID, err := writeDraft(wctx, env, t.thread.ID, t.draftID, text)
	if err != nil {
		return fail(0, err)
	}
	out.Steps[0].Outcome, out.Steps[0].ID = outcomeDone, draftID
	resp, err := env.Web.Do(wctx, tool.Request{
		Method: http.MethodPost, Path: "/v1/resolutionCenterMessages", Write: true,
		Body: map[string]any{"data": map[string]any{
			"type":          "resolutionCenterMessages",
			"relationships": map[string]any{"createFromDraftMessage": tool.RelData("resolutionCenterDraftMessages", draftID)},
		}},
	})
	if err != nil {
		return fail(1, fmt.Errorf("send draft %s: %w", draftID, err))
	}
	m, ok := resp.First()
	if !ok || m.ID == "" {
		return fail(1, fmt.Errorf("send draft %s: %w: Apple returned no message", draftID, tool.ErrOutcomeUnknown))
	}
	out.Steps[1].Outcome, out.Steps[1].ID = outcomeDone, m.ID
	out.Message = &sentMessage{ID: m.ID, Date: m.Str("createdDate")}
	out.Status = "sent"
	var warn string
	if after, err := readReviewThread(ctx, env, t.thread); err != nil {
		warn = "sent, but re-reading the thread failed: " + tool.BodyFor(err).Message
	} else if out.Message.Landed = slices.ContainsFunc(after.Messages, func(x reviewMessage) bool { return x.ID == m.ID }); !out.Message.Landed {
		warn = "sent, but the message is not in the thread yet; check release_review_messages"
	}
	r := tool.OK(out)
	if warn != "" {
		r.Warn(warn)
	}
	return r.Then(tool.NextStep{Tool: "release_review_messages", Args: map[string]any{"submission": t.Submission.ID}}), nil
}

// writeDraft puts text into the thread's unsent draft, the way App Store
// Connect's web form does: PATCH the existing draft, else POST a new one.
// It returns the draft id.
func writeDraft(ctx context.Context, env *tool.Env, threadID, draftID, text string) (string, error) {
	req := tool.Request{
		Method: http.MethodPost, Path: "/v1/resolutionCenterDraftMessages", Write: true,
		Body: map[string]any{"data": map[string]any{
			"type":          "resolutionCenterDraftMessages",
			"attributes":    map[string]any{"messageBody": text},
			"relationships": map[string]any{"resolutionCenterThread": tool.RelData("resolutionCenterThreads", threadID)},
		}},
	}
	if draftID != "" {
		req = tool.Request{
			Method: http.MethodPatch, Path: "/v1/resolutionCenterDraftMessages/" + url.PathEscape(draftID), Write: true,
			Body: map[string]any{"data": map[string]any{
				"type": "resolutionCenterDraftMessages", "id": draftID, "attributes": map[string]any{"messageBody": text},
			}},
		}
	}
	resp, err := env.Web.Do(ctx, req)
	if err != nil {
		return "", fmt.Errorf("write the reply draft: %w", err)
	}
	if d, ok := resp.First(); ok && d.ID != "" {
		return d.ID, nil
	}
	if draftID != "" {
		return draftID, nil
	}
	return "", errors.New("write the reply draft: Apple returned no draft id")
}
