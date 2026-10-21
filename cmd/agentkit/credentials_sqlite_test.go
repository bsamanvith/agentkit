//go:build !nosqlite

package main

import (
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/sqlite"
)

// TestCredentialsSQLiteInTheStore: with no path, the sqlite backend keeps the
// credentials in the SQLite store's file that $AGENTKIT_STORE names.
func TestCredentialsSQLiteInTheStore(t *testing.T) {
	t.Parallel()
	db := filepath.Join(t.TempDir(), "agentkit.db")
	c := cliWithEnv(map[string]string{envStore: "sqlite:" + db})
	ctx := tenantContext(t)
	v := openTestVault(t, c, credentialsConfig{Backend: "sqlite"})
	if _, err := v.Put(ctx, "", "crm", credentials.APIKey{Key: credentials.NewSecret("k")}, 0); err != nil {
		t.Fatal(err)
	}
	s, err := sqlite.Open(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if b, err := s.Get(ctx, "", "crm"); err != nil || b.Version != 1 {
		t.Errorf("the store's file holds %+v, %v", b.Meta, err)
	}
}
