package client

import (
	"fmt"
	"strings"
	"unicode"
)

// MaxDescriptionRunes caps a sanitized tool description.
const MaxDescriptionRunes = 2048

// SanitizeDescription cleans a tool description from a server you may not
// trust before a model reads it: it removes what a person reviewing the text
// would not see (control characters other than newline and tab, format
// characters such as zero-width spaces, bidirectional overrides and the tags
// block, variation selectors, invisible fillers) and keeps at most
// [MaxDescriptionRunes] runes, marking the cut. Invalid UTF-8 becomes U+FFFD.
// The result is valid UTF-8.
func SanitizeDescription(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	kept, dropped := 0, 0
	for _, r := range s {
		switch {
		case hidden(r):
		case kept == MaxDescriptionRunes:
			dropped++
		default:
			b.WriteRune(r)
			kept++
		}
	}
	out := strings.TrimSpace(b.String())
	if dropped > 0 {
		out += fmt.Sprintf("…[truncated %d characters]", dropped)
	}
	return out
}

// hidden reports a rune that renders as nothing, or changes how the text
// around it reads, in a typical review screen.
func hidden(r rune) bool {
	switch r {
	case '\n', '\t':
		return false
	case '\u115f', '\u1160', '\u3164', '\uffa0', '\u034f', '\u180e': // fillers, grapheme joiner, Mongolian vowel separator
		return true
	}
	return unicode.IsControl(r) ||
		unicode.In(r, unicode.Cf, unicode.Bidi_Control, unicode.Variation_Selector, unicode.Zl, unicode.Zp, unicode.Noncharacter_Code_Point)
}
