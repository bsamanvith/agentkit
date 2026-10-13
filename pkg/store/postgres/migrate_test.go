//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/postgres"
)

func prompt(seq int64, text string) store.Entry {
	return store.Entry{Seq: seq, Kind: store.KindPrompt, Messages: []llm.Message{llm.User(llm.Text{Text: text})}}
}

// versions is how schema_migrations reads after migrations 1 to n: "1,2,…,n".
func versions(n int) string {
	vs := make([]string, n)
	for i := range vs {
		vs[i] = strconv.Itoa(i + 1)
	}
	return strings.Join(vs, ",")
}

// migrationRows returns the versions schema_migrations records, in order.
func migrationRows(t testing.TB, url string) string {
	t.Helper()
	var got string
	queryRow(t, url, `SELECT COALESCE(string_agg(version::text, ',' ORDER BY version), '') FROM schema_migrations`, nil, &got)
	return got
}

// Processes starting at once on an empty database migrate it once between
// them: the advisory lock makes the others wait, and they find the work done.
// Had two applied a migration, the second would have failed (CREATE TABLE of
// a table that exists) or recorded its version twice (the primary key).
func TestConcurrentOpensMigrateOnce(t *testing.T) {
	t.Parallel()
	u := schemaURL(t)
	const n = 4
	var wg sync.WaitGroup
	stores := make([]*postgres.Store, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			stores[i], errs[i] = postgres.Open(context.Background(), u, postgres.WithClock(clock.Real()))
		})
	}
	close(start)
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Errorf("Open %d: %v", i, errs[i])
			continue
		}
		t.Cleanup(stores[i].Close)
	}
	if t.Failed() {
		return
	}
	if got, want := migrationRows(t, u), versions(postgres.Latest()); got != want {
		t.Errorf("schema_migrations holds versions %q, want each of %s once", got, want)
	}
	ctx := tenantCtx(t, "acme")
	if _, err := stores[0].Create(ctx, "r", "c"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r, err := stores[n-1].Get(ctx, "r"); err != nil || r.ID != "r" {
		t.Errorf("Get through another store = %+v, %v", r, err)
	}
}

// A database written by an older binary (here, one that knew only the first
// migration, the conversation log) is upgraded in place when a newer binary
// opens it: what it held is still there, and the later tables work.
func TestOpenUpgradesAnOlderSchema(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	u := schemaURL(t)

	old := openURL(t, u, postgres.WithMigrations(1))
	for i, text := range []string{"one", "two"} {
		if err := old.Append(ctx, "c", prompt(int64(i+1), text)); err != nil {
			t.Fatalf("Append at version 1: %v", err)
		}
	}
	if _, err := old.Create(ctx, "r", "c"); err == nil {
		t.Fatal("Create at version 1 succeeded; the old schema has no runs table, so the test proves nothing")
	}
	old.Close()

	s := openURL(t, u)
	if got, want := migrationRows(t, u), versions(postgres.Latest()); got != want {
		t.Errorf("schema_migrations holds versions %q after the upgrade, want %s", got, want)
	}
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
	if err := s.AppendEvents(ctx, "r", []store.Event{{Seq: 1, Type: "started"}}); err != nil {
		t.Errorf("AppendEvents after the upgrade: %v", err)
	}
}

// A binary refuses a database that a newer binary has migrated past what it
// knows, whether it would migrate or only check, and leaves it as it was.
func TestOpenRefusesANewerSchema(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	u := schemaURL(t)
	s := openURL(t, u)
	if err := s.Append(ctx, "c", prompt(1, "kept")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// A migration from the future, as a newer binary would have recorded it.
	future := postgres.Latest() + 1
	exec(t, u, `INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, 'future', 0)`, future)

	for _, opts := range [][]postgres.Option{
		nil,
		{postgres.WithMigrations(2)},
		{postgres.WithMigrate(false)},
	} {
		got, err := postgres.Open(context.Background(), u, opts...)
		if !errors.Is(err, postgres.ErrSchemaTooNew) {
			if got != nil {
				got.Close()
			}
			t.Fatalf("Open(%d options) of a schema at version %d = %v, want ErrSchemaTooNew", len(opts), future, err)
		}
		if !strings.Contains(err.Error(), "knows up to") {
			t.Errorf("error %q does not say which versions disagree", err)
		}
	}
	if err := postgres.Migrate(context.Background(), u); !errors.Is(err, postgres.ErrSchemaTooNew) {
		t.Errorf("Migrate of a schema at version %d = %v, want ErrSchemaTooNew", future, err)
	}

	if got, want := migrationRows(t, u), versions(future); got != want {
		t.Errorf("schema_migrations holds versions %q after the refusals, want %s", got, want)
	}
	page, err := s.History(ctx, "c", store.Page{})
	if err != nil || len(page.Entries) != 1 {
		t.Errorf("History after the refused opens = %+v, %v; want the entry intact", page.Entries, err)
	}
}

// With WithMigrate(false) (what AGENTKIT_STORE_MIGRATE=off selects), a server
// changes no schema: it refuses one that is behind, empty or partly migrated,
// and opens once the one-off job (Migrate) has run.
func TestWithMigrateOffRefusesAnOldSchema(t *testing.T) {
	t.Parallel()
	u := schemaURL(t)

	if s, err := postgres.Open(context.Background(), u, postgres.WithMigrate(false)); !errors.Is(err, postgres.ErrSchemaTooOld) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open(WithMigrate(false)) of an empty schema = %v, want ErrSchemaTooOld", err)
	}
	var tables int
	queryRow(t, u, `SELECT count(*) FROM pg_tables WHERE schemaname = current_schema()`, nil, &tables)
	if tables != 0 {
		t.Errorf("Open(WithMigrate(false)) created %d tables, want none", tables)
	}

	openURL(t, u, postgres.WithMigrations(2)).Close()
	if s, err := postgres.Open(context.Background(), u, postgres.WithMigrate(false)); !errors.Is(err, postgres.ErrSchemaTooOld) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open(WithMigrate(false)) of a schema at version 2 = %v, want ErrSchemaTooOld", err)
	}
	if got := migrationRows(t, u); got != "1,2" {
		t.Errorf("schema_migrations holds versions %q, want 1,2: a check must not migrate", got)
	}

	if err := postgres.Migrate(context.Background(), u); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	s := openURL(t, u, postgres.WithMigrate(false))
	ctx := tenantCtx(t, "acme")
	if _, err := s.Create(ctx, "r", "c"); err != nil {
		t.Errorf("Create after Migrate: %v", err)
	}
}
