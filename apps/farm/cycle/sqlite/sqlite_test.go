package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/cycletest"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/sqlite"
)

func open(t *testing.T, path string) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore(t *testing.T) {
	t.Parallel()
	cycletest.RunStore(t, func(t *testing.T) cycle.Store { return open(t, filepath.Join(t.TempDir(), "farm.db")) })
}

// A record survives closing and opening the file again.
func TestReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "farm.db")
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	r := cycle.Record{Farm: "f1", FutureOff: true}
	r.Slots[1].Method = cycle.Organic
	if _, err := s.Put(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := open(t, path).Get(ctx, "f1")
	if err != nil || got.Version != 1 || !got.FutureOff || got.Slots[1].Method != cycle.Organic {
		t.Errorf("after reopening: %+v, %v", got, err)
	}
}
