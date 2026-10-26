package reviews

import (
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

type summaryResp struct {
	Summary struct {
		Status    string `json:"status"`
		Note      string `json:"note"`
		Summaries []struct {
			Platform  string            `json:"platform"`
			Territory string            `json:"territory"`
			Text      map[string]string `json:"text"`
		} `json:"summaries"`
	} `json:"summary"`
}

func TestListSummaryPerPlatformUntrusted(t *testing.T) {
	k := listKit(t)
	k.Resolve.Apps[0].Platforms = []tool.Platform{"IOS", "MAC_OS"}
	twoPages(k)
	k.ASC.Handle("GET", "/v1/apps/a1/customerReviewSummarizations?filter[platform]=IOS", 200,
		`{"data":[{"type":"customerReviewSummarizations","id":"s1","attributes":{"platform":"IOS","locale":"en-US","text":"Users love it","createdDate":"2026-10-20T00:00:00Z"},`+
			`"relationships":{"territory":{"data":{"type":"territories","id":"USA"}}}}]}`)
	k.ASC.Handle("GET", "/v1/apps/a1/customerReviewSummarizations?filter[platform]=MAC_OS", 200, `{"data":[]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env, `{"summary":true,"limit":1}`))
	o := tooltest.Decode[summaryResp](t, res)
	if o.Summary.Status != "ok" || len(o.Summary.Summaries) != 1 {
		t.Fatalf("summary = %+v", o.Summary)
	}
	s := o.Summary.Summaries[0]
	if s.Platform != "IOS" || s.Territory != "USA" || s.Text["untrusted"] != "Users love it" {
		t.Fatalf("summary item = %+v", s)
	}
	if n := len(k.ASC.RequestsTo("GET", "/v1/apps/a1/customerReviewSummarizations")); n != 2 {
		t.Fatalf("summary calls = %d, want one per platform", n)
	}
}

func TestListSummaryNoneYet(t *testing.T) {
	k := listKit(t)
	twoPages(k)
	k.ASC.Handle("GET", "/v1/apps/a1/customerReviewSummarizations", 200, `{"data":[]}`)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env, `{"summary":true}`))
	o := tooltest.Decode[summaryResp](t, res)
	if o.Summary.Status != "none_yet" || o.Summary.Note == "" {
		t.Fatalf("summary = %+v", o.Summary)
	}
	if q := k.ASC.RequestsTo("GET", "/v1/apps/a1/customerReviewSummarizations")[0].Query; q.Get("filter[platform]") != "IOS" {
		t.Fatalf("platform filter = %v", q)
	}
}

func TestListSummaryAllAppsInvalid(t *testing.T) {
	k := listKit(t)
	tooltest.MustFail(t, tooltest.Call(t, mustTool(t, "reviews_list"), k.Env, `{"app":"all","summary":true}`), "invalid_arguments")
	if n := len(k.ASC.Requests()); n != 0 {
		t.Fatalf("requests = %d", n)
	}
}
