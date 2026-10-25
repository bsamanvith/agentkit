//go:build integration && !nopostgres && !noagent

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// TestMemoryPostgres: memory.store.backend "postgres" keeps an agent's
// memories in the database $AGENTKIT_STORE names, migrating it as the store
// would; another replica's tool reads them; with AGENTKIT_STORE_MIGRATE=off
// an unmigrated database is refused.
func TestMemoryPostgres(t *testing.T) {
	t.Parallel()
	base := testkit.IntegrationEnv(t, "AGENTKIT_PG_URL")
	ctx := context.Background()
	schema := "t_" + strings.ToLower(rand.Text())
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	if _, err := conn.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`) })
	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	store := u.String()

	cfg := memoryStoreConfig{Backend: "postgres", Scope: "user"}
	off := cliWithEnv(map[string]string{envStore: store, envStoreMigrate: "off"})
	if _, _, _, err := openMemoryStore(ctx, "agent.memory.store", cfg, off.env); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("open on an unmigrated database with migrations off: %v, want the schema refused", err)
	}
	on := cliWithEnv(map[string]string{envStore: store})
	a, _, closeA, err := openMemoryStore(ctx, "agent.memory.store", cfg, on.env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeA() })
	b, _, closeB, err := openMemoryStore(ctx, "agent.memory.store", cfg, off.env)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeB() })
	tctx := tenantContext(t)
	if _, err := a.Invoke(tctx, json.RawMessage(`{"command":"create","path":"/memories/prefs.md","file_text":"units: metric\n"}`)); err == nil {
		t.Fatal("a user-scoped create with no user succeeded")
	}
	uctx := tenancy.WithUser(tctx, "u1")
	if _, err := a.Invoke(uctx, json.RawMessage(`{"command":"create","path":"/memories/prefs.md","file_text":"units: metric\n"}`)); err != nil {
		t.Fatal(err)
	}
	out, err := b.Invoke(uctx, json.RawMessage(`{"command":"view","path":"/memories/prefs.md"}`))
	if err != nil || !strings.Contains(string(out), "units: metric") {
		t.Errorf("another replica's view = %s, %v", out, err)
	}
	if out, err := b.Invoke(tenancy.WithUser(tctx, "u2"), json.RawMessage(`{"command":"view","path":"/memories/prefs.md"}`)); err == nil {
		t.Errorf("another user's view = %s, want not found", out)
	}
}
