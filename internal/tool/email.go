package tool

import "strings"

// atext is RFC 5322 atext: the characters allowed in an unquoted local part
// besides letters and digits.
const atext = "!#$%&'*+-/=?^_`{|}~"

// ValidEmail checks s strictly and returns an [ErrInvalidArgs] error when it
// is not a plain ASCII address: at most 254 bytes, exactly one "@", a local
// part of RFC 5322 atext and single interior dots (at most 64 bytes), and a
// domain of at least two LDH labels (1–63 bytes each) ending in an
// alphabetic TLD. Whitespace, control, format and non-ASCII characters are
// refused. The value is reported %q-escaped so it cannot forge text.
func ValidEmail(s string) error {
	if reason := emailProblem(s); reason != "" {
		return InvalidArgs("email %q: %s", s, reason)
	}
	return nil
}

func emailProblem(s string) string {
	if s == "" {
		return "empty"
	}
	if len(s) > 254 {
		return "longer than 254 bytes"
	}
	for i := range len(s) {
		if c := s[i]; c <= ' ' || c >= 0x7F {
			return "only printable ASCII without spaces is allowed"
		}
	}
	if strings.Count(s, "@") != 1 {
		return "needs exactly one @"
	}
	local, domain, _ := strings.Cut(s, "@")
	if local == "" || len(local) > 64 {
		return "local part must be 1–64 bytes"
	}
	for _, part := range strings.Split(local, ".") {
		if part == "" {
			return "local part has a leading, trailing or double dot"
		}
		for i := range len(part) {
			if c := part[i]; !isAlnum(c) && !strings.ContainsRune(atext, rune(c)) {
				return "local part has a character outside RFC 5322 atext"
			}
		}
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return "domain needs a top-level domain"
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "domain labels must be 1–63 letters, digits or inner hyphens"
		}
		for i := range len(l) {
			if c := l[i]; !isAlnum(c) && c != '-' {
				return "domain labels must be 1–63 letters, digits or inner hyphens"
			}
		}
	}
	tld := labels[len(labels)-1]
	for i := range len(tld) {
		if c := tld[i]; (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return "top-level domain must be alphabetic"
		}
	}
	return ""
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
