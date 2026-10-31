package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farm/sqlite"
)

func open(t *testing.T, path string) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func TestConformance(t *testing.T) {
	t.Parallel()
	farmtest.RunStore(t, func(t *testing.T) farm.Store { return open(t, filepath.Join(t.TempDir(), "farm.db")) })
}

func TestFarmsSurviveReopening(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "farm.db")
	ctx := context.Background()
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	want := farmtest.Sample("a", time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
	if err := s.Create(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := open(t, path).Get(ctx, "a")
	if err != nil {
		t.Fatalf("Get after reopening: %v", err)
	}
	if got.Name != want.Name || len(got.Boundary.Corners) != 4 {
		t.Errorf("after reopening: %+v", got)
	}
}
