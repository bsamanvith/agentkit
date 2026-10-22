// Package keyword ranks the items of a keyword search by BM25, the same way
// for every backend: a backend finds the candidates and the statistics of
// the scope, and Rank scores, filters, orders and cuts them. Two backends
// holding the same items therefore return the same results, scores included.
package keyword

import (
	"cmp"
	"math"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

// The BM25 parameters: the usual ones (Robertson and Zaragoza, 2009), and
// SQLite FTS5's.
const (
	K1 = 1.2
	B  = 0.75
)

// Parts returns the parts of q a score sums over: each phrase, then each
// term as a phrase of one word.
func Parts(q memory.KeywordQuery) [][]string {
	out := make([][]string, 0, len(q.Phrases)+len(q.Terms))
	out = append(out, q.Phrases...)
	for _, t := range q.Terms {
		out = append(out, []string{t})
	}
	return out
}

// Count returns how often part occurs in tokens, its words next to each other
// and in order. Occurrences may overlap.
func Count(tokens, part []string) int {
	if len(part) == 0 {
		return 0
	}
	n := 0
	for i := 0; i+len(part) <= len(tokens); i++ {
		if slices.Equal(tokens[i:i+len(part)], part) {
			n++
		}
	}
	return n
}

// Matches reports whether tokens match q: every phrase occurs and, with no
// phrase, at least one term.
func Matches(q memory.KeywordQuery, tokens []string) bool {
	for _, p := range q.Phrases {
		if Count(tokens, p) == 0 {
			return false
		}
	}
	if len(q.Phrases) > 0 {
		return true
	}
	return slices.ContainsFunc(q.Terms, func(t string) bool { return Count(tokens, []string{t}) > 0 })
}

// Stats are the statistics of a scope that BM25 needs.
type Stats struct {
	// Docs is how many items the scope holds; Tokens their words in all.
	Docs, Tokens int
	// DF is, for each of Parts(q), how many items contain it.
	DF []int
}

// Doc is a candidate: an item and its words ([memory.Tokenize] of its text).
type Doc struct {
	Item   memory.Item
	Tokens []string
}

// Rank returns the top k of docs that match q and filter, scored by BM25
// over st: best first, then the newest, then by ID. Docs that do not match
// are dropped, so a backend may pass more candidates than match.
func Rank(q memory.KeywordQuery, docs []Doc, st Stats, filter map[string]string, k int) []memory.Result {
	parts := Parts(q)
	labels := q.Parts()
	var avg float64
	if st.Docs > 0 {
		avg = float64(st.Tokens) / float64(st.Docs)
	}
	out := make([]memory.Result, 0, min(k, len(docs)))
	for _, d := range docs {
		if !memory.MatchesFilter(d.Item.Metadata, filter) || !Matches(q, d.Tokens) {
			continue
		}
		var score float64
		var hl []string
		for i, p := range parts {
			f := Count(d.Tokens, p)
			if f == 0 {
				continue
			}
			hl = append(hl, labels[i])
			score += idf(st.Docs, st.DF[i]) * tf(f, len(d.Tokens), avg)
		}
		out = append(out, memory.Result{Item: d.Item.Clone(), Score: score, Mode: memory.ModeKeyword, Found: []memory.Mode{memory.ModeKeyword}, Highlights: hl})
	}
	slices.SortFunc(out, Compare)
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// Compare orders results best first: by score, then the newest, then by ID.
func Compare(a, b memory.Result) int {
	if c := cmp.Compare(b.Score, a.Score); c != 0 {
		return c
	}
	if c := b.Item.Time.Compare(a.Item.Time); c != 0 {
		return c
	}
	return strings.Compare(a.Item.ID, b.Item.ID)
}

// idf is BM25's inverse document frequency, in the form that never goes
// negative (FTS5's): ln(1 + (N - n + 0.5) / (n + 0.5)).
func idf(n, df int) float64 {
	return math.Log(1 + (float64(n)-float64(df)+0.5)/(float64(df)+0.5))
}

// tf is BM25's saturated term frequency of f occurrences in a document of dl
// words, where documents average avg.
func tf(f, dl int, avg float64) float64 {
	norm := 1.0
	if avg > 0 {
		norm = 1 - B + B*float64(dl)/avg
	}
	return float64(f) * (K1 + 1) / (float64(f) + K1*norm)
}
