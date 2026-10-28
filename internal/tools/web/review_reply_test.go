package web

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const replyArgs = `"app":"Hjul","text":"Hi team,\nwe fixed the crash & <added> tests."`

const sentMessagesBody = `{"data":[
	{"type":"resolutionCenterMessages","id":"m1","attributes":{"createdDate":"2026-01-02T10:00:00Z","messageBody":"Guideline 2.1"}},
	{"type":"resolutionCenterMessages","id":"m3","attributes":{"createdDate":"2026-01-04T10:00:00Z","messageBody":"Hi team,\nwe fixed it."}}]}`

// replyRoutes serves an UNRESOLVED_ISSUES submission with one open thread.
// reads is how many message reads return the original thread before the
// read-back sees the sent message.
func replyRoutes(k *tooltest.Kit, draft string, reads int) {
	submissionRoutes(k)
	k.Web.Handle("GET", "/v1/resolutionCenterThreads", 200, `{"data":[
		{"type":"resolutionCenterThreads","id":"th0","attributes":{"threadType":"APP_MESSAGE_INFORMATIONAL","createdDate":"2026-01-03T00:00:00Z"}},
		{"type":"resolutionCenterThreads","id":"th1","attributes":{"threadType":"REJECTION_REVIEW_SUBMISSION","state":null,"canDeveloperAddNote":true,"createdDate":"2026-01-02T10:00:00Z"}}]}`)
	for range reads {
		k.Web.Handle("GET", "/v1/resolutionCenterThreads/{id}/resolutionCenterMessages", 200, reviewMessagesBody)
	}
	k.Web.Handle("GET", "/v1/resolutionCenterThreads/{id}/resolutionCenterMessages", 200, sentMessagesBody)
	k.Web.Handle("GET", "/v1/resolutionCenterThreads/{id}/resolutionCenterDraftMessage", 200, draft)
}

func replyPreview(t *testing.T, k *tooltest.Kit, args string) string {
	t.Helper()
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_reply"), k.Env, "{"+args+"}"))
	var body struct {
		Status string `json:"status"`
		PlanID string `json:"plan_id"`
	}
	b, _ := json.Marshal(res.Data)
	if err := json.Unmarshal(b, &body); err != nil || body.PlanID == "" {
		t.Fatalf("preview = %s", b)
	}
	if n := len(k.Web.Mutations()) + len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("preview mutated: %d", n)
	}
	return body.PlanID
}

func TestReviewReplyPreviewThenSend(t *testing.T) {
	k := kit(t)
	replyRoutes(k, `{"data":null}`, 3)
	k.Web.Handle("POST", "/v1/resolutionCenterDraftMessages", 201, `{"data":{"type":"resolutionCenterDraftMessages","id":"d1"}}`)
	k.Web.Handle("POST", "/v1/resolutionCenterMessages", 201, `{"data":{"type":"resolutionCenterMessages","id":"m3","attributes":{"createdDate":"2026-01-04T10:00:00Z"}}}`)

	plan := replyPreview(t, k, replyArgs)
	pv := k.Plans.Proposed[0]
	cur, _ := json.Marshal(pv.Current)
	intended, _ := json.Marshal(pv.Intended)
	for _, s := range []string{`"id":"th1"`, `"guidelines":["2.1","2.3.7","4.0"]`, `"id":"sub1"`, `"state":"UNRESOLVED_ISSUES"`} {
		if !strings.Contains(string(cur), s) {
			t.Errorf("preview current %s lacks %s", cur, s)
		}
	}
	if !strings.Contains(string(intended), `"text":"Hi team,\nwe fixed the crash \u0026 \u003cadded\u003e tests."`) || len(pv.Irreversible) == 0 {
		t.Errorf("preview intended = %s", intended)
	}

	asked := k.Elicit(true)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_reply"), k.Env, `{`+replyArgs+`,"plan_id":"`+plan+`"}`))
	if len(*asked) != 1 {
		t.Errorf("human asked %d times", len(*asked))
	}
	muts := k.Web.Mutations()
	var got []string
	for _, m := range muts {
		got = append(got, m.Method+" "+m.Path)
	}
	if want := []string{"POST /v1/resolutionCenterDraftMessages", "POST /v1/resolutionCenterMessages"}; !slices.Equal(got, want) {
		t.Fatalf("mutations = %q", got)
	}
	// Bodies as App Store Connect's web app sends them (m-appstore main bundle):
	// the textarea text as is, then createFromDraftMessage.
	draft, _ := json.Marshal(muts[0].Body)
	if want := `{"data":{"attributes":{"messageBody":"Hi team,\nwe fixed the crash \u0026 \u003cadded\u003e tests."},` +
		`"relationships":{"resolutionCenterThread":{"data":{"id":"th1","type":"resolutionCenterThreads"}}},"type":"resolutionCenterDraftMessages"}}`; string(draft) != want {
		t.Errorf("draft body = %s", draft)
	}
	send, _ := json.Marshal(muts[1].Body)
	if want := `{"data":{"relationships":{"createFromDraftMessage":{"data":{"id":"d1","type":"resolutionCenterDraftMessages"}}},"type":"resolutionCenterMessages"}}`; string(send) != want {
		t.Errorf("send body = %s", send)
	}
	out := tooltest.Decode[reviewReplyOut](t, res)
	if out.Status != "sent" || out.Message == nil || out.Message.ID != "m3" || !out.Message.Landed || out.Message.Date != "2026-01-04T10:00:00Z" ||
		out.Steps[0].Op != replyCreateDraft || out.Steps[0].ID != "d1" || out.Steps[1].Outcome != outcomeDone {
		t.Errorf("out = %s", tooltest.JSON(t, out))
	}
}

