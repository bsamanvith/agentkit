package inmem_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/inmem"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"
)

// TestModesComplement: keyword search finds none of the paraphrases that
// semantic search ranks first, and semantic search cannot leave out an item
// without the exact phrase, which keyword search does.
func TestModesComplement(t *testing.T) {
	t.Parallel()
	e := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	x, err := inmem.NewIndex(memory.IndexConfig{Embedder: e})
	if err != nil {
		t.Fatal(err)
	}
	ctx := memorytest.Ctx(t, "t", "")
	set := memorytest.HybridRelevance()
	for _, it := range set.Items {
		if err := x.Put(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range memorytest.SemanticRelevance().Queries {
		res, err := x.Search(ctx, memory.Query{Text: q.Query.Text, Mode: memory.ModeKeyword, K: memory.MaxK})
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 0 {
			t.Errorf("keyword %q found %v: the paraphrase set must share no word", q.Query.Text, memorytest.IDs(res))
		}
	}
	q := memory.Query{Text: `"blue green deployment"`, K: memory.MaxK}
	for mode, wantColours := range map[memory.Mode]bool{memory.ModeKeyword: false, memory.ModeSemantic: true, memory.ModeHybrid: true} {
		q.Mode = mode
		res, err := x.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(memorytest.IDs(res), "deploy-colours"); got != wantColours {
			t.Errorf("%s %s: has deploy-colours %v, want %v", mode, q.Text, got, wantColours)
		}
	}
}

// TestFakeEmbedder: its listed paraphrases are close, unrelated pairs far,
// its vectors are not normalized, and it is deterministic.
func TestFakeEmbedder(t *testing.T) {
	t.Parallel()
	e := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	cos := func(a, b string) float64 {
		vs, err := e.Embed(t.Context(), []string{a, b}, memory.PurposeDocument)
		if err != nil {
			t.Fatal(err)
		}
		var dot, na, nb float64
		for i := range vs[0] {
			dot += float64(vs[0][i] * vs[1][i])
			na += float64(vs[0][i] * vs[0][i])
			nb += float64(vs[1][i] * vs[1][i])
		}
		return dot / math.Sqrt(na*nb)
	}
	for _, p := range memorytest.FakeParaphrases {
		if c := cos(p[0], p[1]); c < 0.5 {
			t.Errorf("paraphrases %q, %q: cosine %v, want over 0.5", p[0], p[1], c)
		}
	}
	for _, p := range memorytest.FakeUnrelated {
		if c := cos(p[0], p[1]); c > 0.3 {
			t.Errorf("unrelated %q, %q: cosine %v, want under 0.3", p[0], p[1], c)
		}
	}
	a, b := memorytest.FakeVector("pet dog pet dog", 64), memorytest.FakeVector("pet dog pet dog", 64)
	if !slices.Equal(a, b) {
		t.Error("FakeVector is not deterministic")
	}
	var n float64
	for _, x := range a {
		n += float64(x * x)
	}
	if n == 1 {
		t.Error("FakeVector is normalized: the suite then cannot catch an index that does not normalize")
	}
}

// BenchmarkSearch10k: searches over 10,000 items of one scope, with
// 1024-dimension vectors (Voyage's and many models' size).
func BenchmarkSearch10k(b *testing.B) {
	e := memorytest.NewFakeEmbedder("bench", 1024)
	x, err := inmem.NewIndex(memory.IndexConfig{Embedder: e})
	if err != nil {
		b.Fatal(err)
	}
	ctx := memorytest.Ctx(b, "t", "")
	rng := rand.New(rand.NewPCG(1, 2))
	words := strings.Fields("deploy postgres backup flight aisle seat dog puppy salary pay weather rain lisbon garden basil car " +
		"vehicle meeting standup laptop computer invoice cancel subscription allergy peanuts diet vegetarian team lunch")
	for i := range 10_000 {
		var text []string
		for range 8 + rng.IntN(16) {
			text = append(text, words[rng.IntN(len(words))])
		}
		text = append(text, fmt.Sprintf("w%d", i))
		if err := x.Put(ctx, memory.Item{ID: fmt.Sprintf("i%05d", i), Text: strings.Join(text, " ")}); err != nil {
			b.Fatal(err)
		}
	}
	for _, mode := range []memory.Mode{memory.ModeKeyword, memory.ModeSemantic, memory.ModeHybrid} {
		b.Run(string(mode), func(b *testing.B) {
			b.ReportAllocs()
			q := memory.Query{Text: "flight preferences for the dog", K: 10, Mode: mode}
			for b.Loop() {
				if _, err := x.Search(ctx, q); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
