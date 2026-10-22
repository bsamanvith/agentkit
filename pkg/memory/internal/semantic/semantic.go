// Package semantic ranks the items of a semantic search by cosine
// similarity, and fuses a keyword and a semantic ranking into a hybrid one,
// the same way for every backend: a backend keeps the vectors and finds the
// scope's items; Rank and Fuse score, filter, order and cut them.
package semantic

import (
	"cmp"
	"context"
	"encoding/binary"
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

// From reports whether v was made by the model name with dimension dim.
func (v Vector) From(name string, dim int) bool {
	return v.Model == name && v.Dim == dim && len(v.V) == dim
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

func mismatch(q Vector, d Doc) error {
	if d.Vec.Model == "" {
		return fmt.Errorf("%w: item %q has no vector; this index embeds with %s (%d dimensions): re-embed the index",
			memory.ErrEmbedderMismatch, d.Item.ID, q.Model, q.Dim)
	}
	return fmt.Errorf("%w: item %q was embedded with %s (%d dimensions); this index embeds with %s (%d): re-embed the index",
		memory.ErrEmbedderMismatch, d.Item.ID, d.Vec.Model, d.Vec.Dim, q.Model, q.Dim)
}

// Rank returns the top k of docs that match filter, by the cosine similarity
// of their vector and q: best first, then the newest, then by ID. Every doc
// of the scope must be passed (not only those of the filter): one whose
// vector is not q's model's is memory.ErrEmbedderMismatch, whatever the
// filter.
func Rank(q Vector, docs []Doc, filter map[string]string, k int) ([]memory.Result, error) {
	type scored struct {
		i     int
		score float64
	}
	cands := make([]scored, 0, len(docs))
	for i, d := range docs {
		if !d.Vec.From(q.Model, q.Dim) {
			return nil, mismatch(q, d)
		}
		if !memory.MatchesFilter(d.Item.Metadata, filter) {
			continue
		}
		cands = append(cands, scored{i, Dot(q.V, d.Vec.V)})
	}
	slices.SortFunc(cands, func(a, b scored) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		x, y := &docs[a.i].Item, &docs[b.i].Item
		if c := y.Time.Compare(x.Time); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	out := make([]memory.Result, 0, min(k, len(cands)))
	for _, c := range cands[:min(k, len(cands))] {
		out = append(out, memory.Result{
			Item: docs[c.i].Item.Clone(), Score: c.score,
			Mode: memory.ModeSemantic, Found: []memory.Mode{memory.ModeSemantic},
		})
	}
	return out, nil
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

// Decode is the vector of dim values in b, which Encode made.
func Decode(b []byte, dim int) ([]float32, error) {
	if len(b) != 4*dim {
		return nil, fmt.Errorf("a vector of %d bytes, want %d", len(b), 4*dim)
	}
	v := make([]float32, dim)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}
