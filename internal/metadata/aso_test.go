package metadata

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func issue(is []Issue, code string) *Issue {
	for i := range is {
		if is[i].Code == code {
			return &is[i]
		}
	}
	return nil
}

func TestDiffNewLocaleWithoutNameIsError(t *testing.T) {
	_, remote := newRemote(t)
	p, err := Diff(remote, &Listing{Locales: map[string]*LocaleFields{
		"fr-FR": {Description: ptr("Desc"), Keywords: ptr("k")},
	}}, DiffOptions{})
	if err == nil || !hasIssue(p.Errors, IssueIncompleteLocale, "name") {
		t.Fatalf("err=%v errors=%+v", err, p.Errors)
	}
	// An existing locale needs no name.
	if _, err := Diff(remote, &Listing{Locales: map[string]*LocaleFields{"de-DE": {Description: ptr("Neu")}}}, DiffOptions{}); err != nil {
		t.Errorf("existing locale: %v", err)
	}
}

func TestDiffPromoOnlyOnLiveVersion(t *testing.T) {
	f := tooltest.NewFakeASC(t).Handle(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", 200, versionLocs)
	remote, err := Pull(context.Background(), f, "VER", "") // no editable app info
	if err != nil {
		t.Fatal(err)
	}
	p, err := Diff(remote, &Listing{Locales: map[string]*LocaleFields{
		"en-US": {Name: ptr("Other"), Description: ptr("New desc"), Keywords: ptr("a,b"), PromotionalText: ptr("Sale")},
		"fr-FR": {Name: ptr("Hamn"), PromotionalText: ptr("Promo")},
	}}, DiffOptions{PromoOnly: true, AppInfoState: "READY_FOR_DISTRIBUTION"})
	if err != nil {
		t.Fatalf("Diff: %v (errors %+v)", err, p.Errors)
	}
	if len(p.Changes) != 1 {
		t.Fatalf("changes = %+v", p.Changes)
	}
	c := p.Changes[0]
	if c.Locale != "en-US" || c.Op != OpUpdate || len(c.Fields) != 1 || c.Fields[0].Field != "promotionalText" {
		t.Errorf("change = %+v", c)
	}
	var locked []string
	for _, w := range p.Warnings {
		if w.Code == IssueLockedOnLive {
			locked = append(locked, w.Locale+": "+w.Message)
		}
	}
	if len(locked) != 2 || !strings.Contains(locked[0], "name, description") || strings.Contains(locked[0], "keywords") {
		t.Errorf("locked warnings = %v (unchanged keywords must not be listed)", locked)
	}
}

func TestDiffLockedAppInfoExplainsState(t *testing.T) {
	f := tooltest.NewFakeASC(t).Handle(http.MethodGet, "/v1/appStoreVersions/{id}/appStoreVersionLocalizations", 200, versionLocs)
	remote, err := Pull(context.Background(), f, "VER", "")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := Diff(remote, &Listing{Locales: map[string]*LocaleFields{"en-US": {Subtitle: ptr("New")}}}, DiffOptions{AppInfoState: "WAITING_FOR_REVIEW"})
	if is := issue(p.Errors, IssueLevelUnavailable); is == nil || !strings.Contains(is.Message, "WAITING_FOR_REVIEW") {
		t.Errorf("errors = %+v", p.Errors)
	}
}

func TestDiffKeywordHygiene(t *testing.T) {
	_, remote := newRemote(t) // en-US name "Hamn", subtitle "Ship apps"
	p, err := Diff(remote, &Listing{Locales: map[string]*LocaleFields{
		"en-US": {Keywords: ptr("hamn, release,,Release,store,ship")},
	}}, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{IssueKeywordsSpaces, IssueKeywordsEmpty, IssueKeywordsDup, IssueKeywordsInName} {
		if issue(p.Warnings, code) == nil {
			t.Errorf("missing %s in %+v", code, p.Warnings)
		}
	}
	if m := issue(p.Warnings, IssueKeywordsSpaces).Message; !strings.Contains(m, `"hamn,release,store,ship"`) || !strings.Contains(m, "saves 10 bytes") {
		t.Errorf("spaces message = %q", m)
	}
	if m := issue(p.Warnings, IssueKeywordsInName).Message; !strings.Contains(m, "hamn, ship") {
		t.Errorf("in-name message = %q", m)
	}
	clean, _ := Diff(remote, &Listing{Locales: map[string]*LocaleFields{"en-US": {Keywords: ptr("release,store")}}}, DiffOptions{})
	if len(clean.Warnings) != 0 {
		t.Errorf("clean keywords warned: %+v", clean.Warnings)
	}
}

func TestDiffWarnsFoldedLocale(t *testing.T) {
	_, remote := newRemote(t)
	l, err := (&Listing{Locales: map[string]*LocaleFields{"de-CH": {Description: ptr("Neu")}, "en_US": {Description: ptr("New")}}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Diff(remote, l, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var mapped []string
	for _, w := range p.Warnings {
		if w.Code == IssueLocaleMapped {
			mapped = append(mapped, w.Message)
		}
	}
	if len(mapped) != 1 || !strings.HasPrefix(mapped[0], "de-CH → de-DE") {
		t.Errorf("locale_mapped = %v", mapped)
	}
}

func TestApplyClearSendsNull(t *testing.T) {
	k := tooltest.New(t)
	k.ASC.Handle(http.MethodPatch, "/v1/appStoreVersionLocalizations/{id}", 200, `{"data":{"type":"appStoreVersionLocalizations","id":"V-EN"}}`)
	p := &Plan{Changes: []Change{{
		Locale: "en-US", Level: LevelVersion, Op: OpUpdate, ID: "V-EN",
		Fields: []FieldChange{{Field: "marketingUrl", New: ""}, {Field: "description", New: "D"}},
	}}}
	var err error
	runWrite(t, k, func(ctx context.Context) { _, err = Apply(ctx, k.ASC, p, Targets{VersionID: "VER"}, nil) })
	if err != nil {
		t.Fatal(err)
	}
	reqs := k.ASC.Mutations()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	attrs := reqs[0].Body.(map[string]any)["data"].(map[string]any)["attributes"].(map[string]any)
	if v, ok := attrs["marketingUrl"]; !ok || v != nil {
		t.Errorf("marketingUrl = %#v, want null", v)
	}
	if attrs["description"] != "D" {
		t.Errorf("description = %#v", attrs["description"])
	}
}

func TestReadersReportUnknownKeys(t *testing.T) {
	files := tooltest.NewFakeFiles(t)
	writeFile(t, files.Root, "fl/en-US/description.txt", "d")
	writeFile(t, files.Root, "fl/en-US/promo.txt", "typo")
	writeFile(t, files.Root, "fl/copyright.txt", "2026")
	l, err := FromFastlane(files, "fl")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Notes) != 2 || !strings.Contains(l.Notes[0].Message, "promo.txt") || !strings.Contains(l.Notes[1].Message, "appinfo_update") {
		t.Errorf("fastlane notes = %+v", l.Notes)
	}

	writeFile(t, files.Root, "store.config.json", `{"apple":{"copyright":"2026","info":{"en-US":{"title":"Hamn","promotionalText":"x","whatsNew":"y"}}}}`)
	l, err = FromExpo(files, "store.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, n := range l.Notes {
		if n.Code == IssueUnknownSourceKey {
			msgs = append(msgs, n.Message)
		}
	}
	if len(msgs) != 2 || !strings.Contains(msgs[0], "{promotionalText,whatsNew}") || !strings.Contains(msgs[1], "apple.copyright") {
		t.Errorf("expo notes = %v", msgs)
	}
	// Notes surface as plan warnings.
	_, remote := newRemote(t)
	p, err := Diff(remote, l, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(p.Warnings, func(i Issue) bool { return i.Code == IssueUnknownSourceKey }) {
		t.Errorf("warnings = %+v", p.Warnings)
	}
}
