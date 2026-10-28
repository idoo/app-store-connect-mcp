package web

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "release_review_messages", Toolset: tool.Web, Class: tool.Read, Idempotent: true,
		Title: "App Review messages (Resolution Center)",
		Description: "The reviewer's Resolution Center messages for a review submission (web session; default the newest " +
			"submitted one of the app/platform): date, sender, text, rejected guidelines with reasons. Read-only.",
		Enums: map[string][]any{"platform": platformEnum},
	}, releaseReviewMessages))
}

type reviewMessagesIn struct {
	App        tool.AppRef   `json:"app,omitempty" jsonschema:"name, bundle id, SKU or id; default pinned"`
	Platform   tool.Platform `json:"platform,omitempty"`
	Submission string        `json:"submission,omitempty" jsonschema:"review submission id; default the newest submitted one"`
}

// submissionRef is the review submission whose thread was read.
type submissionRef struct {
	ID            string `json:"id"`
	State         string `json:"state,omitempty"`
	Platform      string `json:"platform,omitempty"`
	SubmittedDate string `json:"submitted_date,omitempty"`
}

// rejectionReason is one rejected guideline of a reviewer message.
type rejectionReason struct {
	Guideline   string         `json:"guideline,omitempty"`
	Code        string         `json:"code,omitempty"`
	Description tool.Untrusted `json:"description,omitempty"`
}

type reviewMessage struct {
	ID         string            `json:"id"`
	Date       string            `json:"date"`
	From       string            `json:"from,omitempty"`
	FromName   tool.Untrusted    `json:"from_name,omitempty"`
	Text       tool.Untrusted    `json:"text"`
	Rejections []rejectionReason `json:"rejections,omitempty"`
}

type reviewThread struct {
	ID       string          `json:"id"`
	Type     string          `json:"type,omitempty"`
	State    string          `json:"state,omitempty"`
	Created  string          `json:"created,omitempty"`
	Messages []reviewMessage `json:"messages"`
}

type reviewMessagesOut struct {
	App              *appRef        `json:"app,omitempty"`
	Submission       submissionRef  `json:"submission"`
	Threads          []reviewThread `json:"threads"`
	Guidelines       []string       `json:"guidelines,omitempty"`
	Note             string         `json:"note,omitempty"`
	ResolutionCenter string         `json:"resolution_center,omitempty"`
}

// Resolution Center read bounds.
const (
	maxReviewThreads  = 20
	maxReviewMessages = 200
)

// reviewTarget is the app (when named or needed) and review submission a
// Resolution Center tool works on.
type reviewTarget struct {
	App              *appRef
	Submission       submissionRef
	ResolutionCenter string
}

// resolveReviewTarget resolves app and submission: the given submission id,
// else the newest submitted one of the app on platform. The app is resolved
// whenever it is named or the submission must be found.
func resolveReviewTarget(ctx context.Context, env *tool.Env, appRefIn tool.AppRef, platform tool.Platform, submission string) (reviewTarget, error) {
	var t reviewTarget
	sub := strings.TrimSpace(submission)
	if sub == "" || appRefIn != "" {
		app, err := env.Resolve.App(ctx, appRefIn)
		if err != nil {
			return t, err
		}
		ref := newAppRef(app)
		t.App = &ref
		t.ResolutionCenter = "https://appstoreconnect.apple.com/apps/" + url.PathEscape(app.ID) + "/distribution/reviewsubmissions"
		if sub == "" {
			s, err := newestSubmission(ctx, env, app, app.Platform(platform))
			if err != nil {
				return t, err
			}
			if s == nil {
				return t, &tool.ErrNotFound{
					Kind: "review submission", Ref: string(app.Platform(platform)),
					Hint: "this app has no submitted review submission on that platform",
					Next: []tool.NextStep{{Tool: "release_review_status", Args: map[string]any{"app": app.ID}}},
				}
			}
			t.Submission = *s
		}
	}
	if t.Submission.ID == "" {
		t.Submission.ID = sub
	}
	return t, nil
}

// listReviewThreads lists the Resolution Center threads of a submission.
func listReviewThreads(ctx context.Context, env *tool.Env, sub string) ([]tool.Resource, error) {
	threads, err := env.Web.List(ctx, "/v1/resolutionCenterThreads", tool.ListOptions{
		Query: url.Values{"filter[reviewSubmission]": {sub}}, Limit: -1, MaxItems: maxReviewThreads,
	})
	if err != nil {
		return nil, fmt.Errorf("read Resolution Center threads of submission %s: %w", sub, err)
	}
	return threads.Data, nil
}

