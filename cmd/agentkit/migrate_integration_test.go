//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/store/postgres"
)

// agentkit migrate brings an empty database to the binary's schema, so that a
// server started with AGENTKIT_STORE_MIGRATE=off (postgres.WithMigrate(false))
// opens it; running it again changes nothing.
func TestMigrateCommand(t *testing.T) {
	t.Parallel()
	base := testkit.IntegrationEnv(t, "AGENTKIT_PG_URL")
	ctx := context.Background()
	schema := "t_" + strings.ToLower(rand.Text())
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	// Cleanups run last in, first out: the schema is dropped, then conn closed.
	t.Cleanup(func() { _ = conn.Close(ctx) })
	if _, err := conn.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("DROP SCHEMA %s: %v", schema, err)
		}
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	store := u.String()

	if s, err := postgres.Open(ctx, store, postgres.WithMigrate(false)); err == nil {
		s.Close()
		t.Fatal("an empty schema opened with WithMigrate(false); the test proves nothing")
	}
	for range 2 {
		if code, stderr := migrateWith(t, store); code != 0 {
			t.Fatalf("agentkit migrate = exit %d, stderr %q", code, stderr)
		}
	}
	s, err := postgres.Open(ctx, store, postgres.WithMigrate(false))
	if err != nil {
		t.Fatalf("Open(WithMigrate(false)) after agentkit migrate: %v", err)
	}
	s.Close()
}
