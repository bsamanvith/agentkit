package memory

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tokenize splits text into lowercased words, following the word boundaries
// of Unicode's UAX #29 for the cases that matter to search: a word is a run
// of letters, digits, combining marks and connectors ("_"); an apostrophe, a
// period, a colon or a middle dot between two letters stays inside the word
// ("don't", "e.g", "example.com"), and a period, comma or semicolon between
// two digits too ("3.14", "1,000"). Han and Hiragana characters are a word
// each, as UAX #29 has them; everything else (spaces, punctuation, symbols,
// emoji) separates words. It does no stemming, no stop words and no Unicode
// normalization: "Run" and "run" are one word, "runs" another.
func Tokenize(text string) []string {
	var out []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			out = append(out, strings.ToLower(word.String()))
			word.Reset()
		}
	}
	var prev rune // the last rune added to word, 0 at its start
	for i, w := 0, 0; i < len(text); i += w {
		var r rune
		r, w = utf8.DecodeRuneInString(text[i:])
		switch {
		case r == utf8.RuneError && w == 1:
			flush()
			prev = 0
		case isIdeograph(r):
			flush()
			out = append(out, string(r))
			prev = 0
		case isWordRune(r):
			word.WriteRune(r)
			prev = r
		case prev != 0 && joins(prev, r, text[i+w:]):
			word.WriteRune(r)
			prev = 0 // a mid character never ends a word, and never doubles
		default:
			flush()
			prev = 0
		}
	}
	flush()
	return out
}

func isIdeograph(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) ||
		unicode.Is(unicode.Nl, r) || unicode.IsMark(r) || unicode.Is(unicode.Pc, r)
}

func isLetterLike(r rune) bool { return unicode.IsLetter(r) || unicode.IsMark(r) }

// joins reports whether the mid character r, after prev and before rest,
// stays inside a word (UAX #29 rules WB6/WB7 and WB11/WB12).
func joins(prev, r rune, rest string) bool {
	next, _ := utf8.DecodeRuneInString(rest)
	if isIdeograph(next) {
		return false
	}
	switch {
	case isLetterLike(prev) && isLetterLike(next):
		return isMidLetter(r) || isMidNumLet(r)
	case unicode.IsDigit(prev) && unicode.IsDigit(next):
		return isMidNum(r) || isMidNumLet(r)
	}
	return false
}

func isMidLetter(r rune) bool { return strings.ContainsRune(":·‧︓﹕：", r) }
func isMidNumLet(r rune) bool { return strings.ContainsRune(".'‘’․﹒＇．", r) }
func isMidNum(r rune) bool    { return strings.ContainsRune(",;;؉؊٬߸⁄︐︔﹐﹔，；", r) }

// KeywordQuery is a parsed keyword query: what [ParseKeywordQuery] makes of
// a query's text.
type KeywordQuery struct {
	// Phrases are the quoted parts, each its words in order. An item must
	// contain every phrase, its words next to each other and in order.
	Phrases [][]string
	// Terms are the other words. With no phrase, an item must contain at
	// least one; with phrases, they only add to the score.
	Terms []string
}

// Parts returns the phrases and terms as the highlight strings of a result:
// each phrase's words joined by a space, then each term.
func (q KeywordQuery) Parts() []string {
	out := make([]string, 0, len(q.Phrases)+len(q.Terms))
	for _, p := range q.Phrases {
		out = append(out, strings.Join(p, " "))
	}
	return append(out, q.Terms...)
}

// ParseKeywordQuery parses the text of a keyword query. Words are found by
// [Tokenize]. Text between double quotes ("“" and "”" too) is a phrase; a
// quote left open runs to the end. A phrase of one word is a required word.
// Repeated words and phrases count once. A query with no word at all, or
// with more than MaxQueryParts words and phrases, is ErrInvalid. It never
// panics, whatever the text.
func ParseKeywordQuery(text string) (KeywordQuery, error) {
	var q KeywordQuery
	inQuote := false
	var buf strings.Builder
	end := func() {
		words := Tokenize(buf.String())
		buf.Reset()
		if inQuote {
			if len(words) > 0 && !slices.ContainsFunc(q.Phrases, func(p []string) bool { return slices.Equal(p, words) }) {
				q.Phrases = append(q.Phrases, words)
			}
			return
		}
		for _, w := range words {
			if !slices.Contains(q.Terms, w) {
				q.Terms = append(q.Terms, w)
			}
		}
	}
	for _, r := range text {
		if r == '"' || r == '“' || r == '”' {
			end()
			inQuote = !inQuote
			continue
		}
		buf.WriteRune(r)
	}
	end()
	// A term that is also a one-word phrase is required, so it is not
	// optional as well.
	q.Terms = slices.DeleteFunc(q.Terms, func(t string) bool {
		return slices.ContainsFunc(q.Phrases, func(p []string) bool { return len(p) == 1 && p[0] == t })
	})
	n := len(q.Phrases) + len(q.Terms)
	switch {
	case n == 0:
		return KeywordQuery{}, fmt.Errorf("%w: the query has no words", ErrInvalid)
	case n > MaxQueryParts:
		return KeywordQuery{}, fmt.Errorf("%w: the query has %d words and phrases, at most %d", ErrInvalid, n, MaxQueryParts)
	}
	return q, nil
}
