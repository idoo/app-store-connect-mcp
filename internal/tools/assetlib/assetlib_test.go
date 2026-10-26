package assetlib

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/applemeta"
)

func TestCovers(t *testing.T) {
	for _, tc := range []struct {
		group, typ, state string
		d                 applemeta.DisplayType
		want              bool
	}{
		{"MAC_PROFILE", TypeScreenshot, "ACTIVE", applemeta.AppDesktop, true}, // live shape (Lesoma macOS 3.6)
		{"IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", TypeScreenshot, "ACTIVE", applemeta.AppIPhone67, true},
		{"IPHONE_HOME_BUTTON_35_PROFILE", TypeScreenshot, "ACTIVE", applemeta.AppIPhone67, false},
		{"IPHONE_HOME_BUTTON_35_PROFILE", TypeScreenshot, "ACTIVE", applemeta.AppIPhone65, false},
		{"IPHONE_FACE_ID_MEDIUM_PROFILE", TypeScreenshot, "ACTIVE", applemeta.AppIPhone61, true},
		{"IPHONE_DUO_PROFILE", TypeScreenshot, "ACTIVE", applemeta.AppIPhoneDuo, true},
		{"IMESSAGE_IPHONE_DUO_PROFILE", TypeScreenshot, "ACTIVE", applemeta.AppIPhoneDuo, false},
		{"IMESSAGE_IPHONE_DUO_PROFILE", "IMESSAGE_APP_SCREENSHOT", "ACTIVE", applemeta.AppIPhoneDuo, false},
		{"IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", "PRODUCT_PAGE_HEADER_ASSET", "ACTIVE", applemeta.AppIPhone67, false},
		{"IPAD_13_PROFILE", TypeScreenshot, "FAILED", applemeta.AppIPadPro3Gen129, false},
		{"IPAD_13_PROFILE", TypeScreenshot, "ASSET_PROCESSING", applemeta.AppIPadPro3Gen129, false},
		{"IPAD_13_PROFILE", TypeScreenshot, "PARENT_PREPARE_FOR_SUBMISSION", applemeta.AppIPadPro3Gen129, true},
		{"IPAD_13_PROFILE", TypeScreenshot, "PARENT_APPROVED", applemeta.AppIPadPro3Gen129, true},
		{"IPHONE_FUTURE_PROFILE", TypeScreenshot, "ACTIVE", applemeta.AppIPhone67, false},
	} {
		p := Placement{ID: "p1", Group: tc.group, Type: tc.typ, State: tc.state, AssetID: "a1", AssetState: "APPROVED"}
		if got := Covers([]Placement{p}, tc.d); got != tc.want {
			t.Errorf("Covers(%s %s %s, %s) = %v, want %v", tc.group, tc.typ, tc.state, tc.d, got, tc.want)
		}
	}
	if got := UnknownGroups([]Placement{{Group: "IPHONE_FUTURE_PROFILE", Type: TypeScreenshot}, {Group: "MAC_PROFILE", Type: TypeScreenshot}}); len(got) != 1 || got[0] != "IPHONE_FUTURE_PROFILE" {
		t.Errorf("unknown groups = %v", got)
	}
}

// B2/B3: only usable assets cover; every other asset state (or a missing
// asset) yields a reason, and an unknown placement state warns.
func TestCoversAssetStates(t *testing.T) {
	base := Placement{ID: "p1", Group: "IPHONE_DYNAMIC_ISLAND_LARGE_PROFILE", Type: TypeScreenshot, State: "ACTIVE", AssetID: "a1"}
	for _, tc := range []struct {
		assetState string
		noAsset    bool
		want       bool
	}{
		{"APPROVED", false, true},
		{"PREPARE_FOR_SUBMISSION", false, true},
		{"IN_REVIEW", false, true},
		{"ACCEPTED", false, true},
		{"COMPLETE", false, true},
		{"REJECTED", false, false},
		{"ARCHIVED", false, false},
		{"AWAITING_UPLOAD", false, false},
		{"UPLOAD_COMPLETE", false, false},
		{"FAILED", false, false},
		{"", true, false},
	} {
		p := base
		p.AssetState = tc.assetState
		if tc.noAsset {
			p.AssetID = ""
		}
		got := Covers([]Placement{p}, applemeta.AppIPhone67)
		if got != tc.want {
			t.Errorf("asset %q (missing=%v): covers = %v, want %v", tc.assetState, tc.noAsset, got, tc.want)
		}
		if reason := p.Blocker(); (reason == "") != tc.want {
			t.Errorf("asset %q: blocker %q", tc.assetState, reason)
		}
	}
	if w := base.Warning(); w != "" {
		t.Errorf("ACTIVE warns: %q", w)
	}
	for _, st := range []string{"PARENT_IN_REVIEW", "PARENT_SOMETHING_NEW", "ASSET_PROCESSING", "FAILED"} {
		p := base
		p.State = st
		if w := p.Warning(); w != "" {
			t.Errorf("%s warns: %q", st, w)
		}
	}
	odd := base
	odd.State, odd.AssetState = "PAUSED", "APPROVED"
	if w := odd.Warning(); !strings.Contains(w, "unknown state PAUSED") {
		t.Errorf("unknown placement state warning = %q", w)
	}
}

// Every APP_SCREENSHOT group in Apple's ref data is mapped; IMESSAGE_* and
// DEFAULT_PROFILE groups are never mapped.
func TestGroupTableMatchesRefData(t *testing.T) {
	raw, err := os.ReadFile("testdata/refdata.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Data []struct {
			Attributes struct {
				Features []struct {
					PlacementPolicies []struct {
						PlacementType string `json:"placementType"`
						GroupLimits   []struct {
							GroupIDs []string `json:"groupIds"`
						} `json:"groupLimits"`
					} `json:"placementPolicies"`
				} `json:"features"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range doc.Data[0].Attributes.Features {
		for _, p := range f.PlacementPolicies {
			for _, gl := range p.GroupLimits {
				for _, g := range gl.GroupIDs {
					_, ok := Displays(g)
					switch {
					case p.PlacementType == TypeScreenshot:
						seen[g] = true
						if !ok {
							t.Errorf("APP_SCREENSHOT group %s not mapped", g)
						}
					case (strings.HasPrefix(g, "IMESSAGE_") || g == "DEFAULT_PROFILE") && ok:
						t.Errorf("%s must not fill an app screenshot slot", g)
					}
				}
			}
		}
	}
	if len(seen) != len(groupDisplays) {
		t.Errorf("ref data has %d APP_SCREENSHOT groups, table %d", len(seen), len(groupDisplays))
	}
}
