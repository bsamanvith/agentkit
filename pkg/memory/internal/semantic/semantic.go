// Package semantic ranks the items of a semantic search by cosine
// similarity, and fuses a keyword and a semantic ranking into a hybrid one,
// the same way for every backend: a backend keeps the vectors and finds the
// scope's items; Rank and Fuse score, filter, order and cut them.
package semantic

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/keyword"
)

// Vector is an embedding as an index keeps it: of unit length (or zero, for a
// text the embedder gave nothing), with the model and dimension that made it.
type Vector struct {
	Model string
	Dim   int
	V     []float32
}

// Embed embeds texts with e and returns their normalized vectors. A vector
// of another dimension than e's Model is memory.ErrEmbedderMismatch; a count
// other than len(texts), or a value that is not finite, is an error too.
func Embed(ctx context.Context, e memory.Embedder, texts []string, p memory.Purpose) ([]Vector, error) {
	name, dim := e.Model()
	vs, err := e.Embed(ctx, texts, p)
	if err != nil {
		return nil, fmt.Errorf("memory: embed with %s: %w", name, err)
	}
	if len(vs) != len(texts) {
		return nil, fmt.Errorf("memory: embedder %s returned %d vectors for %d texts", name, len(vs), len(texts))
	}
	out := make([]Vector, len(vs))
	for i, v := range vs {
		if len(v) != dim {
			return nil, fmt.Errorf("%w: embedder %s returned a vector of %d dimensions, its model has %d",
				memory.ErrEmbedderMismatch, name, len(v), dim)
		}
		n, err := Normalize(v)
		if err != nil {
			return nil, fmt.Errorf("memory: embedder %s: %w", name, err)
		}
		out[i] = Vector{Model: name, Dim: dim, V: n}
	}
	return out, nil
}

// Normalize returns a copy of v scaled to unit length, a zero vector as it
// is, or an error for a value that is not finite.
func Normalize(v []float32) ([]float32, error) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if math.IsNaN(sum) || math.IsInf(sum, 0) {
		return nil, fmt.Errorf("a vector with a value that is not finite")
	}
	out := make([]float32, len(v))
	if sum == 0 {
		return out, nil
	}
	inv := 1 / math.Sqrt(sum)
	for i, x := range v {
		out[i] = float32(float64(x) * inv)
	}
	return out, nil
}

// Dot is the dot product of a and b, of equal length: their cosine
// similarity when both are normalized.
func Dot(a, b []float32) float64 {
	b = b[:len(a)]
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= len(a); i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	for ; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return float64(s0 + s1 + s2 + s3)
}

// Doc is a candidate of a semantic search: an item and its vector (zero
// Vector for an item stored without one).
type Doc struct {
	Item memory.Item
	Vec  Vector
}

// Check is nil when a vector of model and dim, of the item id, may be
// compared with q, and memory.ErrEmbedderMismatch naming the item and both
// models otherwise; model "" is an item with no vector.
func Check(q Vector, id, model string, dim int) error {
	switch {
	case model == "":
		return fmt.Errorf("%w: item %q has no vector; this index embeds with %s (%d dimensions): re-embed the index",
			memory.ErrEmbedderMismatch, id, q.Model, q.Dim)
	case model != q.Model || dim != q.Dim:
		return fmt.Errorf("%w: item %q was embedded with %s (%d dimensions); this index embeds with %s (%d): re-embed the index",
			memory.ErrEmbedderMismatch, id, model, dim, q.Model, q.Dim)
	}
	return nil
}

// Scored is a candidate of a semantic search whose similarity to the query
// is known.
type Scored struct {
	Item  memory.Item
	Score float64
}

