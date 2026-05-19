package applemeta

import (
	"errors"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	tests := []struct {
		name  string
		field Field
		value string
		want  *LimitError
	}{
		{"name at limit", FieldName, strings.Repeat("a", 30), nil},
		{"name over", FieldName, strings.Repeat("a", 31), &LimitError{FieldName, 30, 31, "characters"}},
		{"multibyte name counts runes", FieldName, strings.Repeat("ö", 30), nil},
		{"subtitle over", FieldSubtitle, strings.Repeat("x", 31), &LimitError{FieldSubtitle, 30, 31, "characters"}},
		{"keywords ascii at limit", FieldKeywords, strings.Repeat("ab,", 33) + "a", nil},
		// 34 Japanese characters × 3 bytes = 102 bytes, under 100 runes.
		{"keywords multibyte bytes", FieldKeywords, strings.Repeat("日", 34), &LimitError{FieldKeywords, 100, 102, "bytes"}},
		{"keywords multibyte fits", FieldKeywords, strings.Repeat("日", 33), nil},
		{"promo over", FieldPromotionalText, strings.Repeat("p", 171), &LimitError{FieldPromotionalText, 170, 171, "characters"}},
		{"description at limit", FieldDescription, strings.Repeat("d", 4000), nil},
		{"whats new over", FieldWhatsNew, strings.Repeat("w", 4001), &LimitError{FieldWhatsNew, 4000, 4001, "characters"}},
		{"iap description over", FieldIAPDescription, strings.Repeat("i", 46), &LimitError{FieldIAPDescription, 45, 46, "characters"}},
		{"empty ok", FieldWhatToTest, "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.field, tc.value)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("Check = %v; want nil", err)
				}
				return
			}
			var le *LimitError
			if !errors.As(err, &le) {
				t.Fatalf("Check = %v; want *LimitError", err)
			}
			if *le != *tc.want {
				t.Errorf("Check = %+v; want %+v", *le, *tc.want)
			}
		})
	}
}

func TestCheckUnknownField(t *testing.T) {
	err := Check("nope", "x")
	var le *LimitError
	if err == nil || errors.As(err, &le) {
		t.Fatalf("Check(unknown) = %v; want plain error", err)
	}
}

func TestLimit(t *testing.T) {
	if m, b, ok := Limit(FieldKeywords); m != 100 || !b || !ok {
		t.Errorf("Limit(keywords) = %d,%v,%v", m, b, ok)
	}
	if _, _, ok := Limit("nope"); ok {
		t.Error("Limit(unknown) ok")
	}
}
