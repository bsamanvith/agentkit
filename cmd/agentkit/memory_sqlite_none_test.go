//go:build nosqlite

package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// TestMemorySQLiteNotBuilt: an image without SQLite (every one but local)
// refuses memory.store.backend "sqlite" at startup and names the tag. Run
// with -tags nosqlite.
func TestMemorySQLiteNotBuilt(t *testing.T) {
	t.Parallel()
	db := filepath.Join(t.TempDir(), "memories.db")
	config := `{"agent": {"model": {"id": "m"},
	  "memory": {"store": {"backend": "sqlite", "path": ` + strconv.Quote(db) + `}}}}`
	_, err := startAgents(t, config, map[string]*llmtest.Fake{"m": llmtest.NewFake(t)})
	if err == nil || !strings.Contains(err.Error(), `agent.memory.store: backend "sqlite": this agentkit is built without SQLite (-tags nosqlite)`) {
		t.Fatalf("startAgents: %v, want the not-built error", err)
	}
}

// TestMemoryIndexSQLiteNotBuilt: the same for memory.index.backend "sqlite".
func TestMemoryIndexSQLiteNotBuilt(t *testing.T) {
	t.Parallel()
	db := filepath.Join(t.TempDir(), "memories.db")
	config := `{"agent": {"model": {"id": "m"},
	  "memory": {"index": {"backend": "sqlite", "path": ` + strconv.Quote(db) + `}}}}`
	_, err := startAgents(t, config, map[string]*llmtest.Fake{"m": llmtest.NewFake(t)})
	if err == nil || !strings.Contains(err.Error(), `agent.memory.index: backend "sqlite": this agentkit is built without SQLite (-tags nosqlite)`) {
		t.Fatalf("startAgents: %v, want the not-built error", err)
	}
}
