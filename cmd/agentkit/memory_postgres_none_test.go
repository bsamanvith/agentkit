//go:build nopostgres && !noagent

package main

import (
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// TestMemoryPostgresNotBuilt: an image without Postgres (local) refuses
// memory.store.backend "postgres" at startup and names the tag. Run with
// -tags nopostgres.
func TestMemoryPostgresNotBuilt(t *testing.T) {
	t.Parallel()
	config := `{"agent": {"model": {"id": "m"}, "memory": {"store": {"backend": "postgres"}}}}`
	_, err := startAgents(t, config, map[string]*llmtest.Fake{"m": llmtest.NewFake(t)})
	if err == nil || !strings.Contains(err.Error(), `agent.memory.store: backend "postgres": this agentkit is built without Postgres (-tags nopostgres)`) {
		t.Fatalf("startAgents: %v, want the not-built error", err)
	}
}
