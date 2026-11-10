package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/work"
	"github.com/bsamanvith/agentkit/apps/farm/work/sqlite"
	"github.com/bsamanvith/agentkit/apps/farm/work/worktest"
)

func TestStore(t *testing.T) {
	t.Parallel()
	worktest.RunStore(t, func(t *testing.T) work.Store {
		s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "farm.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

// Events survive closing and opening the file again.
func TestReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "farm.db")
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, work.Event{ID: "e1", Farm: "f1", Kind: work.Done, Task: "t", Day: "2026-07-01"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.Events(ctx, "f1"); err != nil || len(got) != 1 || got[0].ID != "e1" {
		t.Fatalf("after reopen: %v, %v", got, err)
	}
}
