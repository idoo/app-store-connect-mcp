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

func releaseReviewMessages(ctx context.Context, env *tool.Env, in reviewMessagesIn) (*tool.Result, error) {
	var out reviewMessagesOut
	sub := strings.TrimSpace(in.Submission)
	if sub == "" || in.App != "" {
		app, err := env.Resolve.App(ctx, in.App)
		if err != nil {
			return nil, err
		}
		ref := newAppRef(app)
		out.App = &ref
		out.ResolutionCenter = "https://appstoreconnect.apple.com/apps/" + url.PathEscape(app.ID) + "/distribution/reviewsubmissions"
		if sub == "" {
			s, err := newestSubmission(ctx, env, app, app.Platform(in.Platform))
			if err != nil {
				return nil, err
			}
			if s == nil {
				return nil, &tool.ErrNotFound{
					Kind: "review submission", Ref: string(app.Platform(in.Platform)),
					Hint: "this app has no submitted review submission on that platform",
					Next: []tool.NextStep{{Tool: "release_review_status", Args: map[string]any{"app": app.ID}}},
				}
			}
			out.Submission = *s
			sub = s.ID
		}
	}
	if out.Submission.ID == "" {
		out.Submission.ID = sub
	}
	threads, err := env.Web.List(ctx, "/v1/resolutionCenterThreads", tool.ListOptions{
		Query: url.Values{"filter[reviewSubmission]": {sub}}, Limit: -1, MaxItems: maxReviewThreads,
	})
	if err != nil {
		return nil, fmt.Errorf("read Resolution Center threads of submission %s: %w", sub, err)
	}
	out.Threads = []reviewThread{}
	guidelines := map[string]bool{}
	for _, th := range threads.Data {
		t := reviewThread{ID: th.ID, Type: th.Str("threadType"), State: th.Str("state"), Created: th.Str("createdDate")}
		msgs, err := env.Web.List(ctx, "/v1/resolutionCenterThreads/"+url.PathEscape(th.ID)+"/resolutionCenterMessages", tool.ListOptions{
			Include: []string{"rejections", "fromActor"}, Limit: -1, MaxItems: maxReviewMessages,
		})
		if err != nil {
			return nil, fmt.Errorf("read Resolution Center messages of thread %s: %w", th.ID, err)
		}
		t.Messages = decodeReviewMessages(msgs)
		for _, m := range t.Messages {
			for _, r := range m.Rejections {
				if r.Guideline != "" {
					guidelines[r.Guideline] = true
				}
			}
		}
		out.Threads = append(out.Threads, t)
	}
	for g := range guidelines {
		out.Guidelines = append(out.Guidelines, g)
	}
	slices.Sort(out.Guidelines)
	if len(out.Threads) == 0 {
		out.Note = "no Resolution Center thread for this submission: App Review has not written about it"
	}
	res := tool.OK(out)
	if out.App != nil {
		res.Then(tool.NextStep{Tool: "release_status", Args: map[string]any{"app": out.App.ID}})
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
		msg := reviewMessage{Date: m.Str("createdDate"), Text: tool.Untrusted(htmlToText(string(m.Text("messageBody"))))}
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
