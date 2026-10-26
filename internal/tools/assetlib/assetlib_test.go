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
		{"IPHONE_FUTURE_PROFILE", TypeScreenshot, "ACTIVE", applemeta.AppIPhone67, false},
	} {
		if got := Covers([]Placement{{Group: tc.group, Type: tc.typ, State: tc.state}}, tc.d); got != tc.want {
			t.Errorf("Covers(%s %s, %s) = %v, want %v", tc.group, tc.typ, tc.d, got, tc.want)
		}
	}
	if got := UnknownGroups([]Placement{{Group: "IPHONE_FUTURE_PROFILE", Type: TypeScreenshot}, {Group: "MAC_PROFILE", Type: TypeScreenshot}}); len(got) != 1 || got[0] != "IPHONE_FUTURE_PROFILE" {
		t.Errorf("unknown groups = %v", got)
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
	seen := 0
	for _, f := range doc.Data[0].Attributes.Features {
		for _, p := range f.PlacementPolicies {
			for _, gl := range p.GroupLimits {
				for _, g := range gl.GroupIDs {
					_, ok := Displays(g)
					switch {
					case p.PlacementType == TypeScreenshot:
						seen++
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
	if seen != len(groupDisplays) {
		t.Errorf("ref data has %d APP_SCREENSHOT groups, table %d", seen, len(groupDisplays))
	}
}
