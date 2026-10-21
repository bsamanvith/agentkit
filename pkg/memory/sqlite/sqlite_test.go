package sqlite_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"
	"github.com/bsamanvith/agentkit/pkg/memory/sqlite"
)

func open(t *testing.T, path string, cfg memory.Config) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(t.Context(), path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestConformance(t *testing.T) {
	t.Parallel()
	memorytest.RunStore(t, func(t *testing.T, cfg memory.Config) memory.Store {
		return open(t, filepath.Join(t.TempDir(), "memory.db"), cfg)
	})
}

// TestFilesSurviveReopening: what one store wrote, another opened on the same
// file later reads; a second store open on it at once sees the first's
// writes.
func TestFilesSurviveReopening(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "memory.db")
	ctx := memorytest.Ctx(t, "t1", "")
	a := open(t, path, memory.Config{})
	if err := a.Create(ctx, "/memories/prefs.md", "units: metric\n"); err != nil {
		t.Fatal(err)
	}
	b := open(t, path, memory.Config{})
	if v, err := b.View(ctx, "/memories/prefs.md"); err != nil || v.Text != "units: metric\n" {
		t.Errorf("second store's view = %+v, %v", v, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	c := open(t, path, memory.Config{})
	if err := c.Insert(ctx, "/memories/prefs.md", 1, "tone: brief"); err != nil {
		t.Fatal(err)
	}
	if v, err := b.View(ctx, "/memories/prefs.md"); err != nil || v.Text != "units: metric\ntone: brief\n" {
		t.Errorf("view after another store's edit = %+v, %v", v, err)
	}
}

func TestOpenRefusesANewerSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "memory.db")
	s := open(t, path, memory.Config{})
	if err := sqlite.SetSchemaVersion(t.Context(), s, 99); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.Open(t.Context(), path, memory.Config{}); !errors.Is(err, sqlite.ErrSchemaTooNew) {
		t.Errorf("Open of a newer schema = %v, want ErrSchemaTooNew", err)
	}
}
