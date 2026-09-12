package jobs

import (
	"net/http"
	"testing"
)

const oneStar = `{"type":"customerReviews","id":"r1","attributes":{"rating":1,"title":"Crashes","body":"Crashes on launch",` +
	`"reviewerNickname":"anon","createdDate":"2026-10-20T10:00:00Z","territory":"SWE"}}`

// Job 3: "any new 1-star reviews? reply to them": one list call, then a
// reply preview and its confirmation.
func TestJobReplyToOneStarReviews(t *testing.T) {
	k := newKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/apps/app1/customerReviews?exists[publishedResponse]=false", http.StatusOK,
		`{"data":[`+oneStar+`],"links":{}}`)
	k.ASC.Handle(http.MethodGet, "/v1/customerReviews/r1", http.StatusOK, `{"data":`+oneStar+`}`)
	k.ASC.Handle(http.MethodPost, "/v1/customerReviewResponses", http.StatusCreated,
		`{"data":{"type":"customerReviewResponses","id":"new1","attributes":{"responseBody":"x","state":"PENDING_PUBLISH"}}}`)

	j := connect(t, k)
	j.call("reviews_list", `{"app":"Hjul","rating":[1],"unanswered":true}`)
	reply := `{"app":"Hjul","replies":[{"review_id":"r1","body":"Sorry! Fixed in 1.4."}]}`
	preview := j.call("reviews_reply", reply)
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("preview mutated: %v (%v)", k.ASC.Mutations(), preview)
	}
	j.call("reviews_reply", `{"app":"Hjul","confirm":true,"replies":[{"review_id":"r1","body":"Sorry! Fixed in 1.4."}]}`)
	j.done(3)

	if m := k.ASC.Mutations(); len(m) != 1 || m[0].Method != http.MethodPost || m[0].Path != "/v1/customerReviewResponses" {
		t.Fatalf("mutations = %v", m)
	}
}
