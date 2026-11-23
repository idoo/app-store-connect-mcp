package developer

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

type searchOut struct {
	Count      int `json:"count"`
	Operations []struct {
		ID       string `json:"id"`
		Group    string `json:"group"`
		Read     bool   `json:"read"`
		Verified string `json:"verified"`
	} `json:"operations"`
	Groups []string `json:"groups"`
}

type describeOut struct {
	ID       string `json:"id"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Read     bool   `json:"read"`
	Verified string `json:"verified"`
	Call     struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	} `json:"call"`
	SourceNote string `json:"source_note"`
}

func TestAPISearch(t *testing.T) {
	k := tooltest.New(t)
	got := tooltest.Decode[searchOut](t, tooltest.MustOK(t, call(t, k, "developer_api_search", `{"query":"revoke key"}`)))
	if got.Count == 0 || got.Count != len(got.Operations) || got.Operations[0].ID != "keys.revoke" || got.Operations[0].Read || got.Operations[0].Verified != "" || got.Groups != nil {
		t.Fatalf("revoke key = %+v", got)
	}
	got = tooltest.Decode[searchOut](t, tooltest.MustOK(t, call(t, k, "developer_api_search", `{"group":"keys","limit":2}`)))
	if got.Count != 2 || got.Operations[0].Group != "keys" || got.Groups != nil {
		t.Fatalf("group keys = %+v", got)
	}
	got = tooltest.Decode[searchOut](t, tooltest.MustOK(t, call(t, k, "developer_api_search", `{}`)))
	if got.Count != 10 || len(got.Groups) == 0 {
		t.Fatalf("empty = %+v", got)
	}
	tooltest.MustFail(t, call(t, k, "developer_api_search", `{"limit":31}`), tool.ErrInvalidArgs.Code)
	if n := len(k.DevSite.Calls()); n != 0 {
		t.Fatalf("calls = %d", n)
	}
}

func describe(t *testing.T, k *tooltest.Kit, args string) describeOut {
	t.Helper()
	d := tooltest.Decode[describeOut](t, tooltest.MustOK(t, call(t, k, "developer_api_describe", args)))
	// The note says whether the operation ever answered live, and when.
	if (d.Verified == "") != strings.Contains(d.SourceNote, "not yet verified") || (d.Verified != "" && !strings.HasSuffix(d.SourceNote, d.Verified)) {
		t.Fatalf("describe %s = %+v", args, d)
	}
	return d
}

// replay sends a described call with its placeholders replaced.
func replay(t *testing.T, k *tooltest.Kit, d describeOut, fill map[string]string) *tool.Result {
	t.Helper()
	args := d.Call.Arguments
	args["team_id"] = team
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for from, to := range fill {
		s = strings.ReplaceAll(s, from, to)
	}
	return call(t, k, d.Call.Tool, s)
}

func TestAPIDescribedReadsReachTheSite(t *testing.T) {
	k := tooltest.New(t).WebSession()
	k.DevSite.HandleAPIJSON("GET", "v1/bundleIds", `{"data":[]}`)
	k.DevSite.HandleAPIJSON("POST", "QH65B2/account/auth/key/get", `{"resultCode":0,"keys":[]}`)

	v1 := describe(t, k, `{"operation":"services_ids.list"}`)
	if v1.Call.Tool != "developer_api_get" || !v1.Read {
		t.Fatalf("v1 = %+v", v1)
	}
	tooltest.MustOK(t, replay(t, k, v1, nil))

	form := describe(t, k, `{"operation":"K2"}`)
	if form.Call.Tool != "developer_api_get" {
		t.Fatalf("form = %+v", form)
	}
	tooltest.MustOK(t, replay(t, k, form, map[string]string{`\u003ckeyId\u003e`: "K1"}))

	calls := callsOf(k, "api")
	if len(calls) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	if c := calls[0]; c.Method != v1.Method || c.Path != v1.Path || c.Query != "filter%5Bplatform%5D=SERVICES" {
		t.Errorf("v1 call = %+v", c)
	}
	if c := calls[1]; c.Method != form.Method || c.Path != form.Path || c.Form != "keyId=K1" || c.TeamID != team {
		t.Errorf("form call = %+v", c)
	}
}

func TestAPIDescribedWriteStopsAtPreview(t *testing.T) {
	k := tooltest.New(t).WebSession()
	d := describe(t, k, `{"operation":"J4","id":"B1"}`)
	if d.ID != "bundle_ids.delete" || d.Call.Tool != "developer_api_request" || d.Call.Arguments["path"] != "v1/bundleIds/B1" || d.Call.Arguments["method"] != "DELETE" {
		t.Fatalf("described = %+v", d)
	}
	res := tooltest.MustOK(t, replay(t, k, d, nil))
	if tooltest.Decode[map[string]any](t, res)["status"] != tool.ErrNeedsConfirm.Code || len(k.DevSite.Calls()) != 0 {
		t.Fatalf("res = %s calls = %d", tooltest.JSON(t, res.Data), len(k.DevSite.Calls()))
	}

	r := describe(t, k, `{"operation":"keys.revoke"}`)
	res = tooltest.MustOK(t, replay(t, k, r, map[string]string{`\u003ckeyId\u003e`: "K1"}))
	if tooltest.Decode[map[string]any](t, res)["status"] != tool.ErrNeedsConfirm.Code || len(k.DevSite.Calls()) != 0 {
		t.Fatalf("revoke res = %s", tooltest.JSON(t, res.Data))
	}
}

func TestAPIDescribeUnknown(t *testing.T) {
	k := tooltest.New(t)
	b := tooltest.MustFail(t, call(t, k, "developer_api_describe", `{"operation":"teleport widgets"}`), tool.ErrInvalidArgs.Code)
	if len(b.Next) == 0 || b.Next[0].Tool != "developer_api_search" || b.Next[0].Args["query"] != "teleport widgets" {
		t.Fatalf("next = %+v", b.Next)
	}
}
