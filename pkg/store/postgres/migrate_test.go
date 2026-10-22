//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
	// The entries an older binary would have appended. This binary's Append
	// counts them on the conversation's record, a later table, so they are
	// written as rows.
	for i, text := range []string{"one", "two"} {
		msgs, err := json.Marshal(prompt(int64(i+1), text).Messages)
		if err != nil {
			t.Fatal(err)
		}
		exec(t, u, `INSERT INTO entries (tenant, conversation, seq, kind, messages, created_at)
			VALUES ('acme', 'c', $1, 'prompt', $2, $3)`, i+1, string(msgs), i+1)
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
	u := schemaURL(t)
	old := openURL(t, u, postgres.WithMigrations(4))
	old.Close()
	// The runs an older binary would have written: one interrupted, one
	// failed and due. This binary's queries need the new column, so they
	// are written as rows.
	exec(t, u, `INSERT INTO runs (tenant, id, conversation, status, created_at) VALUES ('acme', 'interrupted', 'c1', 'running', 1)`)
	exec(t, u, `INSERT INTO runs (tenant, id, conversation, status, retryable, attempts, error, created_at)
		VALUES ('acme', 'due', 'c2', 'failed', true, 1, 'overloaded', 2)`)

	s := openURL(t, u)
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

// ownerRole owns the tables in TestRunsFromBeforeTheQueueAreQueued: not a
// superuser and without BYPASSRLS, so row-level security, forced on owners
// too, applies to the migrations it runs.
const ownerRole = "agentkit_test_owner"

var ownerOnce sync.Once

// Migration 0010 (T099) puts the runs a binary from before it wrote in the
// default lane at priority 0, starting when they were created, in every
// tenant, although the role that migrates is under row-level security: they
// read back so, are resumable as before, and ClaimNext takes them in due
// order.
func TestRunsFromBeforeTheQueueAreQueued(t *testing.T) {
	t.Parallel()
	u := schemaURL(t)
	// With a password, as appRole: CI's Postgres authenticates TCP
	// connections by password (scram), unlike scripts/integration.sh's trust.
	ownerOnce.Do(func() {
		exec(t, u, `DO $$ BEGIN
			CREATE ROLE `+ownerRole+` LOGIN PASSWORD '`+ownerRole+`' NOSUPERUSER NOBYPASSRLS;
		EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL; END $$`)
		// A role an older run of this test made has no password.
		exec(t, u, `ALTER ROLE `+ownerRole+` PASSWORD '`+ownerRole+`'`)
	})
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, u, `GRANT ALL ON SCHEMA `+parsed.Query().Get("search_path")+` TO `+ownerRole)
	parsed.User = url.UserPassword(ownerRole, ownerRole)
	owner := parsed.String()

	openURL(t, owner, postgres.WithMigrations(9)).Close()
	// The runs an older binary would have written, in two tenants, written
	// as rows by a superuser (whom row-level security does not hold back).
	exec(t, u, `INSERT INTO runs (tenant, id, conversation, status, created_at) VALUES ('acme', 'interrupted', 'c1', 'running', 1)`)
	exec(t, u, `INSERT INTO runs (tenant, id, conversation, status, retryable, attempts, error, created_at)
		VALUES ('globex', 'due', 'c2', 'failed', true, 1, 'overloaded', 2)`)
	exec(t, u, `INSERT INTO runs (tenant, id, conversation, status, created_at, interactive) VALUES ('acme', 'chat', 'c3', 'running', 3, true)`)

	s := openURL(t, owner, postgres.WithAdminURL(u)) // migrates, as the owner
	var unset int
	queryRow(t, u, `SELECT count(*) FROM runs WHERE start_at IS DISTINCT FROM created_at OR lane <> '' OR priority <> 0 OR pool <> ''`, nil, &unset)
	if unset != 0 {
		t.Errorf("%d old runs are not in the default lane at priority 0 starting at their creation, in no pool (0013)", unset)
	}
	for tenant, id := range map[string]string{"acme": "interrupted", "globex": "due"} {
		r, err := s.Get(tenantCtx(t, tenant), id)
		if err != nil || !r.StartAt.Equal(r.CreatedAt) {
			t.Errorf("old run %s/%s after the upgrade = %+v, %v; want it starting at its creation", tenant, id, r, err)
		}
	}
	if runs, err := s.ListResumable(tenantCtx(t, "acme"), time.Unix(0, 3), 0); err != nil || len(runs) != 1 || runs[0].ID != "interrupted" {
		t.Errorf("ListResumable after the upgrade = %+v, %v; want the old background run", runs, err)
	}
	admin := postgres.Admin(s)
	for _, want := range []string{"acme/interrupted", "globex/due"} {
		tr, err := admin.ClaimNext(context.Background(), []string{""}, "worker", time.Minute)
		if got := tr.Tenant.ID() + "/" + tr.Run.ID; err != nil || got != want {
			t.Fatalf("ClaimNext after the upgrade = %s, %v; want %s", got, err, want)
		}
	}
	if tr, err := admin.ClaimNext(context.Background(), []string{""}, "worker", time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ClaimNext once both are claimed = %+v, %v; want ErrNotFound", tr.Run, err)
	}
	var forced bool
	queryRow(t, u, `SELECT relforcerowsecurity FROM pg_class WHERE oid = 'runs'::regclass`, nil, &forced)
	if !forced {
		t.Error("row-level security is no longer forced on runs after the migration")
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
