package common

import "github.com/bsamanvith/agentkit/connectors/internal/trunc"

// TruncatedMarker follows text cut by [Truncate].
const TruncatedMarker = trunc.Marker

// Truncate cuts s to limit characters (runes), at a rune boundary, and adds
// [TruncatedMarker] when it cut anything.
func Truncate(s string, limit int) string { return trunc.Short(s, limit, TruncatedMarker) }

// TruncateContent cuts s like [Truncate], with a marker that says how many
// characters were cut and how to get the rest, such as
//
//	…[truncated 12,400 characters; call gmail_read_message with a larger max_body_chars]
//
// so the model is steered instead of left guessing.
func TruncateContent(s string, limit int, more string) string {
	return trunc.Content(s, limit, more)
}
