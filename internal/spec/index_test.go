package spec

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

// Counts logged by the generator for Apple spec 4.5.1.
const (
	wantPaths = 1001
	wantOps   = 1303
)

func load(t *testing.T) *Index {
	t.Helper()
	x, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return x
}

func TestIndexCounts(t *testing.T) {
	x := load(t)
	if got := x.Version(); got != "4.5.1" {
		t.Errorf("Version = %q, want 4.5.1", got)
	}
	if got := x.PathCount(); got != wantPaths {
		t.Errorf("PathCount = %d, want %d", got, wantPaths)
	}
	ops := x.Operations()
	if len(ops) != wantOps {
		t.Errorf("ops = %d, want %d", len(ops), wantOps)
	}
	paths := map[string]bool{}
	for _, op := range ops {
		paths[op.Path] = true
	}
	if len(paths) != wantPaths {
		t.Errorf("distinct op paths = %d, want %d", len(paths), wantPaths)
	}
}

func TestEmbeddedIndexSize(t *testing.T) {
	const maxGz = 1536 << 10 // 1.5 MB
	fi, err := os.Stat("index.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > maxGz {
		t.Errorf("index.json.gz = %d bytes, cap %d", fi.Size(), maxGz)
	}
}

func TestSearchRanking(t *testing.T) {
	x := load(t)
	tests := []struct {
		query string
		want  string
		topN  int
	}{
		{"list apps", "apps_getCollection", 1},
		{"apps_getCollection", "apps_getCollection", 1},
		{"create app store version", "appStoreVersions_createInstance", 3},
		{"customer review response", "customerReviewResponses_createInstance", 5},
		{"list beta groups", "betaGroups_getCollection", 1},
		{"delete beta tester", "betaTesters_deleteInstance", 3},
		{"sales reports", "salesReports_getCollection", 3},
		{"app events", "apps_appEvents_getToManyRelated", 3},
		{"app events", "appEvents_getInstance", 3},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			hits := x.Search(tt.query, 10)
			for i, h := range hits {
				if h.ID == tt.want {
					if i >= tt.topN {
						t.Errorf("%s ranked %d, want top %d", tt.want, i+1, tt.topN)
					}
					return
				}
			}
			t.Errorf("%s not in results: %v", tt.want, ids(hits))
		})
	}
}

func TestSearchLimitsAndEmpty(t *testing.T) {
	x := load(t)
	if got := x.Search("   ", 5); got != nil {
		t.Errorf("empty query returned %d hits", len(got))
	}
	if got := len(x.Search("app", 0)); got != defaultLimit {
		t.Errorf("default limit = %d, want %d", got, defaultLimit)
	}
	if got := len(x.Search("app", 500)); got != maxLimit {
		t.Errorf("capped limit = %d, want %d", got, maxLimit)
	}
}

func ids(hits []tool.SearchHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.ID
	}
	return out
}

func TestDescribeCreateVersion(t *testing.T) {
	x := load(t)
	d, err := x.Describe("appStoreVersions_createInstance")
	if err != nil {
		t.Fatal(err)
	}
	if d.Method != http.MethodPost || d.Path != "/v1/appStoreVersions" || d.BodyType != "appStoreVersions" {
		t.Fatalf("unexpected op: %+v", d.Operation)
	}
	want := map[string]any{"data": map[string]any{
		"type":       "appStoreVersions",
		"attributes": map[string]any{"platform": "IOS", "versionString": placeholderString},
		"relationships": map[string]any{
			"app": map[string]any{"data": map[string]any{"type": "apps", "id": placeholderID}},
		},
	}}
	if diff := cmp.Diff(want, d.BodySkeleton); diff != "" {
		t.Errorf("skeleton (-want +got):\n%s", diff)
	}
	if d.Sensitive {
		t.Error("create version must not be sensitive")
	}
	var release *tool.Param
	for i := range d.Params {
		if d.Params[i].Name == "data.attributes.releaseType" {
			release = &d.Params[i]
		}
	}
	if release == nil || !slices.Contains(release.Enum, "AFTER_APPROVAL") {
		t.Errorf("releaseType body param missing or without enum: %+v", release)
	}
	// The skeleton must itself validate.
	if err := x.Validate(tool.Request{Method: http.MethodPost, Path: d.Path, Body: d.BodySkeleton}); err != nil {
		t.Errorf("skeleton does not validate: %v", err)
	}
}

