package semantic_test

import (
	"math"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/semantic"
)

func res(id string, score float64, mode memory.Mode) memory.Result {
	return memory.Result{Item: memory.Item{ID: id, Text: id}, Score: score, Mode: mode, Found: []memory.Mode{mode}}
}

// TestFuseIsRRF: scores worked by hand with k = 60; tied scores share a
// rank; found-by lists both modes in order.
func TestFuseIsRRF(t *testing.T) {
	t.Parallel()
	kw := []memory.Result{res("a", 9, memory.ModeKeyword), res("b", 5, memory.ModeKeyword), res("c", 5, memory.ModeKeyword)}
	kw[0].Highlights = []string{"x"}
	sem := []memory.Result{res("c", 0.9, memory.ModeSemantic), res("d", 0.8, memory.ModeSemantic), res("a", 0.1, memory.ModeSemantic)}
	got := semantic.Fuse(kw, sem, 10)
	want := map[string]float64{
		"a": 1.0/61 + 1.0/63,
		"b": 1.0 / 62,
		"c": 1.0/62 + 1.0/61, // tied with b at rank 2
		"d": 1.0 / 62,
	}
	order := []string{"c", "a", "b", "d"}
	for i, r := range got {
		if r.Item.ID != order[i] || math.Abs(r.Score-want[r.Item.ID]) > 1e-15 || r.Mode != memory.ModeHybrid {
			t.Errorf("result %d = %s %v %s, want %s %v hybrid", i, r.Item.ID, r.Score, r.Mode, order[i], want[order[i]])
		}
	}
	if !slices.Equal(got[1].Found, []memory.Mode{memory.ModeKeyword, memory.ModeSemantic}) || !slices.Equal(got[1].Highlights, []string{"x"}) {
		t.Errorf("a: found %v, highlights %v", got[1].Found, got[1].Highlights)
	}
	if !slices.Equal(got[3].Found, []memory.Mode{memory.ModeSemantic}) {
		t.Errorf("d: found %v", got[3].Found)
	}
	if len(semantic.Fuse(kw, sem, 2)) != 2 {
		t.Error("Fuse ignores k")
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	v, err := semantic.Normalize([]float32{3, 4})
	if err != nil || math.Abs(float64(v[0])-0.6) > 1e-7 || math.Abs(float64(v[1])-0.8) > 1e-7 {
		t.Errorf("Normalize(3, 4) = %v, %v", v, err)
	}
	if z, err := semantic.Normalize([]float32{0, 0}); err != nil || z[0] != 0 || z[1] != 0 {
		t.Errorf("Normalize(0) = %v, %v", z, err)
	}
	if _, err := semantic.Normalize([]float32{float32(math.NaN())}); err == nil {
		t.Error("Normalize(NaN) succeeded")
	}
}

func TestEncodeDecode(t *testing.T) {
	t.Parallel()
	v := []float32{1, -0.5, float32(math.SmallestNonzeroFloat32), 3e38}
	got, err := semantic.Decode(semantic.Encode(v), len(v))
	if err != nil || !slices.Equal(got, v) {
		t.Errorf("Decode(Encode(%v)) = %v, %v", v, got, err)
	}
	if _, err := semantic.Decode(semantic.Encode(v), 3); err == nil {
		t.Error("Decode of the wrong length succeeded")
	}
}

// TestDecodeBadDim: a dimension a corrupt file gives (negative, past
// MaxEmbedderDim, or one whose byte length overflows) is an error, not a
// panic.
func TestDecodeBadDim(t *testing.T) {
	t.Parallel()
	for _, dim := range []int{-1, memory.MaxEmbedderDim + 1, 1<<62 + 1} {
		if _, err := semantic.Decode([]byte{0, 0, 0, 0}, dim); err == nil {
			t.Errorf("Decode(4 bytes, %d) succeeded", dim)
		}
	}
}

// TestDotBlob: the same sum as Dot of the decoded vector, to the bit (so
// the SQLite index scores as the in-memory one), and an error for a blob
// of another length or values that are not finite.
func TestDotBlob(t *testing.T) {
	t.Parallel()
	q, _ := semantic.Normalize([]float32{0.3, -1.7, 2.2, 0.01, 5, -0.4, 0.9})
	v, _ := semantic.Normalize([]float32{1.1, 0.2, -0.3, 4, 0.5, 0.6, -2})
	got, err := semantic.DotBlob(q, semantic.Encode(v))
	if want := semantic.Dot(q, v); err != nil || got != want {
		t.Errorf("DotBlob = %v, %v; Dot = %v", got, err, want)
	}
	if _, err := semantic.DotBlob(q, semantic.Encode(v[:6])); err == nil {
		t.Error("DotBlob of a short blob succeeded")
	}
	v[2] = float32(math.Inf(1))
	if _, err := semantic.DotBlob(q, semantic.Encode(v)); err == nil {
		t.Error("DotBlob of an infinite value succeeded")
	}
}

// TestTopClamps: float32 rounding past 1 or -1 is held to [-1, 1].
func TestTopClamps(t *testing.T) {
	t.Parallel()
	got := semantic.Top([]semantic.Scored{{Item: memory.Item{ID: "a"}, Score: 1.0000001}, {Item: memory.Item{ID: "b"}, Score: -1.0000001}}, 10)
	if len(got) != 2 || got[0].Score != 1 || got[1].Score != -1 {
		t.Errorf("Top = %v", got)
	}
}
