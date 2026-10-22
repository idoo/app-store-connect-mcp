package release

import (
	"slices"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
	"github.com/idoo/app-store-connect-mcp/internal/tool/tooltest"
)

// placementOf routes loc0's library to one iPhone screenshot placement of
// asset image in state; the asset is PREPARE_FOR_SUBMISSION.
func placementOf(t *testing.T, image, state string) func(k *tooltest.Kit) {
	return placementAsset(t, image, state, "PREPARE_FOR_SUBMISSION")
}

// placementAsset is placementOf with the asset state; "" omits the asset.
func placementAsset(t *testing.T, image, state, assetState string) func(k *tooltest.Kit) {
	return func(k *tooltest.Kit) {
		k.ASC.HandleFunc("GET", "/v1/appStoreVersionLocalizations/{id}/placements", func(req tool.Request) tooltest.Fixture {
			if strings.Split(req.Path, "/")[3] != "loc0" {
				return tooltest.Fixture{Status: 200, Body: []byte(`{"data":[]}`)}
			}
			var inc []jres
			if assetState != "" {
				inc = append(inc, jres{Type: "appAssetLibraryImages", ID: image, Attrs: map[string]any{"fileName": "01.png", "state": assetState}})
			}
			return tooltest.Fixture{Status: 200, Body: []byte(doc(t, []jres{{
				Type: "appAssetLibraryPlacements", ID: "pl1",
				Attrs: map[string]any{"mediaType": "IMAGE", "placementGroup": "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", "placementType": "APP_SCREENSHOT", "state": state},
				Rels:  map[string]any{"image": "appAssetLibraryImages:" + image},
			}}, inc...))}
		})
	}
}

// A slot whose only placement cannot fill it blocks with the reason; an
// unknown placement state is a warn item.
func TestReleaseStatusLibraryBlockerAndWarning(t *testing.T) {
	for _, c := range []struct{ assetState, want string }{
		{"REJECTED", "is REJECTED"}, {"ARCHIVED", "is ARCHIVED"}, {"AWAITING_UPLOAD", "is AWAITING_UPLOAD"}, {"", "no readable asset"},
	} {
		f := readyFixture()
		f.noIPhoneShots = true
		f.library = placementAsset(t, "img1", "ACTIVE", c.assetState)
		out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
		ch := checkByID(t, out, "screenshots")
		if ch.Status != statusBlock || !slices.ContainsFunc(ch.Items, func(s string) bool { return strings.Contains(s, c.want) }) ||
			!slices.ContainsFunc(ch.Items, func(s string) bool { return strings.Contains(s, "no screenshots for APP_IPHONE_67") }) {
			t.Errorf("asset %q: %+v", c.assetState, ch)
		}
	}

	f := readyFixture()
	f.library = placementAsset(t, "img1", "SOMETHING_NEW", "PREPARE_FOR_SUBMISSION")
	out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
	if ch := checkByID(t, out, "screenshots"); ch.Status != statusWarn || !slices.ContainsFunc(ch.Items, func(s string) bool { return strings.Contains(s, "unknown state SOMETHING_NEW") }) {
		t.Errorf("unknown placement state: %+v", ch)
	}
}

// A set screenshot and the placement of the same asset are one record:
// counted and reported once (via the set), and library_plan is suggested.
func TestReleaseStatusDedupesSetAndPlacement(t *testing.T) {
	f := readyFixture()
	f.library = placementOf(t, "s-loc0", "ASSET_PROCESSING") // mirrors set screenshot s-loc0 (COMPLETE)
	out, res := call(t, setup(t, f), `{"app":"Hjul"}`)
	if c := checkByID(t, out, "screenshots"); c.Status != statusOK || len(c.Items) != 0 {
		t.Errorf("same-id placement reported again: %+v", c)
	}
	if !slices.ContainsFunc(res.Next, func(n tool.NextStep) bool { return n.Tool == "library_plan" }) {
		t.Errorf("next = %+v, want library_plan", res.Next)
	}

	f.library = placementOf(t, "img-other", "ASSET_PROCESSING") // a distinct asset still counts
	out, _ = call(t, setup(t, f), `{"app":"Hjul"}`)
	if c := checkByID(t, out, "screenshots"); c.Status != statusWarn {
		t.Errorf("distinct processing placement: %+v", c)
	}

	f.library = nil
	_, res = call(t, setup(t, f), `{"app":"Hjul"}`)
	if slices.ContainsFunc(res.Next, func(n tool.NextStep) bool { return n.Tool == "library_plan" }) {
		t.Errorf("library_plan suggested without placements: %+v", res.Next)
	}
}
