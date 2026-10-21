//go:build integration && !nopostgres

package main

import (
	"context"
	"crypto/rand"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/credentials"
)

// TestCredentialsPostgres: the postgres backend keeps credentials in the
// database $AGENTKIT_STORE names, migrating it as the store would; a second
// replica (another openVault) sees them; with AGENTKIT_STORE_MIGRATE=off an
// unmigrated database is refused.
func TestCredentialsPostgres(t *testing.T) {
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

	cfg := credentialsConfig{Backend: "postgres"}
	off := cliWithEnv(map[string]string{envStore: store, envStoreMigrate: "off"})
	if _, _, err := off.openVault(ctx, cfg, testRing(t)); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("openVault on an unmigrated database with migrations off: %v, want the schema refused", err)
	}
	c := cliWithEnv(map[string]string{envStore: store})
	tctx := tenantContext(t)
	if _, err := openTestVault(t, c, cfg).Put(tctx, "", "crm", credentials.APIKey{Key: credentials.NewSecret("k")}, 0); err != nil {
		t.Fatal(err)
	}
	if _, m, err := openTestVault(t, off, cfg).Get(tctx, "", "crm"); err != nil || m.Version != 1 {
		t.Errorf("another replica reads %+v, %v", m, err)
	}
}
