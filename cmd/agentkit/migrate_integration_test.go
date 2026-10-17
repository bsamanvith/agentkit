//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	// serve as a role under row-level security, with no admin URL for the
	// recovery loop, fails startup and names AGENTKIT_STORE_ADMIN.
	const role = "agentkit_cmd_test_app"
	for _, sql := range []string{
		`DO $$ BEGIN CREATE ROLE ` + role + ` LOGIN PASSWORD '` + role + `' NOSUPERUSER NOBYPASSRLS;
			EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL; END $$`,
		`GRANT USAGE ON SCHEMA ` + schema + ` TO ` + role,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ` + schema + ` TO ` + role,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	u.User = url.UserPassword(role, role)
	config := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(config, []byte(`{"mcp":{"toolsets":[{"name":"demo","type":"demo"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		envComponents: "agent", envAddr: "127.0.0.1:0", envConfig: config,
		envStore: u.String(), envStoreMigrate: "off",
	}
	var stderr strings.Builder
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second) // a broken check serves until then
	defer cancel()
	code := cli{getenv: func(k string) string { return env[k] }, stdout: io.Discard, stderr: &stderr}.main(sctx, []string{"serve"})
	if code != 1 || !strings.Contains(stderr.String(), envStoreAdmin) {
		t.Errorf("serve under RLS with no admin URL: exit %d, stderr %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), role+":"+role) {
		t.Errorf("the error shows the store URL's password: %q", stderr.String())
	}
}
