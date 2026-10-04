package reviews

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

func mustTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	return tl
}

// review renders one customerReviews resource; respID links a response.
func review(id string, rating int, created, territory, respID string) string {
	rel := ""
	if respID != "" {
		rel = fmt.Sprintf(`,"relationships":{"response":{"data":{"type":"customerReviewResponses","id":%q}}}`, respID)
	}
	return fmt.Sprintf(`{"type":"customerReviews","id":%q,"attributes":{"rating":%d,"title":"T %s","body":"Body %s",`+
		`"reviewerNickname":"nick","createdDate":%q,"territory":%q}%s}`, id, rating, id, id, created, territory, rel)
}

func response(id, body string) string {
	return fmt.Sprintf(`{"type":"customerReviewResponses","id":%q,"attributes":{"responseBody":%q,"state":"PUBLISHED"}}`, id, body)
}

func page(next string, included []string, items ...string) string {
	links := ""
	if next != "" {
		links = fmt.Sprintf(`,"links":{"next":%q}`, next)
	}
	return fmt.Sprintf(`{"data":[%s],"included":[%s]%s}`, strings.Join(items, ","), strings.Join(included, ","), links)
}

const (
	d20 = "2026-10-20T10:00:00-07:00"
	d18 = "2026-10-18T10:00:00-07:00"
	d16 = "2026-10-16T10:00:00-07:00"
	d14 = "2026-10-14T10:00:00-07:00"
)

func listKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t)
	k.Resolve.Apps = []tool.App{{ID: "a1", Name: "Alpha", BundleID: "se.alpha"}, {ID: "a2", Name: "Beta", BundleID: "se.beta"}}
	k.Env.Profile.DefaultApp = "a1"
	return k
}

// twoPages registers app a1's reviews: page 1 (d20, d18) linking page 2 (d16, d14).
func twoPages(k *tooltest.Kit) {
	next := "https://api.appstoreconnect.apple.com/v1/apps/a1/customerReviews?cursor=P2&limit=2"
	k.ASC.Handle("GET", "/v1/apps/a1/customerReviews", 200,
		page(next, []string{response("resp1", "Thanks!")}, review("r4", 5, d20, "USA", ""), review("r3", 2, d18, "DEU", "resp1")))
	k.ASC.Handle("GET", "/v1/apps/a1/customerReviews?cursor=P2", 200,
		page("", nil, review("r2", 4, d16, "FRA", ""), review("r1", 1, d14, "XXX", "")))
}

type listOut struct {
	Apps []struct {
		App struct {
			ID string `json:"id"`
		} `json:"app"`
		Since   string `json:"since"`
		Cursor  string `json:"cursor"`
		More    bool   `json:"more"`
		Reviews []struct {
			ID          string            `json:"id"`
			Created     string            `json:"createdDate"`
			ReplyLocale string            `json:"reply_locale"`
			Body        map[string]string `json:"body"`
			Response    *responseOut      `json:"response"`
		} `json:"reviews"`
	} `json:"apps"`
	Returned        int `json:"returned"`
	AppsScanned     int `json:"apps_scanned"`
	AppsWithReviews int `json:"apps_with_reviews"`
}

func ids(o listOut) []string {
	var out []string
	for _, a := range o.Apps {
		for _, r := range a.Reviews {
			out = append(out, a.App.ID+"/"+r.ID)
		}
	}
	return out
}

