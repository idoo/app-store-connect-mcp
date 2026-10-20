package reviews

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const maxReplies = 20

type replyIn struct {
	tool.ConfirmFlag
	App     tool.AppRef `json:"app,omitempty" jsonschema:"the reviews' app (named in the result); default: pinned"`
	Replies []replyItem `json:"replies" jsonschema:"1-20 replies; an existing response is replaced"`
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
	Replaced   string `json:"replaced_response_id,omitempty"`
	// Outcome is "error" or "unknown" (may have been applied) for a failed reply.
	Outcome string `json:"outcome,omitempty"`
	Error   any    `json:"error,omitempty"`
	err     error
}

func init() {
	tool.Register(tool.New(tool.Meta{
		Name: "reviews_reply", Toolset: tool.Reviews, Class: tool.Destructive, Confirm: tool.ConfirmByFlag, RequireHuman: true,
		Title: "Reply to customer reviews",
		Description: "Publish public replies to up to 20 reviews (from reviews_list); an existing response is replaced. " +
			"Without confirm:true previews; the human approves every reply text.",
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
	app, appErr := env.Resolve.App(ctx, in.App)
	if appErr != nil && in.App != "" {
		return nil, appErr
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
	var warnings []string
	links, ids := replyContacts(in.Replies)
	if len(links) > 0 {
		warnings = append(warnings, "Reply contains link/email: "+quoteAll(links)+" — confirm it is yours")
	}
	if len(ids) > 0 {
		warnings = append(warnings, "Reply contains an id or API key id: "+quoteAll(ids)+" — confirm it is meant to be public")
	}
	irreversible := []string{"replies are public on the App Store immediately after Apple processes them"}
	if replacing > 0 {
		summary += fmt.Sprintf(" (%d replace existing responses)", replacing)
		irreversible = append(irreversible, "a replaced response's old text cannot be restored (Apple swaps it in one request; nothing is deleted first)")
	}
	wctx, gate := tool.RequireConfirm(ctx, env, in.Confirm, tool.PlanPreview{
		Summary: summary, Intended: previews, Affected: affected, Irreversible: irreversible, Untrusted: untrusted,
		Warnings: warnings,
	})
	if gate != nil {
		for _, w := range warnings {
			gate.Warn(w)
		}
		return gate, nil
	}

	outcomes := make([]replyOutcome, len(in.Replies))
	var errs []error
	for i, r := range in.Replies {
		out := postReply(wctx, env, reviews[i], r.Body)
		outcomes[i] = out
		if out.err != nil {
			errs = append(errs, out.err)
		}
	}
	failed := len(errs)
	// Replying never moves the reviews_list cursor: only reviews_list
	// mark_seen, which knows what the agent actually read, does.
	data := map[string]any{"replied": len(outcomes) - failed, "failed": failed, "results": outcomes}
	if appErr == nil {
		data["app"] = appRef{ID: app.ID, Name: app.Name, BundleID: app.BundleID}
	}
	return tool.Batch(data, len(outcomes)-failed, 0, errs), nil
}

// Contact separators: a plain "."/"@", a bracketed form ("[.]", "(dot)",
// "{at}") or, for addresses only, a spelled one (" at ", " dot ").
const (
	dotSep     = `(?:\.|\s*[\[({]\s*(?:\.|dot)\s*[\])}]\s*)`
	atSep      = `(?:@|\s*[\[({]\s*at\s*[\])}]\s*|\s+at\s+)`
	spelledDot = `(?:` + dotSep + `|\s+dot\s+)`
	label      = `[a-z0-9](?:[a-z0-9-]*[a-z0-9])?`
)

// linkRE matches URLs, email addresses (also "ivan at example dot com" and
// "ivan(at)example[.]com"), bare and bracket-obfuscated domain names
// (evil.example, evil[.]example) and spelled-out domains with a common TLD
// ("evil dot com"). It runs on [foldContact]'s ASCII-folded copy.
var linkRE = regexp.MustCompile(`(?i)https?://\S+|\bwww\.\S+` +
	`|[a-z0-9._%+-]+` + atSep + label + `(?:` + spelledDot + label + `)+` +
	`|\b(?:` + label + dotSep + `)+[a-z]{2,}\b(?:/\S*)?` +
	`|\b(?:` + label + `\s+dot\s+)+(?:com|net|org|io|app|dev|co|me|info|xyz|ru|se|de|uk)\b`)

// idRE matches identifiers that should not go public: UUIDs (issuer ids,
// ASC resource ids) and 10-character API key ids. A key-id candidate needs
// a digit and a letter ([keyIDLike]).
var idRE = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b|\b[A-Z0-9]{10}\b`)

// replyContacts returns the distinct links/emails and ids in the reply
// bodies, so the human confirms that text copied from a review isn't sent
// publicly. Matching runs on an ASCII-folded copy (fullwidth forms,
// Cyrillic/Greek lookalikes, math letters, invisible marks), but every
// result is the original substring, e.g. "еvil.com" with a Cyrillic е.
func replyContacts(replies []replyItem) (links, ids []string) {
	for _, r := range replies {
		f := foldContact(r.Body)
		for _, m := range linkRE.FindAllStringIndex(f.text, -1) {
			links = appendNew(links, strings.TrimRight(f.original(r.Body, m), `.,;:!?)]}"'`))
		}
		for _, m := range idRE.FindAllStringIndex(f.text, -1) {
			if s := f.text[m[0]:m[1]]; len(s) == 36 || keyIDLike(s) {
				ids = appendNew(ids, f.original(r.Body, m))
			}
		}
	}
	return links, ids
}

