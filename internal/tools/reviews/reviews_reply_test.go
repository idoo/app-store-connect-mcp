package reviews

import (
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// PI5: obfuscated and lookalike contacts and ids are flagged, and the warning
// names the original substring (not an ASCII-only tail like "vil.com").
func TestReplyContactsObfuscation(t *testing.T) {
	tests := []struct {
		body       string
		links, ids []string
	}{
		{"Visit \u0435vil.com today", []string{"\u0435vil.com"}, nil}, // Cyrillic е
		{"Write to ivan at example dot com", []string{"ivan at example dot com"}, nil},
		{"Mail ivan\uff20example\uff0ecom please", []string{"ivan\uff20example\uff0ecom"}, nil},
		{"See evil[.]example for more", []string{"evil[.]example"}, nil},
		{"ivan(at)example(dot)com", []string{"ivan(at)example(dot)com"}, nil},
		{"Go to evil dot com", []string{"evil dot com"}, nil},
		{"Ｅｖｉｌ．ｃｏｍ", []string{"Ｅｖｉｌ．ｃｏｍ"}, nil},
		{"Domain \U0001D41E\U0001D42F\U0001D422\U0001D425.com", []string{"\U0001D41E\U0001D42F\U0001D422\U0001D425.com"}, nil},
		{"Issuer 69a6de70-03db-47e3-e053-5b8c7c11a4d1 ok", nil, []string{"69a6de70-03db-47e3-e053-5b8c7c11a4d1"}},
		{"Key 2X9R4HXF34 is ours", nil, []string{"2X9R4HXF34"}},
		// Plain replies stay quiet: versions, prose, all-caps words.
		{"Thanks! Fixed in 2.1.0, look at the new screen. EXCELLENT stuff", nil, nil},
	}
	for _, tt := range tests {
		links, ids := replyContacts([]replyItem{{Body: tt.body}})
		if !slices.Equal(links, tt.links) || !slices.Equal(ids, tt.ids) {
			t.Errorf("%q: links %q ids %q, want %q %q", tt.body, links, ids, tt.links, tt.ids)
		}
	}
}

// PI5 end to end: the homoglyph domain and a key id appear verbatim in the
// preview warnings.
func TestReplyHomoglyphWarningNamesOriginal(t *testing.T) {
	args := `{"app":"a1","replies":[{"review_id":"r4","body":"Visit \u0435vil.com, key 2X9R4HXF34"}]}`
	k := replyKit(t)
	res := tooltest.MustOK(t, tooltest.Call(t, mustTool(t, "reviews_reply"), k.Env, args))
	p := tooltest.Decode[struct {
		Preview tool.PlanPreview `json:"preview"`
	}](t, res).Preview
	want := []string{
		"Reply contains link/email: \"\u0435vil.com\" — confirm it is yours",
		"Reply contains an id or API key id: \"2X9R4HXF34\" — confirm it is meant to be public",
	}
	if !slices.Equal(p.Warnings, want) {
		t.Fatalf("warnings = %q", p.Warnings)
	}
	if strings.Contains(strings.Join(p.Warnings, ""), `"vil.com"`) {
		t.Fatal("truncated domain reported")
	}
}