func TestListSinceLastSeenStopsWithoutOverFetchAndDoesNotAdvance(t *testing.T) {
	k := listKit(t)
	twoPages(k)
	if err := k.State.SetCursor("test", "a1", cursorName, "2026-10-19T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env, `{"since":"last_seen"}`))
	o := tooltest.Decode[listOut](t, res)
	if got := strings.Join(ids(o), ","); got != "a1/r4" {
		t.Fatalf("reviews = %s", got)
	}
	if n := len(k.ASC.Requests()); n != 1 {
		t.Fatalf("expected 1 request (stop before page 2), got %d: %+v", n, k.ASC.Requests())
	}
	if r4 := o.Apps[0].Reviews[0]; r4.Response != nil || r4.ReplyLocale != "en-US" {
		t.Fatalf("r4 = %+v", r4)
	}
	if o.Apps[0].Reviews[0].Body["untrusted"] != "Body r4" {
		t.Fatalf("body not wrapped as untrusted: %+v", o.Apps[0].Reviews[0].Body)
	}
	if cur, _ := k.State.Cursor("test", "a1", cursorName); cur != "2026-10-19T00:00:00Z" {
		t.Fatalf("read advanced the cursor to %q", cur)
	}
	q := k.ASC.Requests()[0].Query
	if q.Get("sort") != "-createdDate" || q.Get("include") != "response" {
		t.Fatalf("query = %v", q)
	}
}

func TestListMarkSeenAdvancesCursorAndPages(t *testing.T) {
	k := listKit(t)
	twoPages(k)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env, `{"since":"2026-10-15","mark_seen":true}`))
	o := tooltest.Decode[listOut](t, res)
	if got := strings.Join(ids(o), ","); got != "a1/r4,a1/r3,a1/r2" {
		t.Fatalf("reviews = %s", got)
	}
	r3 := o.Apps[0].Reviews[1]
	if r3.Response == nil || r3.Response.ID != "resp1" || r3.Response.Body != "Thanks!" || r3.ReplyLocale != "de-DE" {
		t.Fatalf("r3 = %+v", r3)
	}
	// Page 2 was fetched once and paging stopped at r1 (older than since).
	if n := len(k.ASC.Requests()); n != 2 {
		t.Fatalf("requests = %d", n)
	}
	if cur, _ := k.State.Cursor("test", "a1", cursorName); cur != d20 {
		t.Fatalf("cursor = %q, want %q", cur, d20)
	}
	if o.Apps[0].Cursor != d20 {
		t.Fatalf("result cursor = %q", o.Apps[0].Cursor)
	}
	// A second last_seen read now finds nothing new and stays on page 1.
	res = tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env, `{"since":"last_seen"}`))
	if o := tooltest.Decode[listOut](t, res); o.Returned != 0 {
		t.Fatalf("expected no new reviews, got %v", ids(o))
	}
}

func TestListFanOutOverAllApps(t *testing.T) {
	k := listKit(t)
	k.Env.Profile.DefaultApp = ""
	k.ASC.Handle("GET", "/v1/apps", 200,
		`{"data":[{"type":"apps","id":"a1","attributes":{"name":"Alpha","bundleId":"se.alpha"}},`+
			`{"type":"apps","id":"a2","attributes":{"name":"Beta","bundleId":"se.beta"}}]}`)
	k.ASC.Handle("GET", "/v1/apps/a1/customerReviews", 200, page("", nil, review("r4", 5, d20, "USA", ""), review("r2", 3, d16, "USA", "")))
	k.ASC.Handle("GET", "/v1/apps/a2/customerReviews", 200, page("", nil, review("b3", 2, d18, "SWE", ""), review("b1", 1, d14, "SWE", "")))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env, `{"limit":3}`))
	o := tooltest.Decode[listOut](t, res)
	if got := strings.Join(ids(o), ","); got != "a1/r4,a1/r2,a2/b3" {
		t.Fatalf("reviews = %s", got)
	}
	if o.Returned != 3 || !o.Apps[1].More {
		t.Fatalf("returned=%d more=%v", o.Returned, o.Apps[1].More)
	}
	if n := len(k.ASC.RequestsTo("GET", "/v1/apps/{id}/customerReviews")); n != 2 {
		t.Fatalf("expected 2 review lists, got %d", n)
	}
}

