package screenshots

import (
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// A set screenshot and the library placement of the same asset are one
// record: listed once, and the library write path is suggested.
func TestListBothDedupesSameAssetID(t *testing.T) {
	k, f := newKit(t)
	id := f.addShot("loc-en", "APP_IPHONE_67", "01.png", "abc")
	f.library = map[string][]byte{"loc-en": doc(t, []jres{
		{Type: "appAssetLibraryPlacements", ID: "pl1", Attrs: map[string]any{"mediaType": "IMAGE", "placementGroup": "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", "placementType": "APP_SCREENSHOT", "state": "ACTIVE"}, Rels: map[string]any{"image": "appAssetLibraryImages:" + id}},
		{Type: "appAssetLibraryPlacements", ID: "pl2", Attrs: map[string]any{"mediaType": "IMAGE", "placementGroup": "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", "placementType": "APP_SCREENSHOT", "state": "ACTIVE"}, Rels: map[string]any{"image": "appAssetLibraryImages:img-only"}},
	}, jres{Type: "appAssetLibraryImages", ID: id, Attrs: map[string]any{"fileName": "01.png", "state": "APPROVED"}},
		jres{Type: "appAssetLibraryImages", ID: "img-only", Attrs: map[string]any{"fileName": "02.png", "state": "APPROVED"}})}
	res := tooltest.MustOK(t, call(t, k, "screenshots_list", `{"locales":["en-US"]}`))
	out := tooltest.Decode[listOut](t, res)
	l := out.Locales[0]
	shots := 0
	for _, s := range l.ScreenshotSets {
		shots += len(s.Shots)
	}
	lib := 0
	for _, g := range l.Library {
		for _, p := range g.Placements {
			lib++
			if p.AssetID == id {
				t.Errorf("set screenshot %s listed again as placement %s", id, p.ID)
			}
		}
	}
	if shots != 1 || lib != 1 || l.LibraryDeduped != 1 {
		t.Errorf("shots=%d library=%d deduped=%d", shots, lib, l.LibraryDeduped)
	}
	if len(res.Next) == 0 || res.Next[len(res.Next)-1].Tool != "library_plan" {
		t.Errorf("next = %+v, want library_plan", res.Next)
	}
}