func TestReviewReplyUpdatesExistingDraft(t *testing.T) {
	k := kit(t)
	replyRoutes(k, `{"data":{"type":"resolutionCenterDraftMessages","id":"d7","attributes":{"messageBody":"half-written"}}}`, 3)
	k.Web.Handle("PATCH", "/v1/resolutionCenterDraftMessages/{id}", 200, `{"data":{"type":"resolutionCenterDraftMessages","id":"d7"}}`)
	k.Web.Handle("POST", "/v1/resolutionCenterMessages", 201, `{"data":{"type":"resolutionCenterMessages","id":"m3"}}`)
	plan := replyPreview(t, k, replyArgs)
	if pv := k.Plans.Proposed[0]; len(pv.Warnings) != 1 || !strings.Contains(strings.Join(pv.Untrusted, ""), "half-written") {
		t.Errorf("preview = %+v", pv)
	}
	k.Elicit(true)
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_reply"), k.Env, `{`+replyArgs+`,"plan_id":"`+plan+`"}`))
	muts := k.Web.Mutations()
	if len(muts) != 2 || muts[0].Method != "PATCH" || muts[0].Path != "/v1/resolutionCenterDraftMessages/d7" {
		t.Fatalf("mutations = %+v", muts)
	}
	b, _ := json.Marshal(muts[0].Body)
	if !strings.Contains(string(b), `"id":"d7"`) || strings.Contains(string(b), "relationships") {
		t.Errorf("patch body = %s", b)
	}
	if send, _ := json.Marshal(muts[1].Body); !strings.Contains(string(send), `"id":"d7"`) {
		t.Errorf("send body = %s", send)
	}
}

func TestReviewReplySendFailureIsPartial(t *testing.T) {
	k := kit(t)
	replyRoutes(k, `{"data":null}`, 3)
	k.Web.Handle("POST", "/v1/resolutionCenterDraftMessages", 201, `{"data":{"type":"resolutionCenterDraftMessages","id":"d1"}}`)
	k.Web.HandleFixture("POST", "/v1/resolutionCenterMessages", tooltest.Fixture{Err: fmt.Errorf("POST: %w", tool.ErrOutcomeUnknown)})
	plan := replyPreview(t, k, replyArgs)
	k.Elicit(true)
	res := tooltest.Call(t, mustTool(t, "release_review_reply"), k.Env, `{`+replyArgs+`,"plan_id":"`+plan+`"}`)
	if !res.IsError {
		t.Fatal("failed send reported as success")
	}
	data := tooltest.Decode[replyPartial](t, res)
	if data.Error != tool.ErrOutcomeUnknown.Code || data.Partial.Steps[0].Outcome != outcomeDone || data.Partial.Steps[0].ID != "d1" ||
		data.Partial.Steps[1].Outcome != outcomeUnknown || data.Partial.Message != nil {
		t.Errorf("partial = %s", tooltest.JSON(t, data))
	}
	if n := len(k.Web.RequestsTo("POST", "/v1/resolutionCenterMessages")); n != 1 {
		t.Errorf("send attempted %d times", n)
	}
}

func TestReviewReplyStalePlan(t *testing.T) {
	k := kit(t)
	submissionRoutes(k)
	k.Web.Handle("GET", "/v1/resolutionCenterThreads", 200, reviewThreadsBody)
	k.Web.Handle("GET", "/v1/resolutionCenterThreads/{id}/resolutionCenterMessages", 200, reviewMessagesBody)
	k.Web.Handle("GET", "/v1/resolutionCenterThreads/{id}/resolutionCenterMessages", 200, sentMessagesBody) // a new message arrived
	k.Web.Handle("GET", "/v1/resolutionCenterThreads/{id}/resolutionCenterDraftMessage", 404, `{"errors":[{"status":"404"}]}`)
	plan := replyPreview(t, k, replyArgs)
	k.Elicit(true)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "release_review_reply"), k.Env, `{`+replyArgs+`,"plan_id":"`+plan+`"}`), tool.ErrStalePlan.Code)
	if n := len(k.Web.Mutations()); n != 0 {
		t.Errorf("mutations = %d", n)
	}
}

