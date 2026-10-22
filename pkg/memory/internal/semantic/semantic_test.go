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