// readReviewThread reads a thread's messages, oldest first.
func readReviewThread(ctx context.Context, env *tool.Env, th tool.Resource) (reviewThread, error) {
	t := reviewThread{ID: th.ID, Type: th.Str("threadType"), State: th.Str("state"), Created: th.Str("createdDate")}
	msgs, err := env.Web.List(ctx, "/v1/resolutionCenterThreads/"+url.PathEscape(th.ID)+"/resolutionCenterMessages", tool.ListOptions{
		Include: []string{"rejections", "fromActor"}, Limit: -1, MaxItems: maxReviewMessages,
	})
	if err != nil {
		return t, fmt.Errorf("read Resolution Center messages of thread %s: %w", th.ID, err)
	}
	t.Messages = decodeReviewMessages(msgs)
	return t, nil
}

// threadGuidelines adds the rejected guidelines of t to set: the reason code
// when it refines the section (2.3 → 2.3.7), else the section.
func threadGuidelines(t reviewThread, set map[string]bool) {
	for _, m := range t.Messages {
		for _, r := range m.Rejections {
			switch {
			case r.Guideline != "" && strings.HasPrefix(r.Code, r.Guideline+"."):
				set[r.Code] = true
			case r.Guideline != "":
				set[r.Guideline] = true
			}
		}
	}
}

