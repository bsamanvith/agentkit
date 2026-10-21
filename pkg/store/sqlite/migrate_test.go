package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	// The entries an older binary would have appended. This binary's Append
	// counts them on the conversation's record, a later table, so they are
	// written as rows.
	for i, text := range []string{"one", "two"} {
		msgs, err := json.Marshal(prompt(int64(i+1), text).Messages)
		if err != nil {
			t.Fatal(err)
		}
		if err := sqlite.Exec(old, `INSERT INTO entries (tenant, conversation, seq, kind, messages, created_at)
			VALUES ('acme', 'c', ?, 'prompt', ?, ?)`, i+1, string(msgs), i+1); err != nil {
			t.Fatalf("entry %d at version 1: %v", i+1, err)
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
	if c, err := s.CreateConversation(ctx, store.Conversation{ID: "c", Agent: "a"}); err != nil || c.Messages != 2 {
		t.Errorf("CreateConversation after the upgrade = %+v, %v; want the two messages written before it counted", c, err)
	}
}

// Migration 0005 (T100) makes the runs a binary from before it wrote
// background runs: they read back as not interactive, are listed as
// resumable as before, and can be claimed.
func TestRunsFromBeforeInteractiveStayBackgroundRuns(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	path := filepath.Join(t.TempDir(), "agentkit.db")
	old, err := sqlite.Open(ctx, path, sqlite.WithMigrations(4))
	if err != nil {
		t.Fatalf("Open at version 4: %v", err)
	}
	// The runs an older binary would have written: one interrupted, one
	// failed and due. This binary's queries need the new column, so they
	// are written as rows.
	for _, q := range []string{
		`INSERT INTO runs (tenant, id, conversation, status, created_at) VALUES ('acme', 'interrupted', 'c1', 'running', 1)`,
		`INSERT INTO runs (tenant, id, conversation, status, retryable, attempts, error, created_at)
			VALUES ('acme', 'due', 'c2', 'failed', 1, 1, 'overloaded', 2)`,
	} {
		if err := sqlite.Exec(old, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s := openPath(t, path)
	runs, err := s.ListResumable(ctx, time.Unix(0, 3), 0)
	if err != nil || len(runs) != 2 || runs[0].ID != "interrupted" || runs[1].ID != "due" {
		t.Fatalf("ListResumable after the upgrade = %+v, %v; want both old runs", runs, err)
	}
	for _, r := range runs {
		if r.Interactive {
			t.Errorf("old run %q reads back as interactive", r.ID)
		}
		if _, err := s.Claim(ctx, r.ID, "worker", time.Minute); err != nil {
			t.Errorf("Claim of old run %q: %v", r.ID, err)
		}
	}
}

// Migration 0009 (T099) puts the runs a binary from before it wrote in the
// default lane at priority 0, starting when they were created: they read
// back so, are resumable as before, and ClaimNext takes them in due order.
func TestRunsFromBeforeTheQueueAreQueued(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	path := filepath.Join(t.TempDir(), "agentkit.db")
	old, err := sqlite.Open(ctx, path, sqlite.WithMigrations(8))
	if err != nil {
		t.Fatalf("Open at version 8: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO runs (tenant, id, conversation, status, created_at) VALUES ('acme', 'interrupted', 'c1', 'running', 1)`,
		`INSERT INTO runs (tenant, id, conversation, status, retryable, attempts, error, created_at)
			VALUES ('acme', 'due', 'c2', 'failed', 1, 1, 'overloaded', 2)`,
		`INSERT INTO runs (tenant, id, conversation, status, created_at, interactive) VALUES ('acme', 'chat', 'c3', 'running', 3, 1)`,
		`INSERT INTO runs (tenant, id, conversation, status, created_at, finished_at) VALUES ('globex', 'done', 'c4', 'completed', 4, 5)`,
	} {
		if err := sqlite.Exec(old, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s := openPath(t, path)
	for _, id := range []string{"interrupted", "due", "chat"} {
		r, err := s.Get(ctx, id)
		if err != nil || r.Lane != "" || r.Priority != 0 || !r.StartAt.Equal(r.CreatedAt) {
			t.Errorf("old run %q after the upgrade = %+v, %v; want the default lane, priority 0, starting at its creation", id, r, err)
		}
	}
	runs, err := s.ListResumable(ctx, time.Unix(0, 3), 0)
	if err != nil || len(runs) != 2 || runs[0].ID != "interrupted" || runs[1].ID != "due" {
		t.Fatalf("ListResumable after the upgrade = %+v, %v; want both old background runs", runs, err)
	}
	admin := sqlite.Admin(s)
	for _, want := range []string{"interrupted", "due"} {
		tr, err := admin.ClaimNext(context.Background(), []string{""}, "worker", time.Minute)
		if err != nil || tr.Run.ID != want {
			t.Fatalf("ClaimNext after the upgrade = %+v, %v; want %q", tr.Run, err, want)
		}
	}
	if tr, err := admin.ClaimNext(context.Background(), []string{""}, "worker", time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ClaimNext once both are claimed = %+v, %v; want ErrNotFound", tr.Run, err)
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