func TestDescribeFieldsIncludesAndErrors(t *testing.T) {
	x := load(t)
	d, err := x.Describe("apps_getCollection")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(d.Fields["apps"], "bundleId") {
		t.Errorf("fields[apps] lacks bundleId: %v", d.Fields["apps"])
	}
	if !slices.Contains(d.Includes, "appStoreVersions") {
		t.Errorf("includes lack appStoreVersions: %v", d.Includes)
	}
	for _, p := range d.Params {
		if strings.HasPrefix(p.Name, "fields[") || p.Name == "include" {
			t.Errorf("param %q should be folded into Fields/Includes", p.Name)
		}
	}
	if _, err := x.Describe("nope_getCollection"); !errors.Is(err, tool.ErrInvalidArgs) {
		t.Errorf("unknown id error = %v, want ErrInvalidArgs", err)
	}
	if d, _ := x.Describe("betaGroups_betaTesters_createToManyRelationship"); d == nil || d.BodySkeleton == nil {
		t.Error("linkage op has no skeleton")
	}
	ex, _ := x.describe(x.byID["apps_appEvents_getToManyRelated"]).Example.(map[string]any)
	if ex["path"] != "/v1/apps/<id>/appEvents" || ex["fields"] == nil || ex["query"] == nil || ex["method"] != nil {
		t.Errorf("GET example = %v, want concrete path with fields and limit", ex)
	}
}