// keyIDLike reports whether s, 10 characters, looks like an API key id:
// upper case with at least one digit and one letter.
func keyIDLike(s string) bool {
	return s == strings.ToUpper(s) && strings.ContainsAny(s, "0123456789") &&
		strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
}

func appendNew(list []string, s string) []string {
	if s == "" || slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

func quoteAll(list []string) string {
	q := make([]string, len(list))
	for i, l := range list {
		q[i] = fmt.Sprintf("%q", l)
	}
	return strings.Join(q, ", ")
}

// folded is an ASCII-folded copy of a text; start[i] and end[i] are the
// byte span in the original of the rune that produced folded byte i.
type folded struct {
	text       string
	start, end []int
}

// original returns the original substring behind the folded span m,
// widened over adjacent letters, digits and marks so an unfolded lookalike
// at either edge is reported rather than cut off.
func (f folded) original(s string, m []int) string {
	a, b := f.start[m[0]], f.end[m[1]-1]
	for a > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:a])
		if !wordRune(r) {
			break
		}
		a -= n
	}
	for b < len(s) {
		r, n := utf8.DecodeRuneInString(s[b:])
		if !wordRune(r) {
			break
		}
		b += n
	}
	return s[a:b]
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)
}

// foldContact folds s to ASCII where a rune is a common lookalike of an
// ASCII character, and drops invisible and combining marks.
func foldContact(s string) folded {
	var f folded
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if fr := foldRune(r); fr >= 0 {
			before := b.Len()
			b.WriteRune(fr)
			for range b.Len() - before {
				f.start = append(f.start, i)
				f.end = append(f.end, i+n)
			}
		}
		i += n
	}
	f.text = b.String()
	return f
}

// lookalikes maps Cyrillic and Greek letters to the Latin letter they
// render like.
var lookalikes = map[rune]rune{
	'а': 'a', 'е': 'e', 'о': 'o', 'р': 'p', 'с': 'c', 'у': 'y', 'х': 'x', 'і': 'i', 'ј': 'j', 'ѕ': 's',
	'ԁ': 'd', 'һ': 'h', 'ӏ': 'l', 'ԛ': 'q', 'ԝ': 'w', 'к': 'k',
	'А': 'A', 'В': 'B', 'Е': 'E', 'К': 'K', 'М': 'M', 'Н': 'H', 'О': 'O', 'Р': 'P', 'С': 'C', 'Т': 'T',
	'Х': 'X', 'У': 'Y', 'І': 'I', 'Ј': 'J', 'Ѕ': 'S',
	'α': 'a', 'ε': 'e', 'ι': 'i', 'κ': 'k', 'ν': 'v', 'ο': 'o', 'ρ': 'p', 'υ': 'u', 'χ': 'x',
	'Α': 'A', 'Β': 'B', 'Ε': 'E', 'Ζ': 'Z', 'Η': 'H', 'Ι': 'I', 'Κ': 'K', 'Μ': 'M', 'Ν': 'N', 'Ο': 'O',
	'Ρ': 'P', 'Τ': 'T', 'Υ': 'Y', 'Χ': 'X',
	'。': '.', '｡': '.', '․': '.', '﹒': '.', '﹫': '@',
}

// foldRune returns r's ASCII lookalike, r itself, or -1 to drop it.
func foldRune(r rune) rune {
	switch {
	case r >= 0xFF01 && r <= 0xFF5E: // fullwidth ASCII
		return r - 0xFEE0
	case r >= 0x1D400 && r <= 0x1D6A3: // mathematical alphanumeric letters
		off := (r - 0x1D400) % 52
		if off < 26 {
			return 'A' + off
		}
		return 'a' + off - 26
	case r >= 0x1D7CE && r <= 0x1D7FF: // mathematical digits
		return '0' + (r-0x1D7CE)%10
	case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Cf, r), r == 0x00AD:
		return -1
	}
	if l, ok := lookalikes[r]; ok {
		return l
	}
	return r
}

// postReply creates the response of one review. Apple's POST
// customerReviewResponses creates or replaces the developer response in one
// request, so an edit never deletes first and can't leave the review without
// a reply when the POST fails.
func postReply(ctx context.Context, env *tool.Env, rv reviewOut, body string) replyOutcome {
	out := replyOutcome{ReviewID: rv.ID, Status: "replied"}
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
		if rv.Response != nil {
			msg = "replace reply (the existing response " + rv.Response.ID + " is unchanged unless Apple applied it; check reviews_list)"
		}
		out.err = fmt.Errorf("%s: %w", msg, err)
		out.Status = "failed"
		out.Outcome, out.Error = tool.ItemError(out.err)
		return out
	}
	if rv.Response != nil {
		out.Status, out.Replaced = "replaced", rv.Response.ID
	}
	if r, ok := resp.First(); ok {
		out.ResponseID, out.State = r.ID, r.Str("state")
	}
	return out
}
