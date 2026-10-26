package release

import (
	"strings"
	"testing"

	"github.com/idoo/app-store-connect-mcp/internal/tool"
)

func TestReleaseStatusAccessibilityLabels(t *testing.T) {
	for _, tc := range []struct {
		a11y, status, item string
	}{
		{"", statusOK, ""},
		{"none", statusWarn, "IPHONE: no accessibility declaration"},
		{"draft", statusWarn, "IPHONE: declaration is DRAFT; publish it"},
	} {
		t.Run("a11y="+tc.a11y, func(t *testing.T) {
			f := readyFixture()
			f.a11y = tc.a11y
			out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
			c := checkByID(t, out, "accessibility_labels")
			if c.Status != tc.status {
				t.Fatalf("check = %+v", c)
			}
			if tc.item == "" {
				if c.FixVia != nil || len(c.Items) != 0 {
					t.Errorf("ok check carries a fix: %+v", c)
				}
				return
			}
			if c.Items[0] != tc.item || c.FixVia == nil || c.FixVia.Tool != "appinfo_update" || !strings.HasSuffix(c.FixVia.Manual, "/distribution/accessibility") {
				t.Errorf("check = %+v", c)
			}
			if len(c.Items) != 2 || c.Items[1] != "IPAD: no accessibility declaration" {
				t.Errorf("iPad family not required: %+v", c.Items)
			}
		})
	}
}

// A missing accessibility label never blocks the release.
func TestReleaseStatusAccessibilityNeverBlocks(t *testing.T) {
	f := readyFixture()
	f.a11y = "none"
	out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
	if !ready(out) {
		t.Errorf("summary = %+v", out.Summary)
	}
}

func TestCheckAccessibilityFamilies(t *testing.T) {
	s := &statusSnapshot{App: tool.App{ID: "1"}, Platform: tool.MacOS, A11y: []tool.Resource{
		{Type: "accessibilityDeclarations", ID: "d", Attributes: map[string]any{"deviceFamily": "IPHONE", "state": "PUBLISHED"}},
	}}
	if c := checkAccessibility(s); c.Status != statusWarn || c.Items[0] != "MAC: no accessibility declaration" {
		t.Errorf("mac = %+v", c)
	}
	s.A11y = append(s.A11y, tool.Resource{ID: "m", Attributes: map[string]any{"deviceFamily": "MAC", "state": "REPLACED"}},
		tool.Resource{ID: "m2", Attributes: map[string]any{"deviceFamily": "MAC", "state": "PUBLISHED"}})
	if c := checkAccessibility(s); c.Status != statusOK {
		t.Errorf("published mac = %+v", c)
	}
	s.A11yErr = "403 forbidden"
	if c := checkAccessibility(s); c.Status != statusWarn || !strings.Contains(c.Detail, "403") {
		t.Errorf("error = %+v", c)
	}
}

func TestReleaseStatusAgeRatingSemantics(t *testing.T) {
	for _, tc := range []struct {
		name string
		age  map[string]any
		item string
	}{
		{"assurance without url", map[string]any{"ageAssurance": true, "developerAgeRatingInfoUrl": nil}, "ageAssurance is true but developerAgeRatingInfoUrl is empty"},
		{"social restricted without url", map[string]any{"socialMediaAgeRestricted": true}, "socialMediaAgeRestricted is true but developerAgeRatingInfoUrl is empty"},
		{"legacy override", map[string]any{"ageRatingOverride": "SEVENTEEN_PLUS", "ageRatingOverrideV2": nil}, "legacy ageRatingOverride set but ageRatingOverrideV2 empty: re-answer the 2025 questionnaire"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := readyFixture()
			f.age = tc.age
			out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
			c := checkByID(t, out, "age_rating")
			if c.Status != statusWarn || len(c.Items) != 1 || c.Items[0] != tc.item || c.FixVia == nil {
				t.Errorf("check = %+v", c)
			}
		})
	}
	f := readyFixture()
	f.age = map[string]any{"ageAssurance": true, "developerAgeRatingInfoUrl": "https://example.com/age", "ageRatingOverrideV2": "NONE", "ageRatingOverride": "NONE"}
	out, _ := call(t, setup(t, f), `{"app":"Hjul"}`)
	if c := checkByID(t, out, "age_rating"); c.Status != statusOK {
		t.Errorf("consistent = %+v", c)
	}
}

// A new 2025 tier versus the live app info is an ok note, not a warning.
func TestCheckAgeRatingTierChangeNote(t *testing.T) {
	s := &statusSnapshot{AgeRating: &tool.Resource{Attributes: map[string]any{"violenceCartoonOrFantasy": "NONE"}}, AgeTier: "NINE_PLUS", LiveAgeTier: "FOUR_PLUS"}
	c := checkAgeRating(s)
	if c.Status != statusOK || !strings.Contains(c.Detail, "from FOUR_PLUS (live) to NINE_PLUS") {
		t.Errorf("check = %+v", c)
	}
}
