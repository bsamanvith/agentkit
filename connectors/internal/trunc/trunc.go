// Package trunc cuts text taken from a response to a size the model can use,
// at a character boundary and with a marker that says it was cut
// (docs/CONNECTORS.md §2). Every connector cuts the same way, so the model
// learns one marker.
package trunc

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Marker ends a short field (a title, a URL, an error message) that was cut.
const Marker = "…[truncated]"

// Cut returns s cut to at most limit characters (runes) and how many it cut.
// The result is valid UTF-8: invalid bytes become U+FFFD first.
func Cut(s string, limit int) (string, int) {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	total := utf8.RuneCountInString(s)
	if total <= limit {
		return s, 0
	}
	n := 0
	for i := range s {
		if n == limit {
			return s[:i], total - limit
		}
		n++
	}
	return s, 0
}

// Short cuts s to limit characters, ending a cut one with marker.
func Short(s string, limit int, marker string) string {
	kept, cut := Cut(s, limit)
	if cut == 0 {
		return kept
	}
	return kept + marker
}

// Content cuts a result's content to limit characters. A cut one ends with a
// marker saying how much was cut and, in hint, how the agent gets the rest:
//
//	…[truncated 12,400 characters; call exa_extract on this URL for the full page]
func Content(s string, limit int, hint string) string {
	kept, cut := Cut(s, limit)
	if cut == 0 {
		return kept
	}
	unit := "characters"
	if cut == 1 {
		unit = "character"
	}
	return fmt.Sprintf("%s…[truncated %s %s; %s]", kept, Thousands(cut), unit, hint)
}

// Thousands writes n with commas: 12400 is "12,400".
func Thousands(n int) string {
	s := strconv.Itoa(n)
	start := 0
	if n < 0 {
		start = 1
	}
	for i := len(s) - 3; i > start; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
