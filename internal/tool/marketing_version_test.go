package tool

import (
	"slices"
	"testing"
)

func TestSameMarketingVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.0", "1.0.0", true},
		{"1", "1.0.0", true},
		{" 2.1 ", "2.1.0", true},
		{"10.0", "10", true},
		{"1.0", "1.0", true},
		{"1.0.1", "1.0", false},
		{"2.10", "2.1", false},
		{"1.10", "1.1", false},
		{"1.4", "1.5", false},
	} {
		if got := SameMarketingVersion(c.a, c.b); got != c.want {
			t.Errorf("SameMarketingVersion(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestMarketingVersionAliases(t *testing.T) {
	for in, want := range map[string][]string{
		"1.0":     {"1.0", "1", "1.0.0"},
		"1.0.0":   {"1.0.0", "1", "1.0"},
		"1":       {"1", "1.0", "1.0.0"},
		"2.1":     {"2.1", "2.1.0"},
		"1.2.3":   {"1.2.3"},
		"1.2.3.0": {"1.2.3.0", "1.2.3"},
		"":        {""},
	} {
		if got := MarketingVersionAliases(in); !slices.Equal(got, want) {
			t.Errorf("MarketingVersionAliases(%q) = %v, want %v", in, got, want)
		}
	}
}
