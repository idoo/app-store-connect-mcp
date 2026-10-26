package metadata

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// failingFetcher accepts part PUTs except to URLs containing fail.
type failingFetcher struct{ fail string }

func (f failingFetcher) Fetch(_ context.Context, _, rawURL string, _ http.Header, body io.Reader, _ int64) (*tool.FetchResponse, error) {
	_, _ = io.Copy(io.Discard, body)
	if strings.Contains(rawURL, f.fail) {
		return &tool.FetchResponse{Status: http.StatusForbidden, Header: http.Header{}}, nil
	}
	return &tool.FetchResponse{Status: http.StatusOK, Header: http.Header{}}, nil
}

func TestAppinfoReviewAttachmentPartialReplanSkipsUploaded(t *testing.T) {
	k := attachKit(t)
	for _, n := range []string{"a.pdf", "b.pdf"} {
		if err := k.Files.WriteFile(n, []byte("%PDF "+n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	k.Env.Fetch = failingFetcher{fail: "/b.pdf"}
	k.ASC.HandleFunc(http.MethodPost, "/v1/appStoreReviewAttachments", func(req tool.Request) tooltest.Fixture {
		body := tooltest.JSON(t, req.Body)
		name, id := "a.pdf", "att-a"
		if strings.Contains(body, `"b.pdf"`) {
			name, id = "b.pdf", "att-b"
		}
		return tooltest.Fixture{Status: http.StatusCreated, Body: []byte(`{"data":{"type":"appStoreReviewAttachments","id":"` + id +
			`","attributes":{"fileName":"` + name + `","fileSize":10,"uploadOperations":[{"method":"PUT","url":"https://upload.example.com/` +
			name + `","offset":0,"length":10,"requestHeaders":[]}]}}}`)}
	})
	k.ASC.Handle(http.MethodPatch, "/v1/appStoreReviewAttachments/{id}", http.StatusOK,
		`{"data":{"type":"appStoreReviewAttachments","id":"att-a","attributes":{}}}`)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreReviewAttachments/{id}", http.StatusOK,
		`{"data":{"type":"appStoreReviewAttachments","id":"att-a","attributes":{"assetDeliveryState":{"state":"COMPLETE"}}}}`)
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, `{"review_attachments":{"add":["a.pdf","b.pdf"]}}`))

	res := tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, `{"review_attachments":{"add":["a.pdf","b.pdf"]},"confirm":true}`)
	out := tooltest.JSON(t, res.Data)
	if !strings.Contains(out, `"id":"att-a"`) || !strings.Contains(out, `"file":"a.pdf"`) {
		t.Fatalf("result does not name the uploaded file: %s", out)
	}
	var next *tool.NextStep
	for i := range res.Next {
		if res.Next[i].Tool == "appinfo_update" {
			next = &res.Next[i]
		}
	}
	if next == nil {
		t.Fatalf("no appinfo_update next step: %+v", res.Next)
	}
	if got := tooltest.JSON(t, next.Args["review_attachments"]); got != `{"add":["b.pdf"]}` {
		t.Fatalf("re-run review_attachments = %s, want only b.pdf", got)
	}
}

// authFetcher answers presigned part PUTs and records any Authorization.
type authFetcher struct {
	mu    sync.Mutex
	calls []string
	auth  int
}

func (f *authFetcher) Fetch(_ context.Context, method, rawURL string, header http.Header, body io.Reader, _ int64) (*tool.FetchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if header.Get("Authorization") != "" {
		f.auth++
	}
	_, _ = io.Copy(io.Discard, body)
	f.calls = append(f.calls, method+" "+rawURL)
	return &tool.FetchResponse{Status: http.StatusOK, Header: http.Header{}}, nil
}

func attachKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := kit(t)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreVersions/{id}", http.StatusOK,
		`{"data":{"type":"appStoreVersions","id":"v2","attributes":{"versionString":"1.1"},`+
			`"relationships":{"appStoreReviewDetail":{"data":{"type":"appStoreReviewDetails","id":"rd1"}}}}}`)
	return k
}

