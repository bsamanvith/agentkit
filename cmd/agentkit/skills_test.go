//go:build !nomodels && !noagent

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/skill"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
)

// writeSkill writes a skill folder under dir.
func writeSkill(t *testing.T, dir, name, description string, files map[string]string) {
	t.Helper()
	files = maps.Clone(files)
	if files == nil {
		files = map[string]string{}
	}
	files["SKILL.md"] = fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n# %s\n\nInstructions of %s.\n", name, description, name, name)
	for p, s := range files {
		full := filepath.Join(dir, name, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// newSkillAgents builds the agent component from a config file in dir as
// serve does, with the fake model for every agent.
func newSkillAgents(t *testing.T, dir, config string, model llm.Model) (component.Mount, error) {
	t.Helper()
	path := filepath.Join(dir, "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	c := cli{getenv: func(string) string { return "" }, newModel: func(modelFlags) (llm.Model, error) { return model, nil }}
	env := serveEnv{components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second}
	log := slog.New(slog.DiscardHandler)
	built, problems, err := c.buildToolsets(t.Context(), env, cfg, path, log)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := c.newAgents(t.Context(), cfg, built, problems, env, nil, scoped.Memory(nil), nil, clock.Real(), log)
	if err == nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = m.Component.Shutdown(ctx)
		})
	}
	return m, err
}

// TestServeSkills: an agent's skill folders (relative to the config file)
// put the catalog in its system prompt and skill_load and skill_read in its
// tools; the model loads a skill and reads its file; an optional folder
// that fails is skipped and shown, and /readyz lists the skills loaded.
func TestServeSkills(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSkill(t, filepath.Join(dir, "skills"), "release-notes", "Write release notes.", map[string]string{"references/template.md": "## v<version>\n"})
	writeSkill(t, filepath.Join(dir, "more"), "sql-review", "Review a SQL migration.", nil)
	writeSkill(t, filepath.Join(dir, "broken"), "Bad Name", "d", nil)
	const config = `{
	  "mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
	  "agents": [
	    {"name": "writer", "model": {"id": "m"}, "system": "You write.", "toolsets": ["demo"],
	     "skills": [{"path": "skills"}, "more", {"path": "broken", "optional": true}, {"path": "missing", "optional": true}]},
	    {"name": "plain", "model": {"id": "m"}, "system": "You are plain.", "toolsets": ["demo"]}
	  ]
	}`
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "1", Name: skill.LoadTool, Input: json.RawMessage(`{"name":"release-notes"}`)}),
		llmtest.ToolCalls(llm.ToolUse{ID: "2", Name: skill.ReadTool, Input: json.RawMessage(`{"name":"release-notes","path":"references/template.md"}`)}),
		llmtest.Text("done"),
		llmtest.Text("plain"),
	)
	m, err := newSkillAgents(t, dir, config, model)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(m.Component.Handler())
	t.Cleanup(ts.Close)
	client, err := agentclient.New(ts.URL, agentclient.WithHTTPClient(ts.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if run, err := client.Run(t.Context(), agentclient.RunRequest{Message: "Release notes, please.", Agent: "writer"}); err != nil || run.Answer != "done" {
		t.Fatalf("run = %+v, %v", run, err)
	}
	if run, err := client.Run(t.Context(), agentclient.RunRequest{Message: "hi", Agent: "plain"}); err != nil || run.Answer != "plain" {
		t.Fatalf("plain run = %+v, %v", run, err)
	}
	reqs := model.Requests()
	first := reqs[0]
	if !strings.HasPrefix(first.System, "You write.\n\n<skills>\n") || !strings.Contains(first.System, "- release-notes: Write release notes.\n- sql-review: Review a SQL migration.\n</skills>") {
		t.Errorf("system prompt %q, want the configured one then the catalog", first.System)
	}
	if strings.Contains(first.System, "Instructions of") {
		t.Error("the system prompt holds a skill's instructions")
	}
	if names := toolNames(first); !slices.Contains(names, skill.LoadTool) || !slices.Contains(names, skill.ReadTool) || !slices.Contains(names, "echo") {
		t.Errorf("tools %v, want the demo tools and the skill tools", names)
	}
	if got := lastToolResult(t, reqs[1]); !strings.Contains(got, "Instructions of release-notes.") || !strings.Contains(got, "references/template.md") {
		t.Errorf("skill_load gave %s", got)
	}
	var read struct{ Content string }
	if got := lastToolResult(t, reqs[2]); json.Unmarshal([]byte(got), &read) != nil || read.Content != "## v<version>\n" {
		t.Errorf("skill_read gave %s", got)
	}
	if plain := reqs[3]; plain.System != "You are plain." || slices.Contains(toolNames(plain), skill.LoadTool) {
		t.Errorf("an agent without skills got system %q and tools %v", plain.System, toolNames(plain))
	}

	// /readyz: degraded for the skipped folders, with the skills loaded.
	err = m.Component.Ready(t.Context())
	if !errors.Is(err, component.ErrDegraded) || !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "missing") {
		t.Errorf("Ready = %v, want degraded naming the broken and missing folders", err)
	}
	r, ok := m.Component.(component.Reporter)
	if !ok {
		t.Fatal("the agent component does not report")
	}
	if got := r.Report()["skills"]; !maps.EqualFunc(got.(map[string][]string), map[string][]string{"writer": {"release-notes", "sql-review"}}, slices.Equal) {
		t.Errorf("readyz skills %v", got)
	}
}

