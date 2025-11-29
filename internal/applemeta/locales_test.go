package applemeta

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestLocalesSortedAndUnique(t *testing.T) {
	l := Locales()
	if !slices.IsSorted(l) {
		t.Errorf("locales not sorted: %v", l)
	}
	if len(slices.Compact(slices.Clone(l))) != len(l) {
		t.Error("locales contain duplicates")
	}
	for _, code := range l {
		if got, err := NormalizeLocale(code); err != nil || got != code {
			t.Errorf("NormalizeLocale(%q) = %q, %v; want identity", code, got, err)
		}
	}
}

func TestNormalizeLocale(t *testing.T) {
	tests := []struct{ in, want string }{
		{"de", "de-DE"},
		{"DE-de", "de-DE"},
		{"de-AT", "de-DE"},
		{"ja-JP", "ja"},
		{"ja_JP", "ja"},
		{"zh-CN", "zh-Hans"},
		{"zh_TW", "zh-Hant"},
		{"zh-hans", "zh-Hans"},
		{"pt", "pt-BR"},
		{"pt_PT", "pt-PT"},
		{"es", "es-ES"},
		{"es-AR", "es-MX"},
		{"en", "en-US"},
		{"en-NZ", "en-AU"},
		{"fr", "fr-FR"},
		{"nl", "nl-NL"},
		{"nb", "no"},
		{"nb-NO", "no"},
		{"sv-SE", "sv"},
		{"ta", "ta-IN"},
		{"German", "de-DE"},
		{"  japanese ", "ja"},
		{"SWEDISH", "sv"},
		{"Traditional  Chinese", "zh-Hant"},
		{"English (U.K.)", "en-GB"},
		{"Brazilian Portuguese", "pt-BR"},
	}
	for _, tc := range tests {
		got, err := NormalizeLocale(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeLocale(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestNormalizeLocaleRejects(t *testing.T) {
	for _, in := range []string{"", "xx", "klingon", "en-ZZ", "es-ES-x"} {
		got, err := NormalizeLocale(in)
		var le *LocaleError
		if !errors.As(err, &le) {
			t.Errorf("NormalizeLocale(%q) = %q, %v; want *LocaleError", in, got, err)
			continue
		}
		if len(le.Valid) != len(Locales()) || !strings.Contains(err.Error(), "zh-Hans") {
			t.Errorf("error for %q does not list valid codes: %v", in, err)
		}
	}
}

func TestTerritoryLanguage(t *testing.T) {
	for territory, loc := range TerritoryLanguage {
		if !IsLocale(loc) {
			t.Errorf("territory %s maps to invalid locale %q", territory, loc)
		}
		if len(territory) != 3 || strings.ToUpper(territory) != territory {
			t.Errorf("territory %q is not upper-case alpha-3", territory)
		}
	}
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"deu", "de-DE", true},
		{"JPN", "ja", true},
		{"MEX", "es-MX", true},
		{"ATA", "", false},
	}
	for _, tc := range tests {
		got, ok := LocaleForTerritory(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("LocaleForTerritory(%q) = %q,%v; want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
