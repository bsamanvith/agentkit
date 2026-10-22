package sqlite_test

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"
	"github.com/bsamanvith/agentkit/pkg/memory/sqlite"
)

// TestIndexCorruptVector: a stored vector with a wrong dimension or length,
// or values that are not finite, fails the search with an error (never a
// panic, whatever the dimension says), and Reembed replaces the ones of the
// wrong dimension or length.
func TestIndexCorruptVector(t *testing.T) {
	t.Parallel()
	e := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	nan := make([]byte, 4*memorytest.DefaultFakeDim)
	for i := 0; i < len(nan); i += 4 {
		binary.LittleEndian.PutUint32(nan[i:], math.Float32bits(float32(math.NaN())))
	}
	for name, c := range map[string]struct {
		dim     int
		blob    []byte
		reembed bool // Reembed sees it as stale
	}{
		"overflowing dim": {1<<62 + 1, []byte{0, 0, 0, 0}, true},
		"negative dim":    {-1, []byte{}, true},
		"short blob":      {memorytest.DefaultFakeDim, []byte{1, 2, 3}, true},
		"not finite":      {memorytest.DefaultFakeDim, nan, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			x := openIndex(t, filepath.Join(t.TempDir(), "index.db"), memory.IndexConfig{Embedder: e})
			ctx := memorytest.Ctx(t, "t", "")
			for _, it := range []memory.Item{{ID: "a", Text: "nut allergy"}, {ID: "b", Text: "aisle seat"}} {
				if err := x.Put(ctx, it); err != nil {
					t.Fatal(err)
				}
			}
			if err := sqlite.SetVectorBlob(t.Context(), x, "a", c.dim, c.blob); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []memory.Mode{memory.ModeSemantic, memory.ModeHybrid} {
				if res, err := x.Search(ctx, memory.Query{Text: "peanuts", Mode: mode}); err == nil {
					t.Errorf("%s search of a corrupt vector = %v, want an error", mode, res)
				}
			}
			if !c.reembed {
				return
			}
			if n, err := x.Reembed(t.Context()); err != nil || n != 1 {
				t.Fatalf("Reembed = %d, %v; want 1 (the broken vector)", n, err)
			}
			if res, err := x.Search(ctx, memory.Query{Text: "peanuts", Mode: memory.ModeSemantic}); err != nil || len(res) != 2 {
				t.Errorf("semantic search after Reembed = %v, %v", res, err)
			}
		})
	}
}

// TestIndexVectorsFollowOtherWriters: a writer that knows nothing of
// vectors (a version 1 binary that had the file open when another upgraded
// it) cannot leave a vector of an old text: an edited item has none, which
// a semantic search reports, and a deleted item's vector goes with it.
func TestIndexVectorsFollowOtherWriters(t *testing.T) {
	t.Parallel()
	e := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	x := openIndex(t, filepath.Join(t.TempDir(), "index.db"), memory.IndexConfig{Embedder: e})
	ctx := memorytest.Ctx(t, "t", "")
	for _, it := range []memory.Item{{ID: "a", Text: "nut allergy"}, {ID: "b", Text: "aisle seat"}} {
		if err := x.Put(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	if err := sqlite.ExecIndex(t.Context(), x, `DELETE FROM memory_items WHERE id = 'b'`); err != nil {
		t.Fatal(err)
	}
	if n, err := sqlite.CountVectors(t.Context(), x); err != nil || n != 1 {
		t.Errorf("vectors after another writer's delete = %d, %v; want 1", n, err)
	}
	if err := sqlite.ExecIndex(t.Context(), x, `UPDATE memory_items SET metadata = '{"k":"v"}' WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Search(ctx, memory.Query{Text: "peanuts", Mode: memory.ModeSemantic}); err != nil {
		t.Errorf("semantic search after another writer's metadata edit: %v", err)
	}
	if err := sqlite.ExecIndex(t.Context(), x, `UPDATE memory_items SET text = 'trip to rome' WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Search(ctx, memory.Query{Text: "peanuts", Mode: memory.ModeSemantic}); !errors.Is(err, memory.ErrEmbedderMismatch) {
		t.Errorf("semantic search after another writer's edit = %v, want ErrEmbedderMismatch (no vector)", err)
	}
	if n, err := x.Reembed(t.Context()); err != nil || n != 1 {
		t.Errorf("Reembed = %d, %v; want 1", n, err)
	}
}

// BenchmarkSemanticSearch5k: semantic searches of a scope of 5,000 items
// with 1024-dimension vectors (20 MB of them): the bytes allocated per
// search are what the scan holds, which must not grow with the vectors.
func BenchmarkSemanticSearch5k(b *testing.B) {
	e := memorytest.NewFakeEmbedder("bench", 1024)
	x, err := sqlite.OpenIndex(b.Context(), filepath.Join(b.TempDir(), "index.db"), memory.IndexConfig{Embedder: e})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = x.Close() })
	ctx := memorytest.Ctx(b, "t", "")
	words := strings.Fields("deploy postgres backup flight aisle seat dog puppy salary pay weather rain lisbon garden basil car")
	for i := range 5_000 {
		text := fmt.Sprintf("%s %s %s w%d", words[i%len(words)], words[(i/3)%len(words)], words[(i/7)%len(words)], i)
		if err := x.Put(ctx, memory.Item{ID: fmt.Sprintf("i%05d", i), Text: text}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	q := memory.Query{Text: "flight preferences for the dog", K: 10, Mode: memory.ModeSemantic}
	for b.Loop() {
		if _, err := x.Search(ctx, q); err != nil {
			b.Fatal(err)
		}
	}
}

// TestIndexUpgradeConcurrentOpens: processes opening a version 1 file at
// once all succeed; one upgrades it, in one transaction, and the others see
// version 2.
func TestIndexUpgradeConcurrentOpens(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.db")
	ctx := memorytest.Ctx(t, "t", "")
	old, err := sqlite.OpenIndex(t.Context(), path, memory.IndexConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Put(ctx, memory.Item{ID: "a", Text: "nut allergy"}); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.MakeIndexVersion1(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	e := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	const n = 8
	xs := make([]*sqlite.Index, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { xs[i], errs[i] = sqlite.OpenIndex(t.Context(), path, memory.IndexConfig{Embedder: e}) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		t.Cleanup(func() { _ = xs[i].Close() })
	}
	if got, err := xs[0].Reembed(t.Context()); err != nil || got != 1 {
		t.Fatalf("Reembed = %d, %v", got, err)
	}
	for i, x := range xs {
		if res, err := x.Search(ctx, memory.Query{Text: "peanuts", Mode: memory.ModeSemantic}); err != nil || len(res) != 1 {
			t.Errorf("open %d: semantic search = %v, %v", i, res, err)
		}
	}
}
