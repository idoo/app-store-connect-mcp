package asc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// PO3: every HTTP attempt of one tool call (a retried 503 plus two pages) is
// reported as _meta requests.apple.
func TestToolCallCountsAppleRequests(t *testing.T) {
	var n atomic.Int32
	var srvURL string
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Query().Get("cursor") == "" {
			_, _ = io.WriteString(w, `{"data":[{"type":"apps","id":"1"}],"links":{"next":"`+srvURL+`/v1/apps?cursor=Mg"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"type":"apps","id":"2"}]}`)
	}, nil)
	srvURL = hs.srv.URL
	tl := tool.New(tool.Meta{
		Name: "asc_client_test_count", Toolset: "core", Title: "Test",
		Description: "Test tool.", Class: tool.Read,
	}, func(ctx context.Context, env *tool.Env, _ struct{}) (*tool.Result, error) {
		if _, err := env.ASC.List(ctx, "/v1/apps", tool.ListOptions{MaxItems: 10}); err != nil {
			return nil, err
		}
		return tool.OK(map[string]any{}), nil
	})
	res := tl.Call(context.Background(), &tool.Env{ASC: hs.c}, json.RawMessage(`{}`))
	if res.IsError {
		t.Fatalf("result = %+v", res)
	}
	if res.Meta.Requests == nil || res.Meta.Requests.Apple != 3 || hs.hits.Load() != 3 {
		t.Fatalf("requests meta = %+v, hits = %d, want 3", res.Meta.Requests, hs.hits.Load())
	}
	// Outside a tool call nothing is counted and nothing panics.
	if _, err := hs.c.Get(context.Background(), "/v1/apps", nil); err != nil {
		t.Fatal(err)
	}
}
