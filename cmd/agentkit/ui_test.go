//go:build !nomodels

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// pickSurface is a ui_render input: a choice between two plans and a
// button. With label false, the button's label is missing.
func pickSurface(label bool) json.RawMessage {
	labelDef := ""
	if label {
		labelDef = `, {"id": "ok_label", "component": "Text", "text": "Choose"}`
	}
	return json.RawMessage(`{"messages": [
	  {"version": "v0.9", "createSurface": {"surfaceId": "pick_plan", "catalogId": "` + a2ui.BasicCatalogID + `"}},
	  {"version": "v0.9", "updateComponents": {"surfaceId": "pick_plan", "components": [
	    {"id": "root", "component": "Column", "children": ["plan", "ok"]},
	    {"id": "plan", "component": "ChoicePicker", "options": [{"label": "Basic", "value": "basic"}, {"label": "Pro", "value": "pro"}], "value": {"path": "/plan"}},
	    {"id": "ok", "component": "Button", "child": "ok_label", "action": {"event": {"name": "choose_plan", "context": {"plan": {"path": "/plan"}}}}}` + labelDef + `
	  ]}}
	]}`)
}

// TestServeUIAgent: an agent with "ui": "a2ui" has ui_render and one
// without has not; over agentclient, the UI agent's corrected surface
// streams as ui events, and a ui_action continues its conversation.
func TestServeUIAgent(t *testing.T) {
	t.Parallel()
	const config = `{
	  "mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
	  "agents": [
	    {"name": "painter", "model": {"id": "model-painter"}, "toolsets": ["demo"], "ui": "a2ui"},
	    {"name": "plain", "model": {"id": "model-plain"}, "toolsets": ["demo"]}
	  ]
	}`
	painter := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "u1", Name: a2ui.ToolName, Input: pickSurface(false)}),
		llmtest.ToolCalls(llm.ToolUse{ID: "u2", Name: a2ui.ToolName, Input: pickSurface(true)}),
		llmtest.Text("Pick a plan."),
		llmtest.Text("Pro it is."),
	)
	plain := llmtest.NewFake(t, llmtest.Text("hi"))
	c := serveAgents(t, config, map[string]*llmtest.Fake{"model-painter": painter, "model-plain": plain})
	ctx := t.Context()

	s, err := c.Stream(ctx, agentclient.RunRequest{Message: "Which plan?", Agent: "painter"})
	if err != nil {
		t.Fatal(err)
	}
	var surfaces []string
	var results []bool
	for {
		e, err := s.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch e.Type {
		case wire.TypeUI:
			if _, err := a2ui.Parse(e.UI); err != nil {
				t.Errorf("ui event %s: %v", e.UI, err)
			}
			surfaces = append(surfaces, e.SurfaceID)
		case wire.TypeToolResult:
			results = append(results, e.IsError)
		}
	}
	s.Close()
	if !slices.Equal(results, []bool{true, false}) || !slices.Equal(surfaces, []string{"pick_plan", "pick_plan"}) {
		t.Errorf("tool results (errors) %v and ui events of %v; want a failed call, then its two messages", results, surfaces)
	}

	run, err := c.Run(ctx, agentclient.RunRequest{ConversationID: s.ConversationID(), UIAction: &agentclient.UIAction{
		SurfaceID: "pick_plan", ComponentID: "ok", Action: "choose_plan", Context: json.RawMessage(`{"plan":["pro"]}`),
	}})
	if err != nil || run.Answer != "Pro it is." {
		t.Fatalf("the action's run = %+v, %v", run, err)
	}
	reqs := painter.Requests()
	last := reqs[len(reqs)-1].Messages
	if a, ok := a2ui.ParseActionText(last[len(last)-1].Text()); !ok || a.Name != "choose_plan" || string(a.Context) != `{"plan":["pro"]}` {
		t.Errorf("the model's last message %q is not the action", last[len(last)-1].Text())
	}

	if _, err := c.Run(ctx, agentclient.RunRequest{Message: "hi", Agent: "plain"}); err != nil {
		t.Fatal(err)
	}
	for name, fake := range map[string]*llmtest.Fake{"painter": painter, "plain": plain} {
		for i, req := range fake.Requests() {
			if has := slices.Contains(toolNames(req), a2ui.ToolName); has != (name == "painter") {
				t.Errorf("%s's request %d has tools %v", name, i, toolNames(req))
			}
		}
	}
	agents, err := c.Agents(ctx)
	if err != nil || len(agents) != 2 || agents[0].UI != a2ui.UIMode || agents[1].UI != "" {
		t.Errorf("Agents = %+v, %v; want painter with ui a2ui, plain without", agents, err)
	}
}

// TestServeRejectsBadUI: an unknown ui fails startup naming the agent's
// ui, and the UI tool is not added over a toolset's tool of its name.
func TestServeRejectsBadUI(t *testing.T) {
	t.Parallel()
	clash, err := tool.Func(a2ui.ToolName, "Not the UI tool.", func(context.Context, struct{}) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry([]tool.Tool{clash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withUI(t.Context(), reg); err == nil {
		t.Errorf("withUI over a tool named %s succeeded", a2ui.ToolName)
	}
	for name, config := range map[string]string{
		"unknown ui": `{"mcp": {"toolsets": [{"name": "demo", "type": "demo"}]}, "agents": [{"name": "a", "ui": "html"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "agentkit.json")
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadServeConfig(path)
			if err == nil {
				c := cli{getenv: func(string) string { return "" }, newModel: func(modelFlags) (llm.Model, error) { return llmtest.NewFake(t), nil }}
				env := serveEnv{components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second}
				log := slog.New(slog.DiscardHandler)
				built, problems, berr := c.buildToolsets(t.Context(), env, cfg, path, log)
				if berr != nil {
					err = berr
				} else {
					_, _, err = c.newAgents(t.Context(), cfg, built, problems, env, nil, scoped.Memory(nil), nil, clock.Real(), log)
				}
			}
			if err == nil || !strings.Contains(err.Error(), "agents.a.ui") {
				t.Errorf("startup with %s: %v, want an error naming agents.a.ui", name, err)
			}
		})
	}
}
