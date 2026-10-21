//go:build nosqlite

package main

import (
	"path/filepath"
	"testing"
)

// TestCredentialsSQLiteNotBuilt: an image without SQLite (every one but
// local) refuses credentials.backend "sqlite" at startup and names the tag.
// Run with -tags nosqlite.
func TestCredentialsSQLiteNotBuilt(t *testing.T) {
	t.Parallel()
	cfg := credentialsConfig{Backend: "sqlite", Path: filepath.Join(t.TempDir(), "agentkit.db")}
	_, _, err := cli{}.openVault(t.Context(), cfg, testRing(t))
	const want = `credentials: backend "sqlite": this agentkit is built without SQLite (-tags nosqlite); use an image that has it`
	if err == nil || err.Error() != want {
		t.Fatalf("openVault: %v, want %q", err, want)
	}
}
