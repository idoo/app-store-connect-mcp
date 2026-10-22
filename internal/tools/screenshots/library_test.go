package screenshots

import (
	"slices"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// livePlacements is loc-en's library in the live shape (2026-10-26): an
// iPhone screenshot placement with its image, plus a header creative.
func livePlacements(t *testing.T) []byte {
	return doc(t, []jres{
		{Type: "appAssetLibraryPlacements", ID: "pl1", Attrs: map[string]any{"mediaType": "IMAGE", "placementGroup": "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", "placementType": "APP_SCREENSHOT", "state": "ACTIVE"}, Rels: map[string]any{"image": "appAssetLibraryImages:img1"}},
		{Type: "appAssetLibraryPlacements", ID: "pl2", Attrs: map[string]any{"mediaType": "IMAGE", "placementGroup": "DEFAULT_PROFILE", "placementType": "PRODUCT_PAGE_HEADER_ASSET", "state": "ACTIVE"}, Rels: map[string]any{"image": "appAssetLibraryImages:img2"}},
	}, jres{Type: "appAssetLibraryImages", ID: "img1", Attrs: map[string]any{"fileName": "01-library.png", "state": "IN_REVIEW"}},
		jres{Type: "appAssetLibraryImages", ID: "img2", Attrs: map[string]any{"fileName": "header.png", "state": "APPROVED"}})
}

func TestListLibrarySource(t *testing.T) {
	k, f := newKit(t)
	f.library = map[string][]byte{"loc-en": livePlacements(t)}
	out := tooltest.Decode[listOut](t, tooltest.MustOK(t, call(t, k, "screenshots_list", `{"locales":["en-US"],"source":"library"}`)))
	if len(out.Locales) != 1 {
		t.Fatalf("locales = %+v", out.Locales)
	}
	l := out.Locales[0]
	if len(l.Library) != 1 || l.Library[0].Group != "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE" || len(l.Library[0].Placements) != 1 {
		t.Fatalf("library = %+v", l.Library)
	}
	if p := l.Library[0].Placements[0]; p.ID != "pl1" || p.AssetState != "IN_REVIEW" || p.FileName != "01-library.png" || p.State != "ACTIVE" {
		t.Errorf("placement = %+v", p)
	}
	if len(l.Creatives) != 1 || l.Creatives[0].Type != "PRODUCT_PAGE_HEADER_ASSET" {
		t.Errorf("creatives = %+v", l.Creatives)
	}
	if len(k.ASC.RequestsTo("GET", "/v1/appStoreVersionLocalizations/{id}/appScreenshotSets")) != 0 || out.Coverage != nil {
		t.Error("source=library read sets or computed coverage")
	}
}

func TestListBothCountsLibraryForCoverage(t *testing.T) {
	k, f := newKit(t)
	f.library = map[string][]byte{"loc-en": livePlacements(t)}
	out := tooltest.Decode[listOut](t, tooltest.MustOK(t, call(t, k, "screenshots_list", `{"locales":["en-US"]}`)))
	if out.Coverage == nil || slices.ContainsFunc(out.Coverage.MissingRequired, func(m missingDisplay) bool {
		return slices.Contains(m.OneOf, applemeta.AppIPhone67)
	}) {
		t.Errorf("iPhone slot filled through the library reported missing: %+v", out.Coverage)
	}

	k, f = newKit(t)
	f.libraryStatus = 403
	out = tooltest.Decode[listOut](t, tooltest.MustOK(t, call(t, k, "screenshots_list", `{"locales":["en-US"]}`)))
	if out.LibraryError != "Asset Library could not be read (HTTP 403)" || len(out.Coverage.MissingRequired) == 0 {
		t.Errorf("403: error=%q coverage=%+v", out.LibraryError, out.Coverage)
	}
}

func TestListRejectsUnknownSource(t *testing.T) {
	k, _ := newKit(t)
	tooltest.MustFail(t, call(t, k, "screenshots_list", `{"source":"web"}`), "invalid_arguments")
}
