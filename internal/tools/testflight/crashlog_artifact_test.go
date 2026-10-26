package testflight

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

type crashArtifacts struct{ got map[string][]byte }

func (a *crashArtifacts) Put(_ context.Context, name, mime string, b []byte) (tool.Link, error) {
	a.got[name] = b
	return tool.Link{URI: "app-store-connect-mcp://artifacts/" + name, Name: name, MIMEType: mime, Size: int64(len(b))}, nil
}

func crashLogKit(t *testing.T) (*tooltest.Kit, string) {
	t.Helper()
	k := feedbackKit(t)
	full := strings.Repeat("x", feedbackMaxCrashBytes+10)
	k.ASC.Handle(http.MethodGet, "/v1/betaFeedbackCrashSubmissions/{id}/crashLog", http.StatusOK,
		`{"data":{"type":"betaCrashLogs","id":"cl1","attributes":{"logText":"`+full+`"}}}`)
	return k, full
}

func TestFeedbackCrashLogLinkedAsArtifact(t *testing.T) {
	k, full := crashLogKit(t)
	art := &crashArtifacts{got: map[string][]byte{}}
	k.Env.Artifacts = art
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_feedback_list"), k.Env,
		`{"kind":"crashes","include_crash_logs":true}`))
	d := tooltest.Decode[struct {
		Items []struct {
			ID        string `json:"id"`
			Truncated bool   `json:"crash_log_truncated"`
			Resource  string `json:"crash_log_resource"`
			CrashLog  *struct {
				Untrusted string `json:"untrusted"`
			} `json:"crash_log"`
		} `json:"items"`
	}](t, res)
	if len(d.Items) == 0 || len(res.Links) == 0 {
		t.Fatalf("items = %+v, links = %+v", d.Items, res.Links)
	}
	it := d.Items[0]
	if !it.Truncated || it.CrashLog == nil || len(it.CrashLog.Untrusted) > feedbackMaxCrashBytes || it.Resource != res.Links[0].URI {
		t.Fatalf("item = %+v", it)
	}
	if got := string(art.got["crash-"+it.ID+".txt"]); got != full {
		t.Fatalf("artifact has %d bytes, want %d", len(got), len(full))
	}
	if len(res.Meta.Warnings) != 0 {
		t.Errorf("warnings = %v", res.Meta.Warnings)
	}
}

func TestFeedbackCrashLogWithoutArtifactsWarns(t *testing.T) {
	k, _ := crashLogKit(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "testflight_feedback_list"), k.Env,
		`{"kind":"crashes","include_crash_logs":true}`))
	js := tooltest.JSON(t, res.Data)
	if len(res.Links) != 0 || strings.Contains(js, "crash_log_resource") || !strings.Contains(js, `"crash_log_truncated":true`) {
		t.Fatalf("links = %+v, data = %s", res.Links, js)
	}
	if w := strings.Join(res.Meta.Warnings, "|"); !strings.Contains(w, "could not be stored as resources") {
		t.Errorf("warnings = %q", w)
	}
}