func TestReviewReplyRefusals(t *testing.T) {
	long := strings.Repeat("a", maxReplyRunes+1)
	for _, c := range []struct {
		name, args, code string
		routes           func(k *tooltest.Kit)
	}{
		{"empty text", `"app":"Hjul","text":"  "`, tool.ErrInvalidArgs.Code, nil},
		{"too long", `"app":"Hjul","text":"` + long + `"`, tool.ErrInvalidArgs.Code, nil},
		{"control char", `"app":"Hjul","text":"a\u0007b"`, tool.ErrInvalidArgs.Code, nil},
		{"submission not open", `"submission":"sub9","text":"hi"`, errReviewNotOpen.Code, func(k *tooltest.Kit) {
			k.ASC.Handle("GET", "/v1/reviewSubmissions/{id}", 200, `{"data":{"type":"reviewSubmissions","id":"sub9","attributes":{"state":"COMPLETE","platform":"IOS"}}}`)
			k.Web.Handle("GET", "/v1/resolutionCenterThreads", 200, reviewThreadsBody)
		}},
		{"thread closed to replies", `"app":"Hjul","text":"hi"`, errReviewNotOpen.Code, func(k *tooltest.Kit) {
			submissionRoutes(k)
			k.Web.Handle("GET", "/v1/resolutionCenterThreads", 200, `{"data":[{"type":"resolutionCenterThreads","id":"th1","attributes":{"canDeveloperAddNote":false}}]}`)
		}},
		{"thread of another submission", `"app":"Hjul","thread":"thX","text":"hi"`, "not_found", func(k *tooltest.Kit) {
			submissionRoutes(k)
			k.Web.Handle("GET", "/v1/resolutionCenterThreads", 200, reviewThreadsBody)
		}},
		{"no thread", `"app":"Hjul","text":"hi"`, "not_found", func(k *tooltest.Kit) {
			submissionRoutes(k)
			k.Web.Handle("GET", "/v1/resolutionCenterThreads", 200, `{"data":[]}`)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := kit(t)
			if c.routes != nil {
				c.routes(k)
			}
			tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "release_review_reply"), k.Env, "{"+c.args+"}"), c.code)
			if n := len(k.Web.Mutations()); n != 0 {
				t.Errorf("mutations = %d", n)
			}
		})
	}
}

func TestReviewMessagesSuggestsReply(t *testing.T) {
	k := kit(t).ReadOnly()
	reviewRoutes(k)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_messages"), k.Env, `{"app":"Hjul"}`))
	var tools []string
	for _, n := range res.Next {
		tools = append(tools, n.Tool)
	}
	if !slices.Contains(tools, "release_review_reply") {
		t.Errorf("next = %v", tools)
	}
}

func TestReviewReplyMeta(t *testing.T) {
	m := mustTool(t, "release_review_reply").Meta()
	if m.Toolset != tool.Web || m.Class != tool.Destructive || m.Confirm != tool.ConfirmByPlan || !m.RequireHuman || len(m.Description) > tool.MaxDescription {
		t.Errorf("meta = %+v", m)
	}
	if mustTool(t, "release_review_reply").ListedIn(tool.ProfileInfo{Name: "ro", KeyKind: tool.KeyTeam}) {
		t.Error("release_review_reply is listed in read-only mode")
	}
}

// A named app with an explicit submission: its state is read, so the reply
// step shows up only while App Review waits.
func TestReviewMessagesExplicitSubmissionSuggestsReply(t *testing.T) {
	for state, want := range map[string]bool{"UNRESOLVED_ISSUES": true, "COMPLETE": false} {
		k := kit(t).ReadOnly()
		k.ASC.Handle("GET", "/v1/reviewSubmissions/{id}", 200, `{"data":{"type":"reviewSubmissions","id":"sub7","attributes":{"state":"`+state+`","platform":"IOS"}}}`)
		k.Web.Handle("GET", "/v1/resolutionCenterThreads", 200, reviewThreadsBody)
		k.Web.Handle("GET", "/v1/resolutionCenterThreads/{id}/resolutionCenterMessages", 200, reviewMessagesBody)
		res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_messages"), k.Env, `{"app":"Hjul","submission":"sub7"}`))
		got := slices.ContainsFunc(res.Next, func(n tool.NextStep) bool { return n.Tool == "release_review_reply" && n.Args["submission"] == "sub7" })
		if got != want {
			t.Errorf("%s: reply suggested = %v, next = %+v", state, got, res.Next)
		}
		if out := tooltest.Decode[reviewMessagesOut](t, res); out.Submission.State != state {
			t.Errorf("%s: submission = %+v", state, out.Submission)
		}
	}
}
