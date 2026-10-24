//go:build !nopostgres && !noagent

package main

import (
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// TestMemoryPostgresBackendConfig: backend "postgres" takes the store's
// database, so it needs a Postgres $AGENTKIT_STORE and refuses a path.
func TestMemoryPostgresBackendConfig(t *testing.T) {
	t.Parallel()
	for memory, want := range map[string]string{
		`{"store": {"backend": "postgres"}}`:                 `needs $AGENTKIT_STORE to name a Postgres store`,
		`{"store": {"backend": "postgres", "path": "x.db"}}`: `uses the store's database ($AGENTKIT_STORE): leave out path`,
	} {
		config := `{"agent": {"model": {"id": "m"}, "memory": ` + memory + `}}`
		_, err := startAgents(t, config, map[string]*llmtest.Fake{"m": llmtest.NewFake(t)})
		if err == nil || !strings.Contains(err.Error(), "agent.memory.store: backend \"postgres\"") || !strings.Contains(err.Error(), want) {
			t.Errorf("memory %s: %v, want an error with %q", memory, err, want)
		}
	}
}