// sortedKeys returns the keys of set in order.
func sortedKeys(set map[string]bool) []string {
	var out []string
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// submissionOpen is the review submission state in which App Review waits
// for the developer, so the Resolution Center takes replies.
const submissionOpen = "UNRESOLVED_ISSUES"

// fillSubmission reads the state, platform and date of a submission known
// only by id (public API); a submission found by newestSubmission has them.
func fillSubmission(ctx context.Context, env *tool.Env, s *submissionRef) error {
	if s.State != "" {
		return nil
	}
	resp, err := env.ASC.Get(ctx, "/v1/reviewSubmissions/"+url.PathEscape(s.ID),
		url.Values{"fields[reviewSubmissions]": {"platform,state,submittedDate"}})
	if err != nil {
		return fmt.Errorf("read review submission %s: %w", s.ID, err)
	}
	if r, ok := resp.First(); ok {
		s.State, s.Platform, s.SubmittedDate = r.Str("state"), r.Str("platform"), r.Str("submittedDate")
	}
	return nil
}

func releaseReviewMessages(ctx context.Context, env *tool.Env, in reviewMessagesIn) (*tool.Result, error) {
	t, err := resolveReviewTarget(ctx, env, in.App, in.Platform, in.Submission)
	if err != nil {
		return nil, err
	}
	// A named app with an explicit submission: read its state, so the reply
	// step is offered only when App Review waits for one.
	if t.App != nil {
		if err := fillSubmission(ctx, env, &t.Submission); err != nil {
			return nil, err
		}
	}
	out := reviewMessagesOut{App: t.App, Submission: t.Submission, ResolutionCenter: t.ResolutionCenter, Threads: []reviewThread{}}
	threads, err := listReviewThreads(ctx, env, t.Submission.ID)
	if err != nil {
		return nil, err
	}
	guidelines := map[string]bool{}
	for _, th := range threads {
		rt, err := readReviewThread(ctx, env, th)
		if err != nil {
			return nil, err
		}
		threadGuidelines(rt, guidelines)
		out.Threads = append(out.Threads, rt)
	}
	out.Guidelines = sortedKeys(guidelines)
	if len(out.Threads) == 0 {
		out.Note = "no Resolution Center thread for this submission: App Review has not written about it"
	}
	res := tool.OK(out)
	if out.App != nil {
		res.Then(tool.NextStep{Tool: "release_status", Args: map[string]any{"app": out.App.ID}})
		if out.Submission.State == submissionOpen && len(out.Threads) > 0 {
			res.Then(tool.NextStep{Tool: "release_review_reply", Args: map[string]any{"app": out.App.ID, "submission": out.Submission.ID}})
		}
	}
	return res, nil
}

// newestSubmission is the app's newest submitted review submission on
// platform (nil when none was ever submitted).
func newestSubmission(ctx context.Context, env *tool.Env, app tool.App, platform tool.Platform) (*submissionRef, error) {
	resp, err := env.ASC.Get(ctx, "/v1/reviewSubmissions", url.Values{
		"filter[app]":               {app.ID},
		"filter[platform]":          {string(platform)},
		"fields[reviewSubmissions]": {"platform,state,submittedDate"},
		"limit":                     {"50"},
	})
	if err != nil {
		return nil, fmt.Errorf("list review submissions: %w", err)
	}
	var best *submissionRef
	for _, s := range resp.Data {
		d := s.Str("submittedDate")
		if d == "" || (best != nil && cmp.Compare(d, best.SubmittedDate) <= 0) {
			continue
		}
		best = &submissionRef{ID: s.ID, State: s.Str("state"), Platform: s.Str("platform"), SubmittedDate: d}
	}
	return best, nil
}

// decodeReviewMessages turns resolutionCenterMessages with their included
// rejections and actors into messages, oldest first.
func decodeReviewMessages(res *tool.ListResult) []reviewMessage {
	byID := map[string]tool.Resource{}
	for _, r := range res.Included {
		byID[r.Type+":"+r.ID] = r
	}
	out := make([]reviewMessage, 0, len(res.Data))
	for _, m := range res.Data {
		msg := reviewMessage{ID: m.ID, Date: m.Str("createdDate"), Text: tool.Untrusted(htmlToText(string(m.Text("messageBody"))))}
		if id := m.RelID("fromActor"); id != "" {
			for _, typ := range []string{"actors", "reviewActors"} {
				if a, ok := byID[typ+":"+id]; ok {
					msg.From = a.Str("actorType")
					msg.FromName = cmp.Or(a.Text("name"), a.Text("displayName"))
					break
				}
			}
		}
		for _, id := range m.RelIDs("rejections") {
			if r, ok := byID["reviewRejections:"+id]; ok {
				msg.Rejections = append(msg.Rejections, rejectionReasons(r)...)
			}
		}
		out = append(out, msg)
	}
	slices.SortStableFunc(out, func(a, b reviewMessage) int { return cmp.Compare(a.Date, b.Date) })
	return out
}

// rejectionReasons reads a reviewRejection's reasons[] (older payloads:
// reviewRejectionReasons[]).
func rejectionReasons(r tool.Resource) []rejectionReason {
	raw, _ := r.Attributes["reasons"].([]any)
	if raw == nil {
		raw, _ = r.Attributes["reviewRejectionReasons"].([]any)
	}
	var out []rejectionReason
	for _, e := range raw {
		m, _ := e.(map[string]any)
		str := func(k string) string {
			switch v := m[k].(type) {
			case string:
				return strings.TrimSpace(v)
			case tool.Untrusted:
				return strings.TrimSpace(string(v))
			}
			return ""
		}
		rr := rejectionReason{Guideline: str("reasonSection"), Code: str("reasonCode"), Description: tool.Untrusted(htmlToText(str("reasonDescription")))}
		if rr != (rejectionReason{}) {
			out = append(out, rr)
		}
	}
	return out
}

var (
	htmlLink   = regexp.MustCompile(`(?is)<a\s[^>]*href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)
	htmlBreak  = regexp.MustCompile(`(?i)<br\s*/?>|</?(p|div|ul|ol|table|tr|h[1-6])(\s[^>]*)?>|</li>|<li(\s[^>]*)?>`)
	htmlTag    = regexp.MustCompile(`<[^>]*>`)
	blankLines = regexp.MustCompile(`\n{3,}`)
)

// htmlToText renders Apple's message HTML as plain text: line breaks for
// blocks, "- " list items, links as "text (url)", tags dropped, entities
// decoded.
func htmlToText(s string) string {
	s = htmlLink.ReplaceAllStringFunc(s, func(a string) string {
		m := htmlLink.FindStringSubmatch(a)
		text := strings.TrimSpace(htmlTag.ReplaceAllString(m[2], ""))
		if text == "" || text == m[1] {
			return m[1]
		}
		return text + " (" + m[1] + ")"
	})
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
	s = htmlBreak.ReplaceAllStringFunc(s, func(t string) string {
		switch t = strings.ToLower(t); {
		case strings.HasPrefix(t, "<li"):
			return "\n- "
		case t == "</li>":
			return ""
		}
		return "\n"
	})
	s = html.UnescapeString(htmlTag.ReplaceAllString(s, ""))
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(strings.ReplaceAll(l, "\u00a0", " ")), " ")
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
