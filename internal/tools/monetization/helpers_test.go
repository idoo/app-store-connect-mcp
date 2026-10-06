package monetization

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// testApp is the app every monetization test resolves.
var testApp = tool.App{ID: "app1", BundleID: "se.jvcr.hjul", Name: "Hjul", Platforms: []tool.Platform{tool.IOS}}

// newKit returns a kit (its FakeASC checks every request against the spec)
// resolving testApp.
func newKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t)
	k.Resolve.Apps = []tool.App{testApp}
	return k
}

func mustTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tl
}

// requestLog renders the request log as "METHOD path" lines.
func requestLog(reqs []tool.Request) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Method + " " + r.Path
	}
	return out
}

// bodyOf decodes a request body into a generic map.
func bodyOf(t *testing.T, r tool.Request) map[string]any {
	t.Helper()
	b, err := json.Marshal(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// reqAttrs returns data.attributes of a request body.
func reqAttrs(t *testing.T, r tool.Request) map[string]any {
	t.Helper()
	a, _ := bodyOf(t, r)["data"].(map[string]any)["attributes"].(map[string]any)
	return a
}

// reqRel returns "type/id" of a to-one relationship in a request body.
func reqRel(t *testing.T, r tool.Request, name string) string {
	t.Helper()
	rels, _ := bodyOf(t, r)["data"].(map[string]any)["relationships"].(map[string]any)
	d, _ := rels[name].(map[string]any)["data"].(map[string]any)
	if d == nil {
		return ""
	}
	return d["type"].(string) + "/" + d["id"].(string)
}

// jsonapiDoc renders a JSON:API document with data (object or array) and included.
func jsonapiDoc(data string, included ...string) string {
	s := `{"data":` + data
	if len(included) > 0 {
		s += `,"included":[` + strings.Join(included, ",") + `]`
	}
	return s + `,"links":{}}`
}

func equalLines(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

const conflictBody = `{"errors":[{"status":"409","code":"ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE","title":"duplicate","detail":"The product ID has already been used"}]}`
