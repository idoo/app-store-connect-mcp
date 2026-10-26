package tool

import (
	"errors"
	"strings"
	"testing"
)

func TestValidEmail(t *testing.T) {
	tests := []struct {
		name, in string
		ok       bool
	}{
		{"plain", "ann@example.com", true},
		{"plus tag", "ann+beta@example.com", true},
		{"subdomain", "ann.lee@mail.eu.example.co", true},
		{"atext", "o'neil!#$%&*=?^_`{|}~-@example.org", true},
		{"nbsp", "ann\u00a0@example.com", false},
		{"newline", "ann@example.com\nRole: ADMIN", false},
		{"rlo", "ann@exa\u202Emple.com", false},
		{"lri", "\u2066ann@example.com", false},
		{"two at", "ann@b@example.com", false},
		{"no tld", "ann@localhost", false},
		{"numeric tld", "ann@example.123", false},
		{"non ascii", "änn@example.com", false},
		{"too long", strings.Repeat("a", 64) + "@" + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 60) + ".com", false},
		{"local too long", strings.Repeat("a", 65) + "@example.com", false},
		{"leading dot", ".ann@example.com", false},
		{"double dot", "ann..lee@example.com", false},
		{"trailing dot", "ann.@example.com", false},
		{"space", "ann lee@example.com", false},
		{"empty", "", false},
		{"empty label", "ann@example..com", false},
		{"hyphen label edge", "ann@-example.com", false},
		{"comma", "ann@example.com,bob@example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidEmail(tt.in)
			if (err == nil) != tt.ok {
				t.Fatalf("ValidEmail(%q) = %v, want ok=%v", tt.in, err, tt.ok)
			}
			if err != nil && !errors.Is(err, ErrInvalidArgs) {
				t.Errorf("error %v does not wrap ErrInvalidArgs", err)
			}
			if err != nil && strings.ContainsAny(err.Error(), "\n\u00a0\u202E\u2066") {
				t.Errorf("error message carries raw input: %q", err.Error())
			}
		})
	}
}
