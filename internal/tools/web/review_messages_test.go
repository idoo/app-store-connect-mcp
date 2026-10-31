package web

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const reviewThreadsBody = `{"data":[{"type":"resolutionCenterThreads","id":"th1",
	"attributes":{"threadType":"REJECTION_REVIEW_SUBMISSION","state":"OPEN","createdDate":"2026-01-02T10:00:00Z"}}]}`

const reviewMessagesBody = `{"data":[
	{"type":"resolutionCenterMessages","id":"m2","attributes":{"createdDate":"2026-01-03T09:00:00Z","messageBody":"<p>Thanks, we fixed it.</p>"},
	 "relationships":{"fromActor":{"data":{"type":"actors","id":"a2"}}}},
	{"type":"resolutionCenterMessages","id":"m1","attributes":{"createdDate":"2026-01-02T10:00:00Z",
	 "messageBody":"Hello,<br><br><b>Guideline 2.1 - Performance</b><p>The app crashed &amp; froze.</p><ul><li>Step one</li><li>Step two</li></ul>See <a href=\"https://developer.apple.com/x\">Testing a Release Build</a>."},
	 "relationships":{"fromActor":{"data":{"type":"actors","id":"a1"}},"rejections":{"data":[{"type":"reviewRejections","id":"r1"}]}}}],
	"included":[
	{"type":"actors","id":"a1","attributes":{"actorType":"APPLE","name":"App Review"}},
	{"type":"actors","id":"a2","attributes":{"actorType":"DEVELOPER"}},
	{"type":"reviewRejections","id":"r1","attributes":{"reasons":[
		{"reasonSection":"2.1","reasonCode":"PERFORMANCE_APP_COMPLETENESS","reasonDescription":"Performance: <i>App Completeness</i>"},
		{"reasonSection":"4.0","reasonDescription":"Design"}]}}]}`

func reviewRoutes(k *tooltest.Kit) {
	k.ASC.Handle("GET", "/v1/reviewSubmissions", 200, `{"data":[
		{"type":"reviewSubmissions","id":"draft","attributes":{"platform":"IOS","state":"READY_FOR_REVIEW"}},
		{"type":"reviewSubmissions","id":"old","attributes":{"platform":"IOS","state":"COMPLETE","submittedDate":"2025-12-01T00:00:00Z"}},
		{"type":"reviewSubmissions","id":"sub1","attributes":{"platform":"IOS","state":"UNRESOLVED_ISSUES","submittedDate":"2026-01-01T00:00:00Z"}}]}`)
	k.Web.Handle("GET", "/v1/resolutionCenterThreads", 200, reviewThreadsBody)
	k.Web.Handle("GET", "/v1/resolutionCenterThreads/{id}/resolutionCenterMessages", 200, reviewMessagesBody)
}

func TestReviewMessagesNewestSubmission(t *testing.T) {
	k := kit(t).ReadOnly()
	reviewRoutes(k)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_messages"), k.Env, `{"app":"Hjul"}`))
	out := tooltest.Decode[reviewMessagesOut](t, res)
	if out.Submission.ID != "sub1" || out.Submission.State != "UNRESOLVED_ISSUES" || len(out.Threads) != 1 {
		t.Fatalf("out = %s", tooltest.JSON(t, out))
	}
	if q := k.Web.RequestsTo("GET", "/v1/resolutionCenterThreads"); len(q) != 1 || q[0].Query.Get("filter[reviewSubmission]") != "sub1" {
		t.Errorf("thread requests = %+v", q)
	}
	if q := k.Web.RequestsTo("GET", "/v1/resolutionCenterThreads/{id}/resolutionCenterMessages"); len(q) != 1 ||
		q[0].Path != "/v1/resolutionCenterThreads/th1/resolutionCenterMessages" || !strings.Contains(q[0].Query.Get("include"), "rejections") {
		t.Errorf("message requests = %+v", q)
	}
	msgs := out.Threads[0].Messages
	if len(msgs) != 2 || msgs[0].Date != "2026-01-02T10:00:00Z" || msgs[0].From != "APPLE" || msgs[0].FromName != "App Review" || msgs[1].From != "DEVELOPER" {
		t.Fatalf("messages = %s", tooltest.JSON(t, msgs))
	}
	want := "Hello,\n\nGuideline 2.1 - Performance\nThe app crashed & froze.\n\n- Step one\n- Step two\nSee Testing a Release Build (https://developer.apple.com/x)."
	if string(msgs[0].Text) != want {
		t.Errorf("text = %q, want %q", msgs[0].Text, want)
	}
	if r := msgs[0].Rejections; len(r) != 2 || r[0].Guideline != "2.1" || r[0].Code != "PERFORMANCE_APP_COMPLETENESS" || r[0].Description != "Performance: App Completeness" {
		t.Errorf("rejections = %+v", r)
	}
	if !slices.Equal(out.Guidelines, []string{"2.1", "4.0"}) {
		t.Errorf("guidelines = %v", out.Guidelines)
	}
	// Reviewer text is third-party: it marshals wrapped.
	b, _ := json.Marshal(res.Data)
	if !strings.Contains(string(b), `"text":{"untrusted":"Hello,`) || !strings.Contains(string(b), `"description":{"untrusted":"Performance`) {
		t.Errorf("not wrapped: %s", b)
	}
}

func TestReviewMessagesExplicitSubmissionNoThread(t *testing.T) {
	k := kit(t).ReadOnly()
	k.Web.Handle("GET", "/v1/resolutionCenterThreads", 200, `{"data":[]}`)
	out := tooltest.Decode[reviewMessagesOut](t, tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "release_review_messages"), k.Env, `{"submission":"sub9"}`)))
	if out.Submission.ID != "sub9" || out.App != nil || len(out.Threads) != 0 || out.Note == "" {
		t.Errorf("out = %s", tooltest.JSON(t, out))
	}
	if n := len(k.ASC.Requests()); n != 0 {
		t.Errorf("public API calls = %d", n)
	}
}

func TestReviewMessagesNoSubmission(t *testing.T) {
	k := kit(t).ReadOnly()
	k.ASC.Handle("GET", "/v1/reviewSubmissions", 200, `{"data":[]}`)
	b := tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "release_review_messages"), k.Env, `{"app":"Hjul"}`), "not_found")
	if len(b.Next) != 1 || b.Next[0].Tool != "release_review_status" {
		t.Errorf("body = %+v", b)
	}
}

func TestReviewMessagesWebSessionRequired(t *testing.T) {
	k := kit(t)
	k.Env.Web = nil
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "release_review_messages"), k.Env, `{"submission":"sub1"}`), tool.ErrWebSessionRequired.Code)
}

func TestHTMLToText(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                        "plain",
		"a&nbsp;b  c\r\n\r\n\r\n\r\nd": "a b c\n\nd",
		`<a href="https://x.test">https://x.test</a>`: "https://x.test",
		"<div>one</div><div>two</div>":                "one\n\ntwo",
	} {
		if got := htmlToText(in); got != want {
			t.Errorf("htmlToText(%q) = %q, want %q", in, got, want)
		}
	}
}
