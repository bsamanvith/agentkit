package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// lastResults are the tool results of the last request a fake was sent.
func lastResults(m *llmtest.Fake) map[string]llm.ToolResult {
	reqs := m.Requests()
	out := map[string]llm.ToolResult{}
	for _, msg := range reqs[len(reqs)-1].Messages {
		for _, b := range msg.Content {
			if r, ok := b.(llm.ToolResult); ok {
				out[r.ToolUseID] = r
			}
		}
	}
	return out
}

func calls(names ...string) llmtest.Reply {
	var uses []llm.ToolUse
	for i, n := range names {
		uses = append(uses, llm.ToolUse{ID: fmt.Sprint("c", i), Name: n, Input: json.RawMessage(`{"path":"."}`)})
	}
	return llmtest.ToolCalls(uses...)
}

// TestServeToolPolicies: agents[].tool_policy decides an agent's calls,
// with rules on its toolsets (by the tools' unprefixed names); the
// environment's policy narrows every agent; a run's narrows further.
func TestServeToolPolicies(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	config := `{
	  "mcp": {"toolsets": [{"name": "fs", "type": "files", "settings": {"root": ` + jsonString(root) + `}}, {"name": "demo", "type": "demo"}]},
	  "environment": {"tool_policy": {"rules": [{"match": {"kinds": ["write"]}, "action": "deny", "reason": "this environment is read-only"}], "default": "allow"}},
	  "agents": [
	    {"name": "open", "model": {"id": "m-open"}, "toolsets": ["fs", "demo"]},
	    {"name": "nofs", "model": {"id": "m-nofs"}, "toolsets": ["fs", "demo"],
	     "tool_policy": {"rules": [{"match": {"toolsets": ["fs"]}, "action": "deny", "reason": "no files"}], "default": "allow"}}
	  ]
	}`
	open := llmtest.NewFake(t, calls("edit_file", "list_files"), llmtest.Text("one"), calls("list_files"), llmtest.Text("two"))
	nofs := llmtest.NewFake(t, calls("list_files", "current_time"), llmtest.Text("three"))
	c := serveAgents(t, config, map[string]*llmtest.Fake{"m-open": open, "m-nofs": nofs})
	ctx := t.Context()

	if _, err := c.Run(ctx, agentclient.RunRequest{Message: "go", Agent: "open"}); err != nil {
		t.Fatal(err)
	}
	res := lastResults(open)
	if r := res["c0"]; !r.IsError || r.Content != "not allowed in this run: this environment is read-only" {
		t.Errorf("edit_file under the environment's policy: %+v", r)
	}
	if r := res["c1"]; r.IsError {
		t.Errorf("list_files (a read) was refused: %+v", r)
	}

	if _, err := c.Run(ctx, agentclient.RunRequest{Message: "go", Agent: "open", ToolPolicy: json.RawMessage(`{"rules":[{"match":{"tools":["list_*"]},"action":"deny"}],"default":"allow"}`)}); err != nil {
		t.Fatal(err)
	}
	if r := lastResults(open)["c0"]; !r.IsError || !strings.Contains(r.Content, "(policy run)") {
		t.Errorf("list_files under the run's policy: %+v", r)
	}

	if _, err := c.Run(ctx, agentclient.RunRequest{Message: "go", Agent: "nofs"}); err != nil {
		t.Fatal(err)
	}
	res = lastResults(nofs)
	if r := res["c0"]; !r.IsError || r.Content != "not allowed in this run: no files" {
		t.Errorf("list_files under the agent's toolset rule: %+v", r)
	}
	if r := res["c1"]; strings.HasPrefix(r.Content, "not allowed") {
		t.Errorf("current_time (another toolset) was refused: %+v", r)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestLoadToolPolicies: a preset's name or a policy loads; anything else
// fails startup, naming the section.
func TestLoadToolPolicies(t *testing.T) {
	t.Parallel()
	load := func(config string) (serveConfig, error) {
		path := filepath.Join(t.TempDir(), "agentkit.json")
		if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		return loadServeConfig(path)
	}
	cfg, err := load(`{"environment": {"tool_policy": "read_only"}, "agents": [{"name": "a", "tool_policy": "low_risk_writes"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment.ToolPolicy == nil || cfg.Environment.ToolPolicy.Name != "read_only" || cfg.Agents[0].ToolPolicy.Name != "low_risk_writes" {
		t.Fatalf("loaded %+v %+v", cfg.Environment, cfg.Agents[0])
	}
	if p := cfg.Agents[0].toolPolicy(cfg.Environment.ToolPolicy); p.Name != "low_risk_writes" || len(p.NarrowedBy) != 1 {
		t.Errorf("effective policy %+v, want the agent's narrowed by the environment's", p)
	}
	if p := (agentDef{}).toolPolicy(nil); p.Name != "all" || len(p.NarrowedBy) != 0 {
		t.Errorf("no policy at all: %+v, want all", p)
	}
	for _, bad := range []string{
		`{"environment": {"tool_policy": "lenient"}}`,
		`{"environment": {"policy": "read_only"}}`,
		`{"agents": [{"name": "a", "tool_policy": {"default": "maybe"}}]}`,
		`{"agent": {"tool_policy": {"rules": [{"action": "deny", "match": {"kind": ["write"]}}], "default": "allow"}}}`,
	} {
		if _, err := load(bad); err == nil {
			t.Errorf("%s loaded", bad)
		}
	}
}