func TestListFanOutOmitsAppsWithoutReviews(t *testing.T) {
	k := listKit(t)
	k.ASC.Handle("GET", "/v1/apps", 200,
		`{"data":[{"type":"apps","id":"a1","attributes":{"name":"Alpha"}},{"type":"apps","id":"a2","attributes":{"name":"Beta"}},`+
			`{"type":"apps","id":"a3","attributes":{"name":"Gamma"}}]}`)
	k.ASC.Handle("GET", "/v1/apps/a1/customerReviews", 200, page("", nil))
	k.ASC.Handle("GET", "/v1/apps/a2/customerReviews", 200, page("", nil, review("b1", 1, d14, "SWE", "")))
	k.ASC.Handle("GET", "/v1/apps/a3/customerReviews", 200, page("", nil))
	o := tooltest.Decode[listOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env, `{"app":"all","rating":[1]}`)))
	if len(o.Apps) != 1 || o.Apps[0].App.ID != "a2" || o.AppsScanned != 3 || o.AppsWithReviews != 1 {
		t.Fatalf("out = %+v", o)
	}
}

func TestListUnansweredFilterQuery(t *testing.T) {
	k := listKit(t)
	k.ASC.Handle("GET", "/v1/apps/a2/customerReviews?exists[publishedResponse]=false", 200, page("", nil, review("b1", 1, d14, "SWE", "")))
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env,
		`{"app":"se.beta","unanswered":true,"rating":[1,2],"territory":["swe"]}`))
	if got := strings.Join(ids(tooltest.Decode[listOut](t, res)), ","); got != "a2/b1" {
		t.Fatalf("reviews = %s", got)
	}
	q := k.ASC.Requests()[0].Query
	if q.Get("exists[publishedResponse]") != "false" || q.Get("filter[rating]") != "1,2" || q.Get("filter[territory]") != "SWE" {
		t.Fatalf("query = %v", q)
	}
}

func replyKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := listKit(t)
	k.ASC.Handle("GET", "/v1/customerReviews/r3", 200,
		`{"data":`+review("r3", 2, d18, "DEU", "resp1")+`,"included":[`+response("resp1", "Old reply")+`]}`)
	k.ASC.Handle("GET", "/v1/customerReviews/r4", 200, `{"data":`+review("r4", 5, d20, "USA", "")+`}`)
	k.ASC.Handle("POST", "/v1/customerReviewResponses", http.StatusCreated,
		`{"data":{"type":"customerReviewResponses","id":"new1","attributes":{"responseBody":"x","state":"PENDING_PUBLISH"}}}`)
	return k
}

const replyArgs = `{"app":"a1","confirm":true,"replies":[{"review_id":"r3","body":"Danke, fixed in 2.1"}]}`

// Apple's POST customerReviewResponses replaces an existing response, so an
// edit is a single POST and never a DELETE.
func TestReplyReplacesExistingWithSinglePost(t *testing.T) {
	k := replyKit(t)
	asked := k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_reply"), k.Env, replyArgs))
	muts := k.ASC.Mutations()
	if len(muts) != 1 || muts[0].Method != "POST" || muts[0].Path != "/v1/customerReviewResponses" {
		t.Fatalf("mutations = %+v", muts)
	}
	body := tooltest.JSON(t, muts[0].Body)
	if !strings.Contains(body, `"responseBody":"Danke, fixed in 2.1"`) || !strings.Contains(body, `"id":"r3"`) {
		t.Fatalf("POST body = %s", body)
	}
	if len(*asked) != 1 || !strings.Contains((*asked)[0], "Danke, fixed in 2.1") || !strings.Contains((*asked)[0], "replaces existing response resp1") ||
		strings.Contains((*asked)[0], "are deleted first") {
		t.Fatalf("prompt = %v", *asked)
	}
	out := tooltest.Decode[map[string]any](t, res)
	results, _ := out["results"].([]any)
	first, _ := results[0].(map[string]any)
	if first["status"] != "replaced" || first["response_id"] != "new1" || first["replaced_response_id"] != "resp1" {
		t.Fatalf("result = %v", out)
	}
	if cur, _ := k.State.Cursor("test", "a1", cursorName); cur != d18 {
		t.Fatalf("cursor = %q, want %q", cur, d18)
	}
}

