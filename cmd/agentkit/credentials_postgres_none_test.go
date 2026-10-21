//go:build nopostgres

package main

import "testing"

// TestCredentialsPostgresNotBuilt: an image without Postgres (local) refuses
// credentials.backend "postgres" at startup and names the tag. Run with
// -tags nopostgres.
func TestCredentialsPostgresNotBuilt(t *testing.T) {
	t.Parallel()
	c := cliWithEnv(map[string]string{envStore: "postgres://localhost:1/x"})
	_, _, err := c.openVault(t.Context(), credentialsConfig{Backend: "postgres"}, testRing(t))
	const want = `credentials: backend "postgres": this agentkit is built without Postgres (-tags nopostgres); use an image that has it`
	if err == nil || err.Error() != want {
		t.Fatalf("openVault: %v, want %q", err, want)
	}
}
