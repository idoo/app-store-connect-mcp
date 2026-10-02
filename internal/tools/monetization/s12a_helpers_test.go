package monetization

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

// s12aTestApp is the app every s12a test resolves.
var s12aTestApp = tool.App{ID: "app1", BundleID: "se.jvcr.hjul", Name: "Hjul", Platforms: []tool.Platform{tool.IOS}}

// s12aKit returns a kit (its FakeASC checks every request against the spec)
// resolving s12aTestApp.
func s12aKit(t *testing.T) *tooltest.Kit {
	t.Helper()
	k := tooltest.New(t)
	k.Resolve.Apps = []tool.App{s12aTestApp}
	return k
}

func s12aTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	tl, ok := tool.Lookup(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tl
}

// s12aLog renders the request log as "METHOD path" lines.
func s12aLog(reqs []tool.Request) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Method + " " + r.Path
	}
	return out
}

// s12aBodyOf decodes a request body into a generic map.
func s12aBodyOf(t *testing.T, r tool.Request) map[string]any {
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

// s12aAttrs returns data.attributes of a request body.
func s12aAttrs(t *testing.T, r tool.Request) map[string]any {
	t.Helper()
	a, _ := s12aBodyOf(t, r)["data"].(map[string]any)["attributes"].(map[string]any)
	return a
}

// s12aRel returns "type/id" of a to-one relationship in a request body.
func s12aRel(t *testing.T, r tool.Request, name string) string {
	t.Helper()
	rels, _ := s12aBodyOf(t, r)["data"].(map[string]any)["relationships"].(map[string]any)
	d, _ := rels[name].(map[string]any)["data"].(map[string]any)
	if d == nil {
		return ""
	}
	return d["type"].(string) + "/" + d["id"].(string)
}

// s12aDoc renders a JSON:API document with data (object or array) and included.
func s12aDoc(data string, included ...string) string {
	s := `{"data":` + data
	if len(included) > 0 {
		s += `,"included":[` + strings.Join(included, ",") + `]`
	}
	return s + `,"links":{}}`
}

func s12aEqual(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

const s12aConflictBody = `{"errors":[{"status":"409","code":"ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE","title":"duplicate","detail":"The product ID has already been used"}]}`