// TestServeSkillsInvalid: a required skill folder that fails stops startup
// with an error naming the agent, the skill and the rule.
func TestServeSkillsInvalid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSkill(t, filepath.Join(dir, "skills"), "Bad Name", "d", nil)
	writeSkill(t, filepath.Join(dir, "a"), "same", "d", nil)
	writeSkill(t, filepath.Join(dir, "b"), "same", "d", nil)
	for config, rule := range map[string]skill.Rule{
		`{"agents": [{"name": "w", "skills": ["skills"]}]}`:                 skill.RuleName,
		`{"agents": [{"name": "w", "skills": ["a", "b"]}]}`:                 skill.RuleDuplicate,
		`{"agents": [{"name": "w", "skills": [{"path": "nope"}]}]}`:         "",
		`{"agents": [{"name": "w", "skills": [{"path": "a", "x": true}]}]}`: "",
	} {
		_, err := newSkillAgentsErr(t, dir, config)
		var e *skill.Error
		switch {
		case err == nil:
			t.Errorf("%s: started", config)
		case !strings.Contains(err.Error(), "agents.w") && rule != "":
			t.Errorf("%s: %v, want it to name agents.w", config, err)
		case rule != "" && (!errors.As(err, &e) || e.Rule != rule):
			t.Errorf("%s: %v, want rule %s", config, err, rule)
		}
	}
}

// newSkillAgentsErr is newSkillAgents for configs that may not load.
func newSkillAgentsErr(t *testing.T, dir, config string) (component.Mount, error) {
	t.Helper()
	path := filepath.Join(dir, "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		return component.Mount{}, err
	}
	c := cli{getenv: func(string) string { return "" }, newModel: func(modelFlags) (llm.Model, error) { return llmtest.NewFake(t), nil }}
	env := serveEnv{components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second}
	log := slog.New(slog.DiscardHandler)
	m, _, err := c.newAgents(t.Context(), cfg, nil, nil, env, nil, scoped.Memory(nil), nil, clock.Real(), log)
	if err == nil {
		_ = m.Component.Shutdown(t.Context())
	}
	return m, err
}

// TestSearchConfigSkills: the search agent of deploy/local/agent/search.json
// loads its demo skill from the folder next to the config.
func TestSearchConfigSkills(t *testing.T) {
	t.Parallel()
	cfg, err := loadServeConfig(filepath.Join("..", "..", "deploy", "local", "agent", "search.json"))
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(cfg.Agents, func(a agentDef) bool { return a.Name == "search" })
	if i < 0 {
		t.Fatal("no search agent")
	}
	cat, info, err := loadSkills("agents.search", cfg.Agents[i].Skills)
	if err != nil || !slices.Equal(info.names, []string{"research-brief"}) || len(info.skipped) != 0 {
		t.Fatalf("skills %v skipped %v: %v", info.names, info.skipped, err)
	}
	s, err := cat.Get("research-brief")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("references/format.md"); err != nil {
		t.Errorf("the skill's format file: %v", err)
	}
}

// lastToolResult is the tool result in a request's last message.
func lastToolResult(t *testing.T, req llm.Request) string {
	t.Helper()
	for _, b := range req.Messages[len(req.Messages)-1].Content {
		if r, ok := b.(llm.ToolResult); ok {
			if r.IsError {
				t.Fatalf("tool error: %s", r.Content)
			}
			return r.Content
		}
	}
	t.Fatal("no tool result")
	return ""
}
