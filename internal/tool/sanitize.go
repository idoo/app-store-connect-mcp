package tool

import (
	"fmt"
	"strings"
	"unicode"
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
// invisible runes visible, [MarkLookalikes] flags words that impersonate
// Latin text, and the result is truncated to maxRunes runes ending in "…".
// A maxRunes ≤ 0 means no limit.
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
	out := MarkLookalikes(SanitizeText(b.String()))
	if maxRunes > 0 && strings.Count(out, "")-1 > maxRunes {
		rs := []rune(out)
		out = string(rs[:max(maxRunes-1, 0)]) + "…"
	}
	return out
}

// Lookalike kinds reported by [MarkLookalikes].
const (
	lookalikeCompat    = "lookalike"
	lookalikeMixed     = "mixed-script"
	lookalikeCombining = "combining"
)

// confusableScripts are scripts whose letters pass for one another (Latin
// "A", Cyrillic "А", Greek "Α"). A word mixing two of them is suspicious;
// Latin mixed with Han, Kana, Hangul, Arabic or Devanagari is ordinary text.
var confusableScripts = []*unicode.RangeTable{
	unicode.Latin, unicode.Cyrillic, unicode.Greek, unicode.Armenian, unicode.Cherokee, unicode.Coptic,
}

// compatRanges are compatibility forms of Latin letters and digits:
// small-capital and modifier letters, super/subscripts, letterlike symbols,
// circled letters, fullwidth ASCII letters and digits, mathematical
// alphanumerics and squared letters.
var compatRanges = [][2]rune{
	{0x1D00, 0x1D6A},
	{0x2070, 0x209F},
	{0x2100, 0x214F},
	{0x2460, 0x24FF},
	{0xFF10, 0xFF19},
	{0xFF21, 0xFF3A},
	{0xFF41, 0xFF5A},
	{0x1D400, 0x1D7FF},
	{0x1F130, 0x1F189},
}

func compat(r rune) bool {
	for _, rg := range compatRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r) || compat(r)
}

// MarkLookalikes visibly flags words (runs of letters, digits, combining
// marks and Latin compatibility forms) that can pass for different Latin
// text, wrapping the whole word as "⟨<kind>: <word>⟩" so the human sees both
// the warning and the original characters:
//
//   - "lookalike": the word contains a fullwidth, mathematical, circled,
//     letterlike, small-capital or super/subscript form of a Latin letter or
//     digit ("Ａpp", "𝐀pp" → "⟨lookalike: 𝐀pp⟩");
//   - "mixed-script": letters from two of Latin, Cyrillic, Greek, Armenian,
//     Cherokee and Coptic ("Аpp" with Cyrillic А → "⟨mixed-script: Аpp⟩");
//   - "combining": a combining mark on a Latin letter or ASCII digit, or with
//     no base inside the word ("a̸b" → "⟨combining: a̸b⟩"); decomposed accents
//     (e + U+0301) are flagged too, precomposed "é" is not.
//
// The first matching kind in that order is reported. Words wholly in one
// script (a Russian, Greek, Hindi or Japanese sentence) are left untouched.
func MarkLookalikes(s string) string {
	ascii := true
	for i := range len(s) {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for len(s) > 0 {
		start := strings.IndexFunc(s, wordRune)
		if start < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:start])
		s = s[start:]
		end := strings.IndexFunc(s, func(r rune) bool { return !wordRune(r) })
		if end < 0 {
			end = len(s)
		}
		word := s[:end]
		s = s[end:]
		if kind := lookalikeKind(word); kind != "" {
			b.WriteString("⟨" + kind + ": " + word + "⟩")
			continue
		}
		b.WriteString(word)
	}
	return b.String()
}

// lookalikeKind classifies one word for [MarkLookalikes]; "" means clean.
func lookalikeKind(word string) string {
	var scripts [6]bool
	latin, mark, base := false, false, false
	for _, r := range word {
		switch {
		case compat(r):
			return lookalikeCompat
		case unicode.IsMark(r):
			mark = true
			continue
		case r < 0x80:
			latin = true // ASCII letters and digits
		}
		base = true
		for i, tab := range confusableScripts {
			if unicode.Is(tab, r) {
				scripts[i] = true
				latin = latin || i == 0
			}
		}
	}
	n := 0
	for _, seen := range scripts {
		if seen {
			n++
		}
	}
	switch {
	case n > 1:
		return lookalikeMixed
	case mark && (latin || !base):
		return lookalikeCombining
	}
	return ""
}
