package release

import (
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func listingSnapshot(keywords string) *statusSnapshot {
	loc := tool.Resource{ID: "loc-ru", Type: "appStoreVersionLocalizations", Attributes: map[string]any{
		"locale":      "ru",
		"description": "Описание",
		"keywords":    keywords,
		"supportUrl":  "https://example.com/support",
	}}
	return &statusSnapshot{
		App:      tool.App{ID: "app-1"},
		Platform: tool.IOS,
		Version: &versionDetail{
			Version:  tool.Resource{ID: "ver-1", Type: "appStoreVersions", Rel: map[string]any{"appStoreVersionLocalizations": []string{"loc-ru"}}},
			Included: []tool.Resource{loc},
		},
		AppInfoLocs: []tool.Resource{{ID: "info-ru", Type: "appInfoLocalizations", Attributes: map[string]any{
			"locale": "ru", "name": "Приложение", "privacyPolicyUrl": "https://example.com/privacy",
		}}},
	}
}

func TestCheckListingKeywordsCountCharacters(t *testing.T) {
	ru := strings.Repeat("ж", 100)
	if len(ru) <= 100 {
		t.Fatalf("fixture must exceed 100 bytes, got %d", len(ru))
	}
	if c := checkListing(listingSnapshot(ru)); c.Status != statusOK {
		t.Fatalf("100-character ru keywords: status %q, items %v; want ok", c.Status, c.Items)
	}
	if c := checkListing(listingSnapshot(ru + "ж")); c.Status != statusBlock || len(c.Items) != 1 {
		t.Fatalf("101-character ru keywords: status %q, items %v; want one blocking item", c.Status, c.Items)
	}
}
