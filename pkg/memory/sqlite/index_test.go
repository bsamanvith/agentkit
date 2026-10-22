package sqlite_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/inmem"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"
	"github.com/bsamanvith/agentkit/pkg/memory/sqlite"
)

func openIndex(t *testing.T, path string, cfg memory.IndexConfig) *sqlite.Index {
	t.Helper()
	x, err := sqlite.OpenIndex(t.Context(), path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = x.Close() })
	return x
}

// TestIndexConformance: the suite, with its keyword tests and the keyword
// relevance set, on FTS5.
func TestIndexConformance(t *testing.T) {
	t.Parallel()
	memorytest.RunIndex(t, func(t *testing.T, cfg memory.IndexConfig) memory.Index {
		return openIndex(t, filepath.Join(t.TempDir(), "index.db"), cfg)
	})
}

// TestIndexMatchesInMemory: the relevance sets' items and queries, and some
// more, give the same results as the in-memory index in every mode, scores
// included.
func TestIndexMatchesInMemory(t *testing.T) {
	t.Parallel()
	e := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	x := openIndex(t, filepath.Join(t.TempDir(), "index.db"), memory.IndexConfig{Embedder: e})
	ref, err := inmem.NewIndex(memory.IndexConfig{Embedder: e})
	if err != nil {
		t.Fatal(err)
	}
	ctx := memorytest.Ctx(t, "t", "")
	set := memorytest.HybridRelevance()
	for _, it := range set.Items {
		for _, idx := range []memory.Index{x, ref} {
			if err := idx.Put(ctx, it); err != nil {
				t.Fatal(err)
			}
		}
	}
	queries := []string{"the", "team lunch", `"the team"`, "invoice", "inv 2024", "渋谷", "pg_dump", "cert-manager", "café lumière"}
	for _, q := range set.Queries {
		queries = append(queries, q.Query.Text)
	}
	for _, text := range queries {
		for _, mode := range x.Modes() {
			q := memory.Query{Text: text, K: memory.MaxK, Mode: mode}
			got, err := x.Search(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			want, err := ref.Search(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("%s %s:\nsqlite %v\ninmem  %v", mode, text, got, want)
			}
		}
	}
}

// TestIndexEmbedderChange: the file keeps each vector's model and
// dimension; another embedder is refused until Reembed.
func TestIndexEmbedderChange(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.db")
	memorytest.RunEmbedderChange(t, func(t *testing.T, cfg memory.IndexConfig) memory.Index {
		return openIndex(t, path, cfg)
	})
}

// TestIndexUpgradesVersion1: a file T059 wrote (no vectors) opens, keeps its
// items and keyword search, and needs Reembed for semantic search.
func TestIndexUpgradesVersion1(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.db")
	ctx := memorytest.Ctx(t, "t", "")
	old := openIndex(t, path, memory.IndexConfig{})
	if err := old.Put(ctx, memory.Item{ID: "a", Text: "nut allergy"}); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.MakeIndexVersion1(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	e := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	x := openIndex(t, path, memory.IndexConfig{Embedder: e})
	if res, err := x.Search(ctx, memory.Query{Text: "allergy", Mode: memory.ModeKeyword}); err != nil || len(res) != 1 {
		t.Errorf("keyword search after the upgrade = %v, %v", res, err)
	}
	if _, err := x.Search(ctx, memory.Query{Text: "peanuts", Mode: memory.ModeSemantic}); !errors.Is(err, memory.ErrEmbedderMismatch) {
		t.Errorf("semantic search of an item with no vector = %v, want ErrEmbedderMismatch", err)
	}
	if n, err := x.Reembed(t.Context()); err != nil || n != 1 {
		t.Fatalf("Reembed = %d, %v", n, err)
	}
	if res, err := x.Search(ctx, memory.Query{Text: "peanuts", Mode: memory.ModeSemantic}); err != nil || len(res) != 1 {
		t.Errorf("semantic search after Reembed = %v, %v", res, err)
	}
	if _, err := sqlite.OpenIndex(t.Context(), path, memory.IndexConfig{}); err != nil {
		t.Errorf("reopening the upgraded file: %v", err)
	}
}

// TestIndexSharesAFileWithAStore: a Store and an Index on one file, opened in
// either order, keep their own tables and versions.
func TestIndexSharesAFileWithAStore(t *testing.T) {
	t.Parallel()
	for _, indexFirst := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "memory.db")
		ctx := memorytest.Ctx(t, "t", "")
		var x *sqlite.Index
		if indexFirst {
			x = openIndex(t, path, memory.IndexConfig{})
		}
		s := open(t, path, memory.Config{})
		if !indexFirst {
			x = openIndex(t, path, memory.IndexConfig{})
		}
		if err := s.Create(ctx, "/memories/a.md", "note"); err != nil {
			t.Fatal(err)
		}
		if err := x.Put(ctx, memory.Item{ID: "a", Text: "an item"}); err != nil {
			t.Fatal(err)
		}
		again := openIndex(t, path, memory.IndexConfig{})
		if it, err := again.Get(ctx, "a"); err != nil || it.Text != "an item" {
			t.Errorf("index first %v: reopened Get = %+v, %v", indexFirst, it, err)
		}
		if v, err := open(t, path, memory.Config{}).View(ctx, "/memories/a.md"); err != nil || v.Text != "note" {
			t.Errorf("index first %v: reopened View = %+v, %v", indexFirst, v, err)
		}
	}
}

func TestOpenIndexRefusesANewerSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.db")
	x := openIndex(t, path, memory.IndexConfig{})
	if err := sqlite.SetIndexSchemaVersion(t.Context(), x, 99); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.OpenIndex(t.Context(), path, memory.IndexConfig{}); !errors.Is(err, sqlite.ErrSchemaTooNew) {
		t.Errorf("OpenIndex of a newer schema = %v, want ErrSchemaTooNew", err)
	}
	if _, err := sqlite.OpenIndex(t.Context(), path, memory.IndexConfig{Scope: "team"}); !errors.Is(err, memory.ErrInvalid) {
		t.Errorf("OpenIndex with a bad scope = %v, want ErrInvalid", err)
	}
}
