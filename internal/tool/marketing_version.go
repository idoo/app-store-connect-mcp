package tool

import "strings"

// maxPaddedComponents is how far [MarketingVersionAliases] pads with ".0":
// App Store marketing versions have at most three components.
const maxPaddedComponents = 3

// canonicalMarketingVersion drops trailing ".0" components, keeping the
// first one: "1.0.0" and "1.0" become "1", "2.10" stays.
func canonicalMarketingVersion(v string) string {
	v = strings.TrimSpace(v)
	for strings.HasSuffix(v, ".0") {
		v = v[:len(v)-2]
	}
	return v
}

// SameMarketingVersion reports whether a and b name the same marketing
// version the way App Store Connect compares them: trailing ".0" components
// don't count, so a 1.0.0 build belongs to version 1.0.
func SameMarketingVersion(a, b string) bool {
	return canonicalMarketingVersion(a) == canonicalMarketingVersion(b)
}

// MarketingVersionAliases returns v followed by every other spelling App
// Store Connect treats as the same version ("1.0" → 1.0, 1, 1.0.0), for an
// exact-match filter such as filter[preReleaseVersion.version].
func MarketingVersionAliases(v string) []string {
	v = strings.TrimSpace(v)
	out := []string{v}
	alias := canonicalMarketingVersion(v)
	if alias == "" {
		return out
	}
	for n := strings.Count(alias, ".") + 1; ; n++ {
		if alias != v {
			out = append(out, alias)
		}
		if n >= maxPaddedComponents {
			return out
		}
		alias += ".0"
	}
}
