package metadata

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFromFastlane(t *testing.T) {
	files := tooltest.NewFakeFiles(t)
	writeFile(t, files.Root, "metadata/en-US/description.txt", "English desc\n")
	writeFile(t, files.Root, "metadata/en-US/release_notes.txt", "Fixes\n\n")
	writeFile(t, files.Root, "metadata/de-DE/name.txt", "Hamn DE")
	writeFile(t, files.Root, "metadata/ja/keywords.txt", "a,b\n")
	writeFile(t, files.Root, "metadata/default/support_url.txt", "https://x.test/s\n")
	writeFile(t, files.Root, "metadata/default/privacy_url.txt", "https://x.test/p\n")
	writeFile(t, files.Root, "metadata/de-DE/privacy_url.txt", "https://x.test/de\n")
	writeFile(t, files.Root, "metadata/copyright.txt", "2026 Ivan\n")
	writeFile(t, files.Root, "metadata/review_information/notes.txt", "n\n")
	writeFile(t, files.Root, "metadata/en-US/unknown.txt", "ignored\n")

	l, err := FromFastlane(files, "metadata")
	if err != nil {
		t.Fatalf("FromFastlane: %v", err)
	}
	want := map[string]*LocaleFields{
		"en-US": {Description: ptr("English desc"), WhatsNew: ptr("Fixes"), SupportURL: ptr("https://x.test/s"), PrivacyPolicyURL: ptr("https://x.test/p")},
		"de-DE": {Name: ptr("Hamn DE"), SupportURL: ptr("https://x.test/s"), PrivacyPolicyURL: ptr("https://x.test/de")},
		"ja":    {Keywords: ptr("a,b"), SupportURL: ptr("https://x.test/s"), PrivacyPolicyURL: ptr("https://x.test/p")},
	}
	if diff := cmp.Diff(want, l.Locales); diff != "" {
		t.Errorf("listing (-want +got):\n%s", diff)
	}
}

func TestFromFastlaneUnknownLocaleDir(t *testing.T) {
	files := tooltest.NewFakeFiles(t)
	writeFile(t, files.Root, "metadata/klingon/name.txt", "x")
	if _, err := FromFastlane(files, "metadata"); err == nil {
		t.Fatal("want error for unknown locale dir")
	}
}

func TestToFastlaneRoundTrip(t *testing.T) {
	files := tooltest.NewFakeFiles(t)
	in := &Listing{Locales: map[string]*LocaleFields{
		"en-US": {
			Name: ptr("Hamn"), Subtitle: ptr("Ship"), Description: ptr("Line 1\nLine 2"), Keywords: ptr("a,b"),
			MarketingURL: ptr("https://x.test/m"), PromotionalText: ptr("Promo"), SupportURL: ptr("https://x.test/s"),
			WhatsNew: ptr("Fixes"), PrivacyPolicyURL: ptr("https://x.test/p"), PrivacyPolicyText: ptr("tv"),
		},
		"fr-FR": {Description: ptr("FR")},
	}}
	written, conflicts, err := ToFastlane(files, "out", in)
	if err != nil {
		t.Fatalf("ToFastlane: %v", err)
	}
	if len(written) != 11 || len(conflicts) != 0 {
		t.Errorf("wrote %d files: %v, conflicts %v", len(written), written, conflicts)
	}
	out, err := FromFastlane(files, "out")
	if err != nil {
		t.Fatalf("FromFastlane: %v", err)
	}
	if diff := cmp.Diff(in.Locales, out.Locales); diff != "" {
		t.Errorf("round trip (-in +out):\n%s", diff)
	}
	if _, _, err := ToFastlane(files, "out", &Listing{Locales: map[string]*LocaleFields{"English": {}}}); err == nil {
		t.Error("want error for non-ASC locale key")
	}
	// A second export with an edited file keeps the edit.
	writeFile(t, files.Root, "out/fr-FR/description.txt", "local edit\n")
	written, conflicts, err = ToFastlane(files, "out", in)
	if err != nil || len(written) != 0 || len(conflicts) != 1 || conflicts[0] != filepath.Join("out", "fr-FR", "description.txt") {
		t.Errorf("re-export written=%v conflicts=%v err=%v", written, conflicts, err)
	}
	if b, _ := os.ReadFile(filepath.Join(files.Root, "out/fr-FR/description.txt")); string(b) != "local edit\n" {
		t.Errorf("edited file overwritten: %q", b)
	}
}

func TestFromExpo(t *testing.T) {
	files := tooltest.NewFakeFiles(t)
	writeFile(t, files.Root, "store.config.json", `{"configVersion":0,"apple":{"copyright":"x","info":{
	 "en-US":{"title":"Hamn","subtitle":"Ship","description":"D","keywords":["a","b"],"releaseNotes":"R","promoText":"P",
	  "marketingUrl":"https://x.test/m","supportUrl":"https://x.test/s","privacyPolicyUrl":"https://x.test/p","privacyChoicesUrl":"https://x.test/c"},
	 "pt_BR":{"description":"Desc BR","keywords":"x,y"}}}}`)
	l, err := FromExpo(files, "store.config.json")
	if err != nil {
		t.Fatalf("FromExpo: %v", err)
	}
	want := map[string]*LocaleFields{
		"en-US": {
			Name: ptr("Hamn"), Subtitle: ptr("Ship"), Description: ptr("D"), Keywords: ptr("a,b"), WhatsNew: ptr("R"),
			PromotionalText: ptr("P"), MarketingURL: ptr("https://x.test/m"), SupportURL: ptr("https://x.test/s"),
			PrivacyPolicyURL: ptr("https://x.test/p"), PrivacyChoicesURL: ptr("https://x.test/c"),
		},
		"pt-BR": {Description: ptr("Desc BR"), Keywords: ptr("x,y")},
	}
	if diff := cmp.Diff(want, l.Locales); diff != "" {
		t.Errorf("listing (-want +got):\n%s", diff)
	}
	writeFile(t, files.Root, "bad.json", `{"apple":{"info":{"en-US":{"title":3}}}}`)
	if _, err := FromExpo(files, "bad.json"); err == nil {
		t.Error("want error for non-string title")
	}
}

func TestFromJSON(t *testing.T) {
	l, err := FromJSON([]byte(`{"locales":{"en_us":{"whatsNew":"N"},"German":{"name":"H"}}}`))
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	want := map[string]*LocaleFields{"en-US": {WhatsNew: ptr("N")}, "de-DE": {Name: ptr("H")}}
	if diff := cmp.Diff(want, l.Locales); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if bare, err := FromJSON([]byte(`{"en-US":{"whatsNew":"N"}}`)); err != nil || len(bare.Locales) != 1 {
		t.Errorf("bare map: %v %+v", err, bare)
	}
	for _, bad := range []string{`{"en-US":{"title":"x"}}`, `{"locales":{},"x":1}`, `{"zz-ZZ":{}}`, `[]`} {
		if _, err := FromJSON([]byte(bad)); err == nil {
			t.Errorf("FromJSON(%s): want error", bad)
		}
	}
}
