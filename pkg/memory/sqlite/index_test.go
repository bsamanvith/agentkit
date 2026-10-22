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

// TestIndexMatchesInMemory: the relevance set's items and queries, and some
// more, give the same results as the in-memory index, scores included.
func TestIndexMatchesInMemory(t *testing.T) {
	t.Parallel()
	x := openIndex(t, filepath.Join(t.TempDir(), "index.db"), memory.IndexConfig{})
	ref, err := inmem.NewIndex(memory.IndexConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := memorytest.Ctx(t, "t", "")
	set := memorytest.KeywordRelevance()
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
		q := memory.Query{Text: text, K: memory.MaxK}
		got, err := x.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		want, err := ref.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s:\nsqlite %v\ninmem  %v", text, got, want)
		}
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
