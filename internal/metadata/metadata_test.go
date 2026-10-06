package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

const versionLocs = `{"data":[
 {"type":"appStoreVersionLocalizations","id":"V-EN","attributes":{"locale":"en-US","description":"Old desc","keywords":"a,b","whatsNew":"Fixes","supportUrl":"https://x.test/s","marketingUrl":null}},
 {"type":"appStoreVersionLocalizations","id":"V-DE","attributes":{"locale":"de-DE","description":"Alt","keywords":"x"}}
]}`

const appInfoLocs = `{"data":[
 {"type":"appInfoLocalizations","id":"I-EN","attributes":{"locale":"en-US","name":"Hamn","subtitle":"Ship apps"}},
 {"type":"appInfoLocalizations","id":"I-DE","attributes":{"locale":"de-DE","name":"Hamn"}}
]}`

func ptr(s string) *string { return &s }

func newRemote(t *testing.T) (*tooltest.FakeASC, *Listing) {
	t.Helper()
	f := tooltest.NewFakeASC(t).
		Handle(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", 200, versionLocs).
		Handle(http.MethodGet, "/v1/appInfos/{id}/appInfoLocalizations", 200, appInfoLocs)
	l, err := Pull(context.Background(), f, "VER", "INFO")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	return f, l
}

func TestPull(t *testing.T) {
	_, l := newRemote(t)
	if got := l.SortedLocales(); !slices.Equal(got, []string{"de-DE", "en-US"}) {
		t.Fatalf("locales = %v", got)
	}
	en := l.Locales["en-US"]
	for k, want := range map[string]string{"description": "Old desc", "keywords": "a,b", "name": "Hamn", "subtitle": "Ship apps", "whatsNew": "Fixes"} {
		if v, ok := en.Get(k); !ok || v != want {
			t.Errorf("en-US %s = %q,%v want %q", k, v, ok, want)
		}
	}
	if _, ok := en.Get("marketingUrl"); ok {
		t.Error("null marketingUrl must be absent")
	}
	if l.LocalizationID(LevelVersion, "de-DE") != "V-DE" || l.LocalizationID(LevelAppInfo, "en-US") != "I-EN" {
		t.Errorf("ids = %+v", l.App)
	}
}

func TestAttrStringUnwrapsUntrusted(t *testing.T) {
	for _, v := range []any{"x", tool.Untrusted("x"), map[string]any{"untrusted": "x"}} {
		if s, ok := attrString(v); !ok || s != "x" {
			t.Errorf("attrString(%#v) = %q,%v", v, s, ok)
		}
	}
	if _, ok := attrString(nil); ok {
		t.Error("nil must be absent")
	}
}

func TestDiffUpdatesCreatesAndKeepsOmitted(t *testing.T) {
	_, remote := newRemote(t)
	desired := &Listing{Locales: map[string]*LocaleFields{
		"en_us":  {Description: ptr("New desc"), Keywords: ptr("a,b")}, // keywords identical → skipped
		"French": {Name: ptr("Hamn"), Description: ptr("Desc FR")},
	}}
	p, err := Diff(remote, desired, DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if p.Counts != (Counts{Updates: 1, Creates: 2, Unchanged: 3}) {
		t.Errorf("counts = %+v", p.Counts)
	}
	for _, c := range p.Changes {
		if c.Op == OpDelete {
			t.Fatalf("omission deleted %+v", c)
		}
	}
	up := p.Changes[0]
	if up.Locale != "en-US" || up.Op != OpUpdate || up.ID != "V-EN" || len(up.Fields) != 1 || *up.Fields[0].Old != "Old desc" {
		t.Errorf("update = %+v", up)
	}
	cr := p.Changes[2]
	if cr.Locale != "fr-FR" || cr.Op != OpCreate || cr.Level != LevelVersion {
		t.Errorf("create = %+v", cr)
	}
	if !hasIssue(p.Warnings, IssueIncompleteLocale, "keywords") {
		t.Errorf("warnings = %+v", p.Warnings)
	}
	if p.Fingerprint() == "" {
		t.Error("empty fingerprint")
	}
}

func TestDiffRemoveDeletesOnlyListed(t *testing.T) {
	_, remote := newRemote(t)
	p, err := Diff(remote, &Listing{}, DiffOptions{Remove: []string{"de", "ja"}})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	var got []string
	for _, c := range p.Changes {
		got = append(got, string(c.Op)+" "+c.Locale+" "+c.ID)
	}
	if want := []string{"delete de-DE I-DE", "delete de-DE V-DE"}; !slices.Equal(got, want) {
		t.Errorf("changes = %v want %v", got, want)
	}
	if !hasIssue(p.Warnings, IssueRemoveMissing, "") || p.Counts.Deletes != 2 || p.Counts.Unchanged != 2 {
		t.Errorf("plan = %+v", p)
	}
	_, err = Diff(remote, &Listing{Locales: map[string]*LocaleFields{"de-DE": {Name: ptr("x")}}}, DiffOptions{Remove: []string{"de-DE"}})
	if !errors.Is(err, ErrInvalidListing) {
		t.Errorf("remove+desired conflict: err = %v", err)
	}
}

func TestDiffValidation(t *testing.T) {
	_, remote := newRemote(t)
	desired := &Listing{Locales: map[string]*LocaleFields{
		"en-US": {Name: ptr(strings.Repeat("n", 31)), Keywords: ptr(strings.Repeat("ü", 51)), SupportURL: ptr("ftp://x"), WhatsNew: ptr("hi")},
		"xx-YY": {Description: ptr("d")},
	}}
	p, err := Diff(remote, desired, DiffOptions{FirstVersion: true})
	if !errors.Is(err, ErrInvalidListing) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range [][2]string{{IssueTooLong, "name"}, {IssueTooLong, "keywords"}, {IssueInvalidURL, "supportUrl"}, {IssueUnknownLocale, ""}} {
		if !hasIssue(p.Errors, want[0], want[1]) {
			t.Errorf("missing error %v in %+v", want, p.Errors)
		}
	}
	if !hasIssue(p.Warnings, IssueWhatsNewFirst, "whatsNew") {
		t.Errorf("warnings = %+v", p.Warnings)
	}
	for _, c := range p.Changes {
		for _, f := range c.Fields {
			if f.Field == "whatsNew" {
				t.Error("whatsNew kept on first version")
			}
		}
	}
}

func TestDiffAliasCollisionAndUnpulledLevel(t *testing.T) {
	f := tooltest.NewFakeASC(t).Handle(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", 200, versionLocs)
	remote, err := Pull(context.Background(), f, "VER", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Diff(remote, &Listing{Locales: map[string]*LocaleFields{
		"en-US": {WhatsNew: ptr("New")}, "en_US": {WhatsNew: ptr("New")}, "de": {Name: ptr("x")},
	}}, DiffOptions{})
	if err == nil || !hasIssue(p.Errors, IssueDuplicateLocale, "") || !hasIssue(p.Errors, IssueLevelUnavailable, "") {
		t.Errorf("err=%v errors=%+v", err, p.Errors)
	}
}

func hasIssue(is []Issue, code, field string) bool {
	return slices.ContainsFunc(is, func(i Issue) bool { return i.Code == code && (field == "" || i.Field == field) })
}

// runWrite runs fn inside a write-enabled tool call so ctx carries the
// write capability, as metadata_apply's handler does.
func runWrite(t *testing.T, k *tooltest.Kit, fn func(ctx context.Context)) {
	t.Helper()
	tl := tool.New(tool.Meta{
		Name: "metadata_test_apply", Toolset: tool.Metadata, Title: "test",
		Description: "test harness", Class: tool.Write,
	}, func(ctx context.Context, _ *tool.Env, _ struct{}) (*tool.Result, error) {
		fn(ctx)
		return nil, nil
	})
	tooltest.MustOK(t, tooltest.Call(t, tl, k.Env, ""))
}

func TestApplyRequestLog(t *testing.T) {
	k := tooltest.New(t)
	k.ASC.Handle(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", 200, versionLocs).
		Handle(http.MethodGet, "/v1/appInfos/{id}/appInfoLocalizations", 200, appInfoLocs).
		Handle(http.MethodPatch, "/v1/appStoreVersionLocalizations/{id}", 200, `{"data":{"type":"appStoreVersionLocalizations","id":"V-EN"}}`).
		Handle(http.MethodPatch, "/v1/appInfoLocalizations/{id}", 200, `{"data":{"type":"appInfoLocalizations","id":"I-EN"}}`).
		Handle(http.MethodPost, "/v1/appStoreVersionLocalizations", 201, `{"data":{"type":"appStoreVersionLocalizations","id":"V-FR"}}`).
		Handle(http.MethodPost, "/v1/appInfoLocalizations", 201, `{"data":{"type":"appInfoLocalizations","id":"I-FR"}}`).
		Handle(http.MethodDelete, "/v1/appStoreVersionLocalizations/{id}", 204, "").
		Handle(http.MethodDelete, "/v1/appInfoLocalizations/{id}", 404, `{"errors":[{"status":"404","code":"NOT_FOUND"}]}`)
	remote, err := Pull(context.Background(), k.ASC, "VER", "INFO")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Diff(remote, &Listing{Locales: map[string]*LocaleFields{
		"en-US": {Subtitle: ptr("Ship faster"), WhatsNew: ptr("Bug fixes")},
		"fr-FR": {Name: ptr("Hamn"), Description: ptr("Desc"), Keywords: ptr("k")},
	}}, DiffOptions{Remove: []string{"de-DE"}})
	if err != nil {
		t.Fatal(err)
	}
	var results []ItemResult
	var calls int
	runWrite(t, k, func(ctx context.Context) {
		results, err = Apply(ctx, k.ASC, p, Targets{VersionID: "VER", AppInfoID: "INFO"}, func(done, total int) {
			calls++
			if total != 6 || done != calls {
				t.Errorf("progress %d/%d at call %d", done, total, calls)
			}
		})
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if calls != 6 {
		t.Errorf("progress calls = %d", calls)
	}
	tooltest.AssertGolden(t, "testdata/golden/apply.json", map[string]any{"results": results, "requests": mutationLog(t, k.ASC)})
}

func TestApplyDuplicateLocaleIsAlready(t *testing.T) {
	k := tooltest.New(t)
	k.ASC.Handle(http.MethodPost, "/v1/appStoreVersionLocalizations", 409,
		`{"errors":[{"status":"409","code":"ENTITY_ERROR.ATTRIBUTE.INVALID.DUPLICATE","title":"dup","source":{"pointer":"/data/attributes/locale"}}]}`).
		Handle(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations?filter[locale]=fr-FR", 200,
			`{"data":[{"type":"appStoreVersionLocalizations","id":"V-FR","attributes":{"locale":"fr-FR"}}]}`).
		Handle(http.MethodPatch, "/v1/appStoreVersionLocalizations/{id}", 200, `{"data":{"type":"appStoreVersionLocalizations","id":"V-FR"}}`)
	p := &Plan{Changes: []Change{{Locale: "fr-FR", Level: LevelVersion, Op: OpCreate, Fields: []FieldChange{{Field: "description", New: "D"}}}}}
	var results []ItemResult
	var err error
	runWrite(t, k, func(ctx context.Context) {
		results, err = Apply(ctx, k.ASC, p, Targets{VersionID: "VER"}, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := results[0]; r.Outcome != OutcomeAlready || r.ID != "V-FR" {
		t.Errorf("result = %+v", r)
	}
	if n := len(k.ASC.RequestsTo(http.MethodPatch, "/v1/appStoreVersionLocalizations/V-FR")); n != 1 {
		t.Errorf("patches = %d", n)
	}
}

func TestApplyRefusesInvalidPlanAndMissingParent(t *testing.T) {
	ctx := context.Background()
	if _, err := Apply(ctx, nil, &Plan{Errors: []Issue{{Code: IssueTooLong}}}, Targets{}, nil); !errors.Is(err, ErrInvalidListing) {
		t.Errorf("err = %v", err)
	}
	p := &Plan{Changes: []Change{{Locale: "fr-FR", Level: LevelAppInfo, Op: OpCreate}}}
	if _, err := Apply(ctx, nil, p, Targets{VersionID: "V"}, nil); !errors.Is(err, tool.ErrInvalidArgs) {
		t.Errorf("err = %v", err)
	}
}

func TestApplyWithoutWriteCapabilityFails(t *testing.T) {
	f := tooltest.NewFakeASC(t).Handle(http.MethodDelete, "/v1/appInfoLocalizations/{id}", 204, "")
	p := &Plan{Changes: []Change{{Locale: "de-DE", Level: LevelAppInfo, Op: OpDelete, ID: "I-DE"}}}
	res, err := Apply(context.Background(), f, p, Targets{}, nil)
	if err != nil || res[0].Outcome != OutcomeError || !strings.Contains(res[0].Error, "writes") {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

// mutationLog returns the non-GET requests in a deterministic order.
func mutationLog(t *testing.T, f *tooltest.FakeASC) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range f.Mutations() {
		b, err := json.Marshal(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var body any
		_ = json.Unmarshal(b, &body)
		out = append(out, map[string]any{"method": r.Method, "path": r.Path, "body": body})
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := json.Marshal(out[i])
		b, _ := json.Marshal(out[j])
		return string(a) < string(b)
	})
	return out
}
