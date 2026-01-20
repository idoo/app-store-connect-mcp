package resolve

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/hamn/internal/tool"
	"github.com/idoo/hamn/internal/tool/tooltest"
)

// res is a JSON:API resource for fixtures.
type res struct {
	Type       string         `json:"type"`
	ID         string         `json:"id"`
	Attributes map[string]any `json:"attributes"`
}

// body renders a collection document.
func body(t *testing.T, items ...res) string {
	t.Helper()
	if items == nil {
		items = []res{}
	}
	b, err := json.Marshal(map[string]any{"data": items})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// single renders a single-resource document.
func single(t *testing.T, item res) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"data": item})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func app(id, name, bundle, sku string) res {
	return res{Type: "apps", ID: id, Attributes: map[string]any{"name": name, "bundleId": bundle, "sku": sku, "primaryLocale": "en-US"}}
}

func ver(id, platform, vs, state, created string) res {
	return res{Type: "appStoreVersions", ID: id, Attributes: map[string]any{
		"platform": platform, "versionString": vs, "appVersionState": state, "createdDate": created,
	}}
}

// serveVersions emulates GET /v1/apps/{id}/appStoreVersions, applying the
// platform, versionString and appVersionState filters to byApp.
func serveVersions(t *testing.T, f *tooltest.FakeASC, byApp map[string][]res) {
	t.Helper()
	f.HandleFunc(http.MethodGet, "/v1/apps/{id}/appStoreVersions", func(req tool.Request) tooltest.Fixture {
		appID := strings.Split(strings.Trim(req.Path, "/"), "/")[2]
		var out []res
		for _, v := range byApp[appID] {
			if !filterOK(req.Query, "filter[platform]", v.Attributes["platform"]) ||
				!filterOK(req.Query, "filter[versionString]", v.Attributes["versionString"]) ||
				!filterOK(req.Query, "filter[appVersionState]", v.Attributes["appVersionState"]) {
				continue
			}
			out = append(out, v)
		}
		return tooltest.Fixture{Status: http.StatusOK, Body: []byte(body(t, out...))}
	})
}

func filterOK(q url.Values, key string, val any) bool {
	want := q.Get(key)
	if want == "" {
		return true
	}
	s, _ := val.(string)
	return slices.Contains(strings.Split(want, ","), s)
}

// wantErr checks err against the expected kind: "" (nil), "notfound" or
// "ambiguous", and returns the next-step tool name (if any).
func wantErr(t *testing.T, err error, kind string) (nextTool string, candidates int) {
	t.Helper()
	var nf *tool.ErrNotFound
	var amb *tool.ErrAmbiguous
	switch kind {
	case "":
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return "", 0
	case "notfound":
		if !errors.As(err, &nf) {
			t.Fatalf("want *ErrNotFound, got %T %v", err, err)
		}
		if len(nf.Next) > 0 {
			return nf.Next[0].Tool, 0
		}
		return "", 0
	case "ambiguous":
		if !errors.As(err, &amb) {
			t.Fatalf("want *ErrAmbiguous, got %T %v", err, err)
		}
		if len(amb.Next) > 0 {
			return amb.Next[0].Tool, len(amb.Candidates)
		}
		return "", len(amb.Candidates)
	}
	t.Fatalf("bad kind %q", kind)
	return "", 0
}