func TestAppinfoReviewAttachmentUploadsWithoutAuthorization(t *testing.T) {
	k := attachKit(t)
	content := []byte("%PDF-1.4 demo walkthrough")
	if err := k.Files.WriteFile("review.pdf", content, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &authFetcher{}
	k.Env.Fetch = f
	ops := `[{"method":"PUT","url":"https://upload.example.com/p1","offset":0,"length":25,` +
		`"requestHeaders":[{"name":"Content-Type","value":"application/pdf"},{"name":"Authorization","value":"Bearer leak"}]}]`
	k.ASC.Handle(http.MethodPost, "/v1/appStoreReviewAttachments", http.StatusCreated,
		`{"data":{"type":"appStoreReviewAttachments","id":"att1","attributes":{"fileName":"review.pdf","fileSize":25,"uploadOperations":`+ops+`}}}`)
	k.ASC.Handle(http.MethodPatch, "/v1/appStoreReviewAttachments/{id}", http.StatusOK,
		`{"data":{"type":"appStoreReviewAttachments","id":"att1","attributes":{}}}`)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreReviewAttachments/{id}", http.StatusOK,
		`{"data":{"type":"appStoreReviewAttachments","id":"att1","attributes":{"assetDeliveryState":{"state":"COMPLETE"}}}}`)

	args := `{"review_attachments":{"add":["review.pdf"]}}`
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, args))
	if n := len(k.ASC.Mutations()); n != 0 || len(f.calls) != 0 {
		t.Fatalf("preview mutated: %d asc, %d parts", n, len(f.calls))
	}
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, `{"review_attachments":{"add":["review.pdf"]},"confirm":true}`))
	if f.auth != 0 || len(f.calls) != 1 {
		t.Fatalf("part PUTs = %v, with Authorization = %d", f.calls, f.auth)
	}
	post := tooltest.JSON(t, k.ASC.RequestsTo(http.MethodPost, "/v1/appStoreReviewAttachments")[0].Body)
	for _, want := range []string{`"fileName":"review.pdf"`, `"fileSize":25`, `"appStoreReviewDetail"`, `"id":"rd1"`} {
		if !strings.Contains(post, want) {
			t.Errorf("reserve body %s lacks %s", post, want)
		}
	}
	commit := tooltest.JSON(t, k.ASC.RequestsTo(http.MethodPatch, "/v1/appStoreReviewAttachments/{id}")[0].Body)
	if !strings.Contains(commit, `"uploaded":true`) || !strings.Contains(commit, `"sourceFileChecksum"`) {
		t.Errorf("commit = %s", commit)
	}
	if out := tooltest.JSON(t, res.Data); !strings.Contains(out, `"op":"upload"`) || !strings.Contains(out, `"id":"att1"`) {
		t.Errorf("result = %s", out)
	}
}

func TestAppinfoReviewAttachmentRemoveNeedsConfirm(t *testing.T) {
	k := attachKit(t)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreReviewDetails/{id}/appStoreReviewAttachments", http.StatusOK,
		`{"data":[{"type":"appStoreReviewAttachments","id":"att9","attributes":{"fileName":"old.pdf"}}]}`)
	k.ASC.Handle(http.MethodDelete, "/v1/appStoreReviewAttachments/{id}", http.StatusNoContent, ``)

	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, `{"review_attachments":{"remove":["att9"]}}`))
	if n := len(k.ASC.RequestsTo(http.MethodDelete, "/v1/appStoreReviewAttachments/{id}")); n != 0 {
		t.Fatalf("remove without confirm sent %d DELETEs", n)
	}
	if out := tooltest.JSON(t, res.Data); !strings.Contains(out, `"old.pdf"`) {
		t.Errorf("preview = %s", out)
	}
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, `{"review_attachments":{"remove":["nope"]}}`), "invalid_arguments")
	tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, `{"review_attachments":{"remove":["att9"]},"confirm":true}`))
	if n := len(k.ASC.RequestsTo(http.MethodDelete, "/v1/appStoreReviewAttachments/{id}")); n != 1 {
		t.Fatalf("confirmed remove sent %d DELETEs", n)
	}
}

func TestAppinfoReviewAttachmentRejectsEscapingPath(t *testing.T) {
	k := attachKit(t)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "appinfo_update"), k.Env, `{"review_attachments":{"add":["../../etc/passwd"]}}`), "invalid_arguments")
	if n := len(k.ASC.Mutations()); n != 0 {
		t.Fatalf("mutations = %d", n)
	}
}
