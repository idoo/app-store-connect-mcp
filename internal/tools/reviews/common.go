package reviews

import (
	"context"
	"fmt"
	"sync"
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
		Title:     r.Text("title"),
		Body:      r.Text("body"),
		Nickname:  tool.Untrusted(r.Str("reviewerNickname")),
	}
	out.created, _ = tool.ParseInstant(out.Created)
	if loc, ok := applemeta.LocaleForTerritory(out.Territory); ok {
		out.ReplyLocale = loc
	}
	if id := r.RelID("response"); id != "" {
		resp := responses[id]
		out.Response = &responseOut{
			ID: id, Body: resp.Str("responseBody"), State: resp.Str("state"), Modified: resp.Str("lastModifiedDate"), // trusted: the developer's own published reply
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

// cursorLocks serialise load→compare→save of one profile+app cursor within
// the process, so concurrent calls never store an older value.
var cursorLocks sync.Map // key → *sync.Mutex

// advanceCursor stores value as the app's reviews cursor unless the stored
// cursor is already at or past it. It returns the cursor now stored.
func advanceCursor(env *tool.Env, appID string, newest time.Time, value string) (string, error) {
	m, _ := cursorLocks.LoadOrStore(env.Profile.Name+"\x00"+appID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	stored, err := env.State.Cursor(env.Profile.Name, appID, cursorName)
	if err != nil {
		return "", fmt.Errorf("read reviews cursor: %w", err)
	}
	if st, perr := tool.ParseInstant(stored); stored != "" && perr == nil && !newest.After(st) {
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
