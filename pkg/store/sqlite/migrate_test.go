package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

func tenantCtx(t testing.TB, id string) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatalf("NewTenant: %v", err)
	}
	return tenancy.With(context.Background(), tn)
}

func prompt(seq int64, text string) store.Entry {
	return store.Entry{Seq: seq, Kind: store.KindPrompt, Messages: []llm.Message{llm.User(llm.Text{Text: text})}}
}

// A file written by an older binary (here, one that knew only the first
// migration, the conversation log) is upgraded in place when a newer binary
// opens it: what it held is still there, and the later tables work.
func TestOpenUpgradesAnOlderSchema(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	path := filepath.Join(t.TempDir(), "agentkit.db")

	old, err := sqlite.Open(ctx, path, sqlite.WithMigrations(1))
	if err != nil {
		t.Fatalf("Open at version 1: %v", err)
	}
	for i, text := range []string{"one", "two"} {
		if err := old.Append(ctx, "c", prompt(int64(i+1), text)); err != nil {
			t.Fatalf("Append at version 1: %v", err)
		}
	}
	if _, err := old.Create(ctx, "r", "c"); err == nil {
		t.Fatal("Create at version 1 succeeded; the old schema has no runs table, so the test proves nothing")
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s := openPath(t, path)
	page, err := s.History(ctx, "c", store.Page{})
	if err != nil || len(page.Entries) != 2 || page.Entries[1].Messages[0].Text() != "two" {
		t.Fatalf("History after the upgrade = %+v, %v; want the two entries written before it", page.Entries, err)
	}
	if _, err := s.Create(ctx, "r", "c"); err != nil {
		t.Errorf("Create after the upgrade: %v", err)
	}
	if err := s.Save(ctx, "r", flow.Checkpoint{RunID: "r", Shape: "v1:x"}); err != nil {
		t.Errorf("Save after the upgrade: %v", err)
	}
}

// A binary refuses a file that a newer binary has migrated past what it
// knows, and leaves the file as it was.
func TestOpenRefusesANewerSchema(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	path := filepath.Join(t.TempDir(), "agentkit.db")
	s := openPath(t, path)
	if err := s.Append(ctx, "c", prompt(1, "kept")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	for _, known := range []int{1, 2} {
		old, err := sqlite.Open(ctx, path, sqlite.WithMigrations(known))
		if !errors.Is(err, sqlite.ErrSchemaTooNew) {
			if old != nil {
				_ = old.Close()
			}
			t.Fatalf("Open by a binary that knows %d migrations = %v, want ErrSchemaTooNew", known, err)
		}
		if !strings.Contains(err.Error(), "knows up to") {
			t.Errorf("error %q does not say which versions disagree", err)
		}
	}

	page, err := s.History(ctx, "c", store.Page{})
	if err != nil || len(page.Entries) != 1 {
		t.Errorf("History after the refused opens = %+v, %v; want the entry intact", page.Entries, err)
	}
}

// Processes starting at once on a new file migrate it once between them.
func TestConcurrentOpensMigrateOnce(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "agentkit.db")
	const n = 4
	var wg sync.WaitGroup
	stores := make([]*sqlite.Store, n)
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() { stores[i], errs[i] = sqlite.Open(context.Background(), path, sqlite.WithClock(clock.Real())) })
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Errorf("Open %d: %v", i, errs[i])
			continue
		}
		t.Cleanup(func() { _ = stores[i].Close() })
	}
	if t.Failed() {
		return
	}
	ctx := tenantCtx(t, "acme")
	if _, err := stores[0].Create(ctx, "r", "c"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r, err := stores[n-1].Get(ctx, "r"); err != nil || r.ID != "r" {
		t.Errorf("Get through another store = %+v, %v", r, err)
	}
}
