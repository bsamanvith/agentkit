//go:build !noagent

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
)

// routedConfig has three agents and the pools of T105: chats to "chat",
// but search's to "special"; batch's runs, chat or background, to "batch";
// every other background run to "background", the default.
const routedConfig = `{
  "agents": [
    {"name": "plain", "model": {"id": "model-plain"}},
    {"name": "search", "model": {"id": "model-search"}},
    {"name": "batch", "model": {"id": "model-batch"}, "isolation": "process"}
  ],
  "runners": {
    "pools": [
      {"name": "chat", "isolation": "process", "limit": 2},
      {"name": "special"},
      {"name": "batch", "limit": 1},
      {"name": "background", "limit": 1}
    ],
    "routes": [
      {"pool": "special", "runs": "chat", "agents": ["search"]},
      {"pool": "batch", "agents": ["batch"]},
      {"pool": "chat", "runs": "chat"},
      {"pool": "background", "runs": "background", "isolation": "process"}
    ],
    "default": "background"
  }
}`

// T105: agentkit serve routes runs as its runners section says: each chat
// runs in the pool its route names, which the run stores and the API
// returns; background runs go to theirs; the pools have their limits, and
// those that take background runs get a dispatcher.
func TestServeRoutesRunsByConfig(t *testing.T) {
	t.Parallel()
	models := map[string]*llmtest.Fake{
		"model-plain":  llmtest.NewFake(t, llmtest.Text("plain")),
		"model-search": llmtest.NewFake(t, llmtest.Text("search")),
		"model-batch":  llmtest.NewFake(t, llmtest.Text("batch")),
	}
	c := serveAgents(t, routedConfig, models)
	for agent, want := range map[string]string{"plain": "chat", "search": "special", "batch": "batch"} {
		run, err := c.Run(t.Context(), agentclient.RunRequest{Message: "hi", Agent: agent})
		if err != nil {
			t.Fatalf("a chat of %s: %v", agent, err)
		}
		if run.Pool != want {
			t.Errorf("a chat of %s ran in pool %q, want %q", agent, run.Pool, want)
		}
		if got, err := c.GetRun(t.Context(), run.ID); err != nil || got.Pool != want {
			t.Errorf("GET /v1/runs/%s = pool %q, %v; want %q", run.ID, got.Pool, err, want)
		}
	}

	cfg := loadConfig(t, routedConfig)
	isolations := map[string]pool.Isolation{"plain": pool.Process, "search": pool.Process, "batch": pool.Process}
	rt, err := cfg.Runners.router(cfg.Agents, isolations)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	for agent, want := range map[string]string{"plain": "background", "search": "background", "batch": "batch"} {
		p, err := rt.Route(pool.Request{Placement: runner.Placement{ConversationID: "c"}, Agent: agent})
		if err != nil || p.Name() != want {
			t.Errorf("a background run of %s goes to %v (%v), want %s", agent, p, err, want)
		}
	}
	for name, limit := range map[string]int{"chat": 2, "special": 0, "batch": 1, "background": 1} {
		got := 0 // no limit of its own
		if s := rt.Slots(name); s != nil {
			got = s.Limit()
		}
		if got != limit {
			t.Errorf("pool %s has limit %d, want %d", name, got, limit)
		}
	}
	if got, want := cfg.Runners.dispatched(), []string{"background", "batch"}; !slices.Equal(got, want) {
		t.Errorf("dispatched pools %v, want %v (special and chat take only chats)", got, want)
	}

	// No runners section: the one default pool, dispatched as before.
	none := loadConfig(t, `{"agents": [{"name": "plain"}]}`)
	if rt, err := none.Runners.router(none.Agents, map[string]pool.Isolation{"plain": pool.Process}); err != nil || rt != nil {
		t.Errorf("router of no runners = %v, %v; want none (the server's default)", rt, err)
	}
	if got := none.Runners.dispatched(); !slices.Equal(got, []string{pool.DefaultName}) {
		t.Errorf("dispatched pools of no runners = %v, want [default]", got)
	}
}

func loadConfig(t *testing.T, config string) serveConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
