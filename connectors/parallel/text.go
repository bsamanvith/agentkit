package parallel

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// ellipsis ends a short field (a title, an error message) that was cut.
	ellipsis = "…"
	// excerptSep joins a result's excerpts into one content string.
	excerptSep = "\n…\n"
	// maxTitleChars caps a title, which Parallel does not bound.
	maxTitleChars = 500
	// maxURLChars caps a URL taken from a response.
	maxURLChars = 2048
)

// Hints in the marker that ends cut content: what the agent can do to get
// the rest.
const (
	searchHint          = "call parallel_extract on this URL for more of the page"
	extractExcerptsHint = "call parallel_extract with a narrower objective for the parts you need"
	extractFullHint     = "the rest of the page is not available through this tool"
)

// cut returns the first max characters of s and the number of characters
// dropped. It never splits a character.
func cut(s string, max int) (string, int) {
	n := utf8.RuneCountInString(s)
	if n <= max {
		return s, 0
	}
	i := 0
	for range max {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i], n - max
}

// truncate caps a short field at max characters, ending a cut one with "…".
func truncate(s string, max int) string {
	kept, dropped := cut(s, max)
	if dropped == 0 {
		return s
	}
	return kept + ellipsis
}

// truncateContent caps content at max characters. Cut content ends with a
// marker saying how much was dropped and what the agent can do about it:
// "…[truncated 12,400 characters; <hint>]".
func truncateContent(s string, max int, hint string) string {
	kept, dropped := cut(s, max)
	if dropped == 0 {
		return s
	}
	return fmt.Sprintf("%s…[truncated %s characters; %s]", kept, thousands(dropped), hint)
}

// thousands formats n with comma separators: 12400 is "12,400".
func thousands(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// joinExcerpts joins excerpts, skipping empty ones.
func joinExcerpts(excerpts []string) string {
	kept := make([]string, 0, len(excerpts))
	for _, e := range excerpts {
		if strings.TrimSpace(e) != "" {
			kept = append(kept, e)
		}
	}
	return strings.Join(kept, excerptSep)
}
