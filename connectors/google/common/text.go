package common

import (
	"fmt"
	"strconv"
	"unicode/utf8"
)

// TruncatedMarker follows text cut by [Truncate].
const TruncatedMarker = "…[truncated]"

// Truncate cuts s to limit characters (runes), at a rune boundary, and adds
// [TruncatedMarker] when it cut anything.
func Truncate(s string, limit int) string {
	return cut(s, limit, func(int) string { return TruncatedMarker })
}

// TruncateContent cuts s like [Truncate], with a marker that says how many
// characters were cut and how to get the rest, such as
//
//	…[truncated 12,400 characters; call gmail_read_message with a larger max_body_chars]
//
// so the model is steered instead of left guessing.
func TruncateContent(s string, limit int, more string) string {
	return cut(s, limit, func(n int) string {
		unit := "characters"
		if n == 1 {
			unit = "character"
		}
		return fmt.Sprintf("…[truncated %s %s; %s]", thousands(n), unit, more)
	})
}

func cut(s string, limit int, marker func(cutRunes int) string) string {
	total := utf8.RuneCountInString(s)
	if total <= limit {
		return s
	}
	n := 0
	for i := range s {
		if n == limit {
			return s[:i] + marker(total-limit)
		}
		n++
	}
	return s
}

// thousands formats n with commas: 12400 is "12,400".
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
