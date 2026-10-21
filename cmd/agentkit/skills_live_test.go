//go:build live

package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/skill"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// TestLiveSkillLoad: real Claude, with the demo skill of search.json in its
// catalog and a task its description matches, calls skill_load for it.
func TestLiveSkillLoad(t *testing.T) {
	t.Parallel()
	requireCredentials(t)
	cat, _, err := loadSkills("agents.search", []skillSource{{Path: filepath.Join("..", "..", "deploy", "local", "agent", "skills")}})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cat.Toolset()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	model, err := newClaude(modelFlags{model: defaultModelID, effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := harness.New(model, reg, harness.WithMaxTurns(4),
		harness.WithSystem("You are a helpful research assistant. Keep answers short.\n\n"+cat.SystemText()))
	if err != nil {
		t.Fatal(err)
	}
	loaded := false
	for ev, err := range h.Run(t.Context(), harness.Input{Message: harness.UserText("What is the latest stable release of Go? Give me a short cited brief.")}) {
		if err != nil {
			t.Logf("run ended: %v", err) // the agent has no search tool; loading the skill is what counts
			break
		}
		if ev.Kind == harness.EventToolStart {
			t.Logf("tool call: %s %s", ev.ToolUse.Name, ev.ToolUse.Input)
			var in struct{ Name string }
			if ev.ToolUse.Name == skill.LoadTool && json.Unmarshal(ev.ToolUse.Input, &in) == nil && in.Name == "research-brief" {
				loaded = true
			}
		}
	}
	if !loaded {
		t.Error("Claude did not call skill_load for research-brief")
	}
}
