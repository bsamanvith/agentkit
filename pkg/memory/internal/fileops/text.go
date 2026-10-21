package fileops

import (
	"strings"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

// ReplaceOnce replaces the one occurrence of old in text with new. None is
// ErrNoMatch; more than one is a *memory.MatchError naming the lines where
// each starts.
func ReplaceOnce(text, old, new string) (string, error) {
	i := strings.Index(text, old)
	if i < 0 {
		return "", memory.ErrNoMatch
	}
	if strings.Contains(text[i+1:], old) {
		var lines []int
		for at := 0; ; {
			j := strings.Index(text[at:], old)
			if j < 0 {
				break
			}
			lines = append(lines, 1+strings.Count(text[:at+j], "\n"))
			at += j + 1
		}
		return "", &memory.MatchError{Lines: lines}
	}
	return text[:i] + new + text[i+len(old):], nil
}

// Lines counts text's lines: the newlines, plus one for a last line with no
// newline. An empty text has none.
func Lines(text string) int {
	n := strings.Count(text, "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}

// InsertAt inserts insert after line (0 for the start) of text, as whole
// lines: insert gets a final newline if it has none, and so does a last line
// it goes after. A line outside [0, Lines(text)] is a *memory.LineError.
func InsertAt(text string, line int, insert string) (string, error) {
	n := Lines(text)
	if line < 0 || line > n {
		return "", &memory.LineError{Line: line, Lines: n}
	}
	if insert != "" && !strings.HasSuffix(insert, "\n") {
		insert += "\n"
	}
	// at is the byte offset where line+1 starts.
	at := 0
	for range line {
		j := strings.IndexByte(text[at:], '\n')
		if j < 0 {
			at = len(text)
			break
		}
		at += j + 1
	}
	head, tail := text[:at], text[at:]
	if head != "" && !strings.HasSuffix(head, "\n") {
		head += "\n"
	}
	return head + insert + tail, nil
}
