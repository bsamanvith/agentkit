package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
)

// A panic inside a write transaction does not leave the writer's one
// connection taken: the next write goes through.
func TestPanicInWriteReleasesTheWriter(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "agentkit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("fn's panic did not reach the caller")
			}
		}()
		_ = sqlite.Write(ctx, s, func(*sql.Tx) error { panic("boom") })
	}()

	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlite.Write(wctx, s, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("write after a panic: %v", err)
	}
}