// Rank returns the top k of docs that match filter, by the cosine similarity
// of their vector and q: best first, then the newest, then by ID. Every doc
// of the scope must be passed (not only those of the filter): one whose
// vector is not q's model's is memory.ErrEmbedderMismatch, whatever the
// filter.
func Rank(q Vector, docs []Doc, filter map[string]string, k int) ([]memory.Result, error) {
	cands := make([]Scored, 0, len(docs))
	for _, d := range docs {
		if err := Check(q, d.Item.ID, d.Vec.Model, d.Vec.Dim); err != nil {
			return nil, err
		}
		if len(d.Vec.V) != q.Dim {
			return nil, fmt.Errorf("%w: item %q has a vector of %d values, its model has %d",
				memory.ErrEmbedderMismatch, d.Item.ID, len(d.Vec.V), q.Dim)
		}
		if !memory.MatchesFilter(d.Item.Metadata, filter) {
			continue
		}
		cands = append(cands, Scored{d.Item, Dot(q.V, d.Vec.V)})
	}
	return Top(cands, k), nil
}

// Top orders cands best first, then the newest, then by ID, and returns
// the first k as semantic results, their scores held to [-1, 1] (float32
// rounding can take the cosine of a vector with itself just past 1).
func Top(cands []Scored, k int) []memory.Result {
	slices.SortFunc(cands, func(a, b Scored) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		if c := b.Item.Time.Compare(a.Item.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Item.ID, b.Item.ID)
	})
	out := make([]memory.Result, 0, min(k, len(cands)))
	for _, c := range cands[:min(k, len(cands))] {
		out = append(out, memory.Result{
			Item: c.Item.Clone(), Score: min(max(c.Score, -1), 1),
			Mode: memory.ModeSemantic, Found: []memory.Mode{memory.ModeSemantic},
		})
	}
	return out
}

// Fuse is reciprocal rank fusion of a keyword and a semantic ranking, each
// ordered best first: an item scores 1/(memory.RRFK + rank) in each ranking
// it is in, ranks starting at 1 and tied scores sharing the rank of the
// first of them, and the top k of the sums come first, then the newest,
// then by ID. Highlights come from the keyword ranking.
func Fuse(kw, sem []memory.Result, k int) []memory.Result {
	byID := map[string]int{}
	var out []memory.Result
	add := func(list []memory.Result, mode memory.Mode) {
		rank := 0
		for i, r := range list {
			if i == 0 || r.Score != list[i-1].Score {
				rank = i + 1
			}
			contrib := 1 / float64(memory.RRFK+rank)
			j, ok := byID[r.Item.ID]
			if !ok {
				j = len(out)
				byID[r.Item.ID] = j
				out = append(out, memory.Result{Item: r.Item, Mode: memory.ModeHybrid, Highlights: r.Highlights})
			}
			out[j].Score += contrib
			out[j].Found = append(out[j].Found, mode)
		}
	}
	add(kw, memory.ModeKeyword)
	add(sem, memory.ModeSemantic)
	slices.SortFunc(out, keyword.Compare)
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// Encode is v as a little-endian float32 blob.
func Encode(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

// Decode is the vector of dim values in b, which Encode made. A dim out of
// range or a blob of another length is an error, never a panic.
func Decode(b []byte, dim int) ([]float32, error) {
	if dim < 0 || dim > memory.MaxEmbedderDim || len(b) != 4*dim {
		return nil, fmt.Errorf("a vector of %d bytes, want %d values", len(b), dim)
	}
	v := make([]float32, dim)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}

// DotBlob is Dot(q, Decode(b, len(q))) without decoding b into a vector, so
// a scan of a scope holds no vector but the query's: the same sum, in the
// same order, as Dot. A blob of another length, or a sum that is not finite
// (a corrupt blob), is an error.
func DotBlob(q []float32, b []byte) (float64, error) {
	if len(b) != 4*len(q) {
		return 0, fmt.Errorf("a vector of %d bytes, want %d", len(b), 4*len(q))
	}
	at := func(i int) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:])) }
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= len(q); i += 4 {
		s0 += q[i] * at(i)
		s1 += q[i+1] * at(i+1)
		s2 += q[i+2] * at(i+2)
		s3 += q[i+3] * at(i+3)
	}
	for ; i < len(q); i++ {
		s0 += q[i] * at(i)
	}
	s := float64(s0 + s1 + s2 + s3)
	if math.IsNaN(s) || math.IsInf(s, 0) {
		return 0, errors.New("a vector with a value that is not finite")
	}
	return s, nil
}
