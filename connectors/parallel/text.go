package parallel

import (
	"strings"

	"github.com/bsamanvith/agentkit/connectors/internal/trunc"
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

// truncate caps a short field at max characters, ending a cut one with "…".
func truncate(s string, max int) string { return trunc.Short(s, max, ellipsis) }

// truncateContent caps content at max characters. Cut content ends with a
// marker saying how much was dropped and what the agent can do about it:
// "…[truncated 12,400 characters; <hint>]".
func truncateContent(s string, max int, hint string) string {
	return trunc.Content(s, max, hint)
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