func TestReplyNewResponseOnlyPosts(t *testing.T) {
	k := replyKit(t)
	k.Elicit(true)
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_reply"), k.Env,
		`{"app":"a1","confirm":true,"replies":[{"review_id":"r4","body":"Thank you!"}]}`))
	if muts := k.ASC.Mutations(); len(muts) != 1 || muts[0].Method != "POST" {
		t.Fatalf("mutations = %+v", muts)
	}
}

func TestReplyPreviewWithoutConfirmChangesNothing(t *testing.T) {
	k := replyKit(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_reply"), k.Env, strings.Replace(replyArgs, `"confirm":true,`, "", 1)))
	if out := tooltest.Decode[map[string]any](t, res); out["status"] != tool.ErrNeedsConfirm.Code {
		t.Fatalf("preview = %v", out)
	}
	if len(k.ASC.Mutations()) != 0 {
		t.Fatal("preview mutated")
	}
	// Customer text sits only in Untrusted; Intended holds reply texts first.
	p := tooltest.Decode[struct {
		Preview tool.PlanPreview `json:"preview"`
	}](t, res).Preview
	intended := tooltest.JSON(t, p.Intended)
	if len(p.Untrusted) == 0 || !strings.HasPrefix(p.Untrusted[0], "review ") ||
		strings.Contains(intended, `"review"`) || !strings.Contains(intended, `"reply"`) {
		t.Fatalf("preview intended = %s, untrusted = %q", intended, p.Untrusted)
	}
}

func TestReplyRequiresHuman(t *testing.T) {
	t.Run("decline", func(t *testing.T) {
		k := replyKit(t)
		k.Elicit(false)
		tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "reviews_reply"), k.Env, replyArgs), tool.ErrDeclined.Code)
		if len(k.ASC.Mutations()) != 0 {
			t.Fatal("declined reply mutated")
		}
	})
	t.Run("no elicitation", func(t *testing.T) {
		k := replyKit(t)
		tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "reviews_reply"), k.Env, replyArgs), tool.ErrHumanRequired.Code)
		if len(k.ASC.Mutations()) != 0 {
			t.Fatal("unapproved reply mutated")
		}
	})
	t.Run("allow no elicit", func(t *testing.T) {
		k := replyKit(t)
		k.Env.Profile.AllowNoElicit = true
		tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_reply"), k.Env, `{"app":"a1","replies":[{"review_id":"r3","body":"Danke, fixed in 2.1"}]}`))
		tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_reply"), k.Env, replyArgs))
		if len(k.ASC.Mutations()) != 1 {
			t.Fatalf("mutations = %+v", k.ASC.Mutations())
		}
	})
}

func TestReplyBodyOverLimitRejectedBeforeRequests(t *testing.T) {
	k := replyKit(t)
	k.Elicit(true)
	long := strings.Repeat("a", 5971)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "reviews_reply"), k.Env,
		`{"confirm":true,"replies":[{"review_id":"r4","body":"`+long+`"}]}`), tool.ErrInvalidArgs.Code)
	if n := len(k.ASC.Requests()); n != 0 {
		t.Fatalf("expected no requests, got %d", n)
	}
}

func TestReplyTooManyRejected(t *testing.T) {
	k := replyKit(t)
	items := make([]string, maxReplies+1)
	for i := range items {
		items[i] = fmt.Sprintf(`{"review_id":"r%d","body":"hi"}`, i)
	}
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "reviews_reply"), k.Env,
		`{"replies":[`+strings.Join(items, ",")+`]}`), tool.ErrInvalidArgs.Code)
	if len(k.ASC.Requests()) != 0 {
		t.Fatal("requests made")
	}
}
