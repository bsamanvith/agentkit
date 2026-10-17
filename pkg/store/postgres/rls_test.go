//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/postgres"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// The suites run as appRole: not a superuser, not the tables' owner, no
// BYPASSRLS, so row-level security applies to every query they make.
const appRole = "agentkit_test_app"

var roleOnce sync.Once

// appURL migrates the schema of owner (a URL from schemaURL) as the owner and
// returns the same URL as appRole, with the rights an application role gets:
// rows, not schema.
func appURL(t testing.TB, owner string) string {
	t.Helper()
	roleOnce.Do(func() {
		exec(t, owner, `DO $$ BEGIN
			CREATE ROLE `+appRole+` LOGIN PASSWORD '`+appRole+`' NOSUPERUSER NOBYPASSRLS;
		EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL; END $$`)
	})
	if err := postgres.Migrate(context.Background(), owner); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	u, _ := url.Parse(owner)
	schema := u.Query().Get("search_path")
	exec(t, owner, `GRANT USAGE ON SCHEMA `+schema+` TO `+appRole)
	exec(t, owner, `GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA `+schema+` TO `+appRole)
	u.User = url.UserPassword(appRole, appRole)
	return u.String()
}

// rlsRig is a store over a fresh schema as appRole, with a row in every
// table for tenants a and b.
func rlsRig(t *testing.T, opts ...postgres.Option) (owner, app string, s *postgres.Store) {
	t.Helper()
	owner = schemaURL(t)
	app = appURL(t, owner)
	s = openURL(t, app, append([]postgres.Option{postgres.WithMigrate(false)}, opts...)...)
	for _, tn := range []string{"a", "b"} {
		ctx, run, conv := tenantCtx(t, tn), "run-"+tn, "conv-"+tn
		_, err := s.CreateConversation(ctx, store.Conversation{ID: conv, Agent: "x"})
		if err == nil {
			err = s.Append(ctx, conv, store.Entry{Seq: 1, Kind: store.KindTurn, Messages: []llm.Message{llm.User(llm.Text{Text: "hi"})}})
		}
		if err == nil {
			_, err = s.Create(ctx, run, conv)
		}
		if err == nil {
			err = s.AppendEvents(ctx, run, []store.Event{{Seq: 1, Type: "t", Data: json.RawMessage(`{}`)}})
		}
		if err == nil {
			_, err = s.SendCommand(ctx, run, store.Command{Key: "k", Type: "t"})
		}
		if err == nil {
			_, err = s.PutToolset(ctx, store.ToolsetRecord{Name: "ts", Data: json.RawMessage(`{}`)}, 0)
		}
		if err == nil {
			err = s.Save(ctx, run, flow.Checkpoint{
				Version: flow.CheckpointVersion, RunID: run, Status: flow.StatusRunning,
				Initial: json.RawMessage(`{}`), Steps: map[string]json.RawMessage{},
			})
		}
		if err != nil {
			t.Fatalf("seeding tenant %s: %v", tn, err)
		}
	}
	return owner, app, s
}

// A query with no tenant filter, on the store's own connections, sees only
// the context's tenant's rows, and cannot write another tenant's.
func TestRowLevelSecurity(t *testing.T) {
	t.Parallel()
	owner, _, s := rlsRig(t)
	pool := postgres.Pool(s)
	ctx := tenantCtx(t, "a")
	for _, table := range []string{"runs", "entries", "events", "checkpoints", "conversations", "commands", "toolsets"} {
		var others int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tenant <> 'a'`).Scan(&others); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if others != 0 {
			t.Errorf("%s: tenant a sees %d rows of other tenants", table, others)
		}
		var all int
		queryRow(t, owner, `SELECT count(*) FROM `+table, nil, &all)
		if all < 2 {
			t.Fatalf("%s: %d rows in all; the test needs one per tenant", table, all)
		}
	}
	var ids []string
	rows, err := pool.Query(ctx, `SELECT id FROM runs`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	if rows.Err() != nil || len(ids) != 1 || ids[0] != "run-a" {
		t.Errorf("unfiltered SELECT as a = %v, %v; want [run-a]", ids, rows.Err())
	}
	if _, err := pool.Exec(ctx, `INSERT INTO runs (tenant, id, conversation, status, created_at) VALUES ('b', 'planted', 'c', 'running', 1)`); err == nil {
		t.Error("tenant a wrote a row of tenant b")
	}
	if tag, err := pool.Exec(ctx, `UPDATE runs SET status = 'failed'`); err != nil || tag.RowsAffected() != 1 {
		t.Errorf("unfiltered UPDATE as a touched %d rows, %v; want 1", tag.RowsAffected(), err)
	}
	if tag, err := pool.Exec(ctx, `DELETE FROM runs WHERE id = 'run-b'`); err != nil || tag.RowsAffected() != 0 {
		t.Errorf("tenant a deleted %d of b's runs, %v", tag.RowsAffected(), err)
	}
	if _, err := s.Get(tenantCtx(t, "b"), "run-b"); err != nil {
		t.Errorf("b's run after a's attempts: %v", err)
	}
}

// One pooled connection serves tenant a, then a context with no tenant: that
// fails before its query, and a later tenant sees only its own rows.
func TestRowLevelSecurityPooled(t *testing.T) {
	t.Parallel()
	owner := schemaURL(t)
	app := appURL(t, owner)
	u, _ := url.Parse(app)
	q := u.Query()
	q.Set("pool_max_conns", "1")
	u.RawQuery = q.Encode()
	s := openURL(t, u.String(), postgres.WithMigrate(false))
	if _, err := s.Create(tenantCtx(t, "a"), "run-a", "conv-a"); err != nil {
		t.Fatal(err)
	}
	pool := postgres.Pool(s)
	var n int
	err := pool.QueryRow(context.Background(), `SELECT count(*) FROM runs`).Scan(&n)
	if !errors.Is(err, tenancy.ErrNoTenant) {
		t.Errorf("no tenant on a connection tenant a used: %v (saw %d rows), want ErrNoTenant", err, n)
	}
	if err := pool.QueryRow(tenantCtx(t, "b"), `SELECT count(*) FROM runs`).Scan(&n); err != nil || n != 0 {
		t.Errorf("tenant b on a's connection sees %d runs, %v", n, err)
	}
	if _, err := s.Get(context.Background(), "run-a"); !errors.Is(err, tenancy.ErrNoTenant) {
		t.Errorf("store call with no tenant: %v", err)
	}
}

// Admin reads across tenants only on a role that bypasses row-level
// security; on the application role it fails rather than finding nothing.
func TestAdminRole(t *testing.T) {
	t.Parallel()
	owner, app, s := rlsRig(t)
	now := time.Now().Add(time.Hour)
	if _, err := postgres.Admin(s).ListResumableAll(context.Background(), now, 10); !errors.Is(err, postgres.ErrAdminRole) {
		t.Errorf("Admin on the application role: %v, want ErrAdminRole", err)
	}
	withAdmin := openURL(t, app, postgres.WithMigrate(false), postgres.WithAdminURL(owner))
	got, err := postgres.Admin(withAdmin).ListResumableAll(context.Background(), now, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("Admin on its own role: %d runs, %v; want both tenants'", len(got), err)
	}
	if _, err := postgres.Open(context.Background(), app, postgres.WithAdminURL("::bad")); err == nil {
		t.Error("a bad admin URL was accepted")
	}
}
