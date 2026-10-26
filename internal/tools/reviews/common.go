package reviews

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

const (
	// cursorName is the StateStore cursor name for the reviews "since" cursor.
	cursorName = "reviews"
	// responseType is the JSON:API type of a public review response.
	responseType = "customerReviewResponses"
	// excerptRunes caps review text quoted in approval prompts.
	excerptRunes = 200
)

// reviewFields and responseFields are the sparse fieldsets for review reads.
var (
	reviewFields   = []string{"rating", "title", "body", "reviewerNickname", "createdDate", "territory", "response"}
	responseFields = []string{"responseBody", "lastModifiedDate", "state"}
	readFields     = map[string][]string{"customerReviews": reviewFields, responseType: responseFields}
)

// responseOut is the developer's public response to a review.
type responseOut struct {
	ID       string `json:"id"`
	Body     string `json:"body"`
	State    string `json:"state,omitempty"`
	Modified string `json:"lastModifiedDate,omitempty"`
}

// reviewOut is one customer review; reviewer-written text is untrusted.
type reviewOut struct {
	ID          string         `json:"id"`
	Rating      int            `json:"rating"`
	Territory   string         `json:"territory,omitempty"`
	Created     string         `json:"createdDate"`
	ReplyLocale string         `json:"reply_locale,omitempty"`
	Title       tool.Untrusted `json:"title"`
	Body        tool.Untrusted `json:"body"`
	Nickname    tool.Untrusted `json:"nickname"`
	Response    *responseOut   `json:"response,omitempty"`

	created time.Time
}

// toReview converts a flattened customerReviews resource, looking its
// response up in included.
func toReview(r tool.Resource, responses map[string]tool.Resource) reviewOut {
	rating, _ := r.Int("rating")
	out := reviewOut{
		ID:        r.ID,
		Rating:    rating,
		Territory: r.Str("territory"),
		Created:   r.Str("createdDate"),
		Title:     tool.Untrusted(r.Str("title")),
		Body:      tool.Untrusted(r.Str("body")),
		Nickname:  tool.Untrusted(r.Str("reviewerNickname")),
	}
	out.created, _ = parseTime(out.Created)
	if loc, ok := applemeta.LocaleForTerritory(out.Territory); ok {
		out.ReplyLocale = loc
	}
	if id := r.RelID("response"); id != "" {
		resp := responses[id]
		out.Response = &responseOut{
			ID: id, Body: resp.Str("responseBody"), State: resp.Str("state"), Modified: resp.Str("lastModifiedDate"),
		}
	}
	return out
}

// responsesByID indexes included customerReviewResponses by id.
func responsesByID(included []tool.Resource) map[string]tool.Resource {
	m := make(map[string]tool.Resource)
	for _, r := range included {
		if r.Type == responseType {
			m[r.ID] = r
		}
	}
	return m
}

// parseTime accepts RFC 3339 (with or without fractional seconds) and plain
// YYYY-MM-DD dates (UTC midnight).
func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q is not an ISO date or RFC 3339 time", tool.ErrInvalidArgs, s)
	}
	return t, nil
}

// advanceCursor stores value as the app's reviews cursor unless the stored
// cursor is already at or past it. It returns the cursor now stored.
func advanceCursor(env *tool.Env, appID string, newest time.Time, value string) (string, error) {
	stored, err := env.State.Cursor(env.Profile.Name, appID, cursorName)
	if err != nil {
		return "", fmt.Errorf("read reviews cursor: %w", err)
	}
	if st, perr := parseTime(stored); stored != "" && perr == nil && !newest.After(st) {
		return stored, nil
	}
	if err := env.State.SetCursor(env.Profile.Name, appID, cursorName, value); err != nil {
		return "", fmt.Errorf("store reviews cursor: %w", err)
	}
	return value, nil
}

// excerpt shortens s to excerptRunes runes for approval prompts.
func excerpt(s string) string {
	if utf8.RuneCountInString(s) <= excerptRunes {
		return s
	}
	r := []rune(s)
	return string(r[:excerptRunes]) + "…"
}

// fetchReview reads one review with its existing response.
func fetchReview(ctx context.Context, env *tool.Env, id string) (reviewOut, error) {
	resp, err := env.ASC.Get(ctx, "/v1/customerReviews/"+id, readQuery())
	if err != nil {
		return reviewOut{}, fmt.Errorf("read review %s: %w", id, err)
	}
	r, ok := resp.First()
	if !ok {
		return reviewOut{}, fmt.Errorf("read review %s: empty response", id)
	}
	return toReview(r, responsesByID(resp.Included)), nil
}
