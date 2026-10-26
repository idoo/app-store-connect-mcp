package tool

import (
	"fmt"
	"strings"
)

// invisibleRanges are runes that render invisibly or reorder text (control,
// format, bidi and tag characters). [SanitizeText] makes each one visible.
var invisibleRanges = [][2]rune{
	{0x00, 0x08},
	{0x0B, 0x1F}, // C0 except \t and \n
	{0x7F, 0x9F}, // DEL and C1
	{0xAD, 0xAD}, // soft hyphen
	{0x061C, 0x061C},
	{0x180E, 0x180E},
	{0x200B, 0x200F},
	{0x202A, 0x202E},
	{0x2060, 0x206F},
	{0xFEFF, 0xFEFF},
	{0xFFF9, 0xFFFB},
	{0xE0000, 0xE007F}, // tag characters
}

func invisible(r rune) bool {
	for _, rg := range invisibleRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// SanitizeText makes third-party text inert for display: each invisible,
// control, bidi or tag rune becomes the visible token "⟨U+XXXX⟩". Newlines
// and tabs are kept; everything else is returned unchanged.
func SanitizeText(s string) string {
	if strings.IndexFunc(s, invisible) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for _, r := range s {
		if invisible(r) {
			fmt.Fprintf(&b, "⟨U+%04X⟩", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SanitizeLine renders third-party or interpolated text on one line: CR,
// LF, U+2028 and U+2029 become "⏎" (a CRLF pair is one break), runs of more
// than two "⏎" collapse to "⏎⏎", then [SanitizeText] makes the remaining
// invisible runes visible, and the result is truncated to maxRunes runes
// ending in "…". A maxRunes ≤ 0 means no limit.
func SanitizeLine(s string, maxRunes int) string {
	var b strings.Builder
	b.Grow(len(s))
	breaks := 0
	prevCR := false
	for _, r := range s {
		switch r {
		case '\r', '\n', '\u2028', '\u2029':
			if r == '\n' && prevCR {
				prevCR = false
				continue
			}
			prevCR = r == '\r'
			breaks++
			if breaks <= 2 {
				b.WriteRune('⏎')
			}
			continue
		}
		prevCR = false
		breaks = 0
		b.WriteRune(r)
	}
	out := SanitizeText(b.String())
	if maxRunes > 0 && strings.Count(out, "")-1 > maxRunes {
		rs := []rune(out)
		out = string(rs[:max(maxRunes-1, 0)]) + "…"
	}
	return out
}
