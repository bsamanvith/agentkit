package memory

import (
	"fmt"
	"unicode/utf8"
)

// CheckText returns nil when text is readable text, the only thing a file
// may hold: valid UTF-8 with no control character but tab, line feed and
// carriage return (no NUL, no escape sequences, none of C1). Otherwise it
// returns an error matching [ErrNotText] that says where. Memory is plain
// notes the model and its users read; anything else is refused, not stored.
func CheckText(text string) error {
	for i, r := range text {
		switch {
		case r == utf8.RuneError && !validAt(text, i):
			return fmt.Errorf("%w: byte %d is not UTF-8", ErrNotText, i)
		case r == '\t' || r == '\n' || r == '\r':
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return fmt.Errorf("%w: byte %d is the control character %U", ErrNotText, i, r)
		}
	}
	return nil
}

// validAt reports whether the U+FFFD at byte i of s is written there, rather
// than standing for a byte that is not UTF-8.
func validAt(s string, i int) bool {
	_, n := utf8.DecodeRuneInString(s[i:])
	return n == 3
}
