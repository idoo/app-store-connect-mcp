package applemeta

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
)

// Every displayClasses.screenDimensions entry of Apple's appAssetLibraryRefData
// (full copy in the assetlib testdata) must map to a display type: "WxH"
// pixel entries via DisplayForPixels, watch entries ("49" mm) via the
// APP_<displayClassId> slot. IPHONE_DUO is the pending slot the pinned spec
// lacks.
func TestRefDataDisplayClassesMap(t *testing.T) {
	b, err := os.ReadFile("../tools/assetlib/testdata/refdata.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Data []struct {
			Attributes struct {
				DisplayClasses []struct {
					DisplayClassID   string   `json:"displayClassId"`
					DeviceFamily     *string  `json:"deviceFamily"`
					ScreenDimensions []string `json:"screenDimensions"`
				} `json:"displayClasses"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Data) == 0 {
		t.Fatal("ref data has no data")
	}
	rd := doc.Data[0].Attributes
	if len(rd.DisplayClasses) < 10 {
		t.Fatalf("fixture has %d display classes", len(rd.DisplayClasses))
	}
	for _, c := range rd.DisplayClasses {
		if c.DisplayClassID == "IPHONE_DUO" {
			if !slices.ContainsFunc(pendingDisplays, func(p PendingDisplay) bool { return p.Type == AppIPhoneDuo }) {
				t.Errorf("IPHONE_DUO is not a pending display")
			}
			continue
		}
		for _, dim := range c.ScreenDimensions {
			var w, h int
			if n, _ := fmt.Sscanf(dim, "%dx%d", &w, &h); n == 2 {
				if got, ok := DisplayForPixels(w, h, ""); !ok {
					t.Errorf("%s %s maps to no display type", c.DisplayClassID, dim)
				} else if fam := c.DeviceFamily; fam != nil && !familyMatches(*fam, got) {
					t.Errorf("%s %s maps to %v, wrong family", c.DisplayClassID, dim, got)
				}
				continue
			}
			if _, ok := LookupDisplay(DisplayType("APP_" + c.DisplayClassID)); !ok {
				t.Errorf("%s (%s mm) has no APP_%s display type", c.DisplayClassID, dim, c.DisplayClassID)
			}
		}
	}
}

func familyMatches(fam string, types []DisplayType) bool {
	want := map[string]Family{"IPHONE": FamilyIPhone, "IPAD": FamilyIPad}[fam]
	for _, d := range types {
		if s, _ := LookupDisplay(d); s.Family == want {
			return true
		}
	}
	return false
}

func TestLegacyRefDataSizes(t *testing.T) {
	for _, c := range []struct {
		w, h int
		want DisplayType
	}{
		{600, 1136, AppIPhone40},
		{600, 960, AppIPhone35},
		{320, 480, AppIPhone35},
		{480, 320, AppIPhone35},
		{300, 480, AppIPhone35},
		{320, 460, AppIPhone35},
		{748, 1024, AppIPad97},
		{1496, 2048, AppIPad97},
		{2048, 1496, AppIPad97},
	} {
		got, ok := DisplayForPixels(c.w, c.h, "")
		if !ok || !slices.Contains(got, c.want) {
			t.Errorf("%dx%d = %v, want %s", c.w, c.h, got, c.want)
		}
	}
}