func TestMatch(t *testing.T) {
	x := load(t)
	tests := []struct {
		method, path, want string
	}{
		{"GET", "/v1/apps", "apps_getCollection"},
		{"get", "/v1/apps/123", "apps_getInstance"},
		{"GET", "/v1/apps/123?limit=1", "apps_getInstance"},
		{"GET", "/v1/apps/123/appStoreVersions", "apps_appStoreVersions_getToManyRelated"},
		{"PATCH", "/v1/builds/abc", "builds_updateInstance"},
		{"POST", "/v1/betaGroups/g1/relationships/betaTesters", "betaGroups_betaTesters_createToManyRelationship"},
		{"DELETE", "/v1/apps/123", ""},
		{"GET", "/v1/nothing", ""},
		{"GET", "/v1/apps//appStoreVersions", ""},
		{"GET", "v1/apps", ""},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			op, ok := x.Match(tt.method, tt.path)
			if ok != (tt.want != "") || op.ID != tt.want {
				t.Errorf("Match = %q,%v want %q", op.ID, ok, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	x := load(t)
	versionBody := func(attrs map[string]any, rels map[string]any) map[string]any {
		data := map[string]any{"type": "appStoreVersions", "attributes": attrs}
		if rels != nil {
			data["relationships"] = rels
		}
		return map[string]any{"data": data}
	}
	appRel := map[string]any{"app": map[string]any{"data": map[string]any{"type": "apps", "id": "1"}}}
	tests := []struct {
		name    string
		req     tool.Request
		problem string // substring; empty means valid
	}{
		{"list apps", tool.Request{Method: "GET", Path: "/v1/apps", Query: url.Values{"limit": {"10"}, "fields[apps]": {"name,bundleId"}}}, ""},
		{"inline query", tool.Request{Method: "GET", Path: "/v1/apps?filter[bundleId]=a.b&sort=-name"}, ""},
		{"cursor allowed", tool.Request{Method: "GET", Path: "/v1/apps", Query: url.Values{"cursor": {"x"}}}, ""},
		{"unknown query", tool.Request{Method: "GET", Path: "/v1/apps", Query: url.Values{"filter[nope]": {"1"}}}, `unknown query parameter "filter[nope]"`},
		{"bad sort enum", tool.Request{Method: "GET", Path: "/v1/apps", Query: url.Values{"sort": {"color"}}}, `sort: "color" is not one of`},
		{"bad fields", tool.Request{Method: "GET", Path: "/v1/apps", Query: url.Values{"fields[apps]": {"name,colour"}}}, `"colour" is not one of`},
		{"bad limit type", tool.Request{Method: "GET", Path: "/v1/apps", Query: url.Values{"limit": {"ten"}}}, "not an integer"},
		{"missing required query", tool.Request{Method: "GET", Path: "/v1/salesReports"}, `missing required query parameter "filter[frequency]"`},
		{"wrong method", tool.Request{Method: "PUT", Path: "/v1/apps"}, "method PUT is not defined"},
		{"unknown path", tool.Request{Method: "GET", Path: "/v9/rockets"}, "no operation matches"},
		{"create ok", tool.Request{Method: "POST", Path: "/v1/appStoreVersions", Body: versionBody(map[string]any{"platform": "IOS", "versionString": "1.0", "releaseType": "MANUAL"}, appRel)}, ""},
		{"create bad enum", tool.Request{Method: "POST", Path: "/v1/appStoreVersions", Body: versionBody(map[string]any{"platform": "ANDROID", "versionString": "1.0"}, appRel)}, "data.attributes.platform: ANDROID is not one of"},
		{"create missing attr", tool.Request{Method: "POST", Path: "/v1/appStoreVersions", Body: versionBody(map[string]any{"platform": "IOS"}, appRel)}, "missing required attribute data.attributes.versionString"},
		{"create missing rel", tool.Request{Method: "POST", Path: "/v1/appStoreVersions", Body: versionBody(map[string]any{"platform": "IOS", "versionString": "1"}, nil)}, "missing required relationship data.relationships.app"},
		{"create unknown attr", tool.Request{Method: "POST", Path: "/v1/appStoreVersions", Body: versionBody(map[string]any{"platform": "IOS", "versionString": "1", "colour": "red"}, appRel)}, "unknown attribute data.attributes.colour"},
		{"create wrong type", tool.Request{Method: "POST", Path: "/v1/appStoreVersions", Body: map[string]any{"data": map[string]any{"type": "apps"}}}, `data.type must be "appStoreVersions"`},
		{"rel wrong type", tool.Request{Method: "POST", Path: "/v1/appStoreVersions", Body: versionBody(map[string]any{"platform": "IOS", "versionString": "1"}, map[string]any{"app": map[string]any{"data": map[string]any{"type": "builds", "id": "1"}}})}, `type must be "apps"`},
		{"missing body", tool.Request{Method: "POST", Path: "/v1/appStoreVersions"}, "requires a request body"},
		{"raw body invalid json", tool.Request{Method: "POST", Path: "/v1/appStoreVersions", RawBody: []byte("{"), ContentType: "application/json"}, "not valid JSON"},
		{"update ok", tool.Request{Method: "PATCH", Path: "/v1/builds/b1", Body: map[string]any{"data": map[string]any{"type": "builds", "id": "b1", "attributes": map[string]any{"expired": true}}}}, ""},
		{"update id mismatch", tool.Request{Method: "PATCH", Path: "/v1/builds/b1", Body: map[string]any{"data": map[string]any{"type": "builds", "id": "b2"}}}, `does not match the path id "b1"`},
		{"update missing id", tool.Request{Method: "PATCH", Path: "/v1/builds/b1", Body: map[string]any{"data": map[string]any{"type": "builds"}}}, "data.id is required"},
		{"linkage ok", tool.Request{Method: "POST", Path: "/v1/betaGroups/g/relationships/betaTesters", Body: map[string]any{"data": []any{map[string]any{"type": "betaTesters", "id": "t"}}}}, ""},
		{"linkage not array", tool.Request{Method: "POST", Path: "/v1/betaGroups/g/relationships/betaTesters", Body: map[string]any{"data": map[string]any{"type": "betaTesters", "id": "t"}}}, "must be an array"},
		{"get with body", tool.Request{Method: "GET", Path: "/v1/apps", Body: map[string]any{"data": 1}}, "takes no request body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := x.Validate(tt.req)
			if tt.problem == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var ve *tool.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error = %v, want *ValidationError", err)
			}
			if !slices.ContainsFunc(ve.Problems, func(p string) bool { return strings.Contains(p, tt.problem) }) {
				t.Errorf("problems %q lack %q", ve.Problems, tt.problem)
			}
		})
	}
}

func TestValidationErrorCarriesSkeleton(t *testing.T) {
	x := load(t)
	err := x.Validate(tool.Request{Method: "POST", Path: "/v1/appStoreVersions", Body: map[string]any{"data": map[string]any{"type": "x"}}})
	var ve *tool.ValidationError
	if !errors.As(err, &ve) || ve.Skeleton == nil || ve.Skeleton.ID != "appStoreVersions_createInstance" {
		t.Fatalf("want skeleton for appStoreVersions_createInstance, got %v", err)
	}
}

func TestEnumLookupAndFreeText(t *testing.T) {
	x := load(t)
	tests := []struct {
		op, pointer, want string
	}{
		{"appStoreVersions_createInstance", "/data/attributes/platform", "IOS"},
		{"appStoreVersions_createInstance", "/data/type", "appStoreVersions"},
		{"appStoreVersions_createInstance", "/data/relationships/app", "apps"},
		{"apps_getCollection", "sort", "-name"},
		{"apps_getCollection", "fields[apps]", "bundleId"},
	}
	for _, tt := range tests {
		if got := x.EnumLookup(tt.op, tt.pointer); !slices.Contains(got, tt.want) {
			t.Errorf("EnumLookup(%s,%s) = %v, want contains %q", tt.op, tt.pointer, got, tt.want)
		}
	}
	if got := x.EnumLookup("apps_getCollection", "/data/attributes/x"); got != nil {
		t.Errorf("no-body lookup = %v", got)
	}
	if got := x.EnumLookup("missing", "sort"); got != nil {
		t.Errorf("unknown op lookup = %v", got)
	}
	free := []struct{ typ, attr string }{
		{"customerReviews", "body"},
		{"customerReviews", "title"},
		{"appStoreVersionLocalizations", "whatsNew"},
		{"appStoreVersionLocalizations", "description"},
		{"betaFeedbackScreenshotSubmissions", "comment"},
	}
	for _, f := range free {
		if !slices.Contains(x.FreeTextAttributes(f.typ), f.attr) {
			t.Errorf("FreeTextAttributes(%s) = %v, want %s", f.typ, x.FreeTextAttributes(f.typ), f.attr)
		}
	}
	if slices.Contains(x.FreeTextAttributes("appStoreVersionLocalizations"), "supportUrl") {
		t.Error("URLs are not free text")
	}
}
