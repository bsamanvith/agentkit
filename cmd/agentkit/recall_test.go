//go:build !noagent

package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/memory"
)

func recallCall(id, name, input string) llmtest.Reply {
	return llmtest.ToolCalls(llm.ToolUse{ID: id, Name: name, Input: json.RawMessage(input)})
}

// TestRecallThroughTheAPI: an agent with an index saves an item in one
// conversation; in a new one, auto recall puts it in the user turn (the
// system prompt unchanged) and recall_search finds it. An agent on the same
// file without auto_recall gets the tools and no recalled block.
func TestRecallThroughTheAPI(t *testing.T) {
	t.Parallel()
	db := filepath.Join(t.TempDir(), "memories.db")
	index := func(auto bool) string {
		return `{"index": {"backend": "sqlite", "path": ` + strconv.Quote(db) + `, "auto_recall": ` + strconv.FormatBool(auto) + `, "k": 3}}`
	}
	config := `{"agents": [
	  {"name": "auto", "model": {"id": "auto"}, "system": "Be brief.", "memory": ` + index(true) + `},
	  {"name": "plain", "model": {"id": "plain"}, "system": "Be brief.", "memory": ` + index(false) + `}]}`
	auto := llmtest.NewFake(t,
		recallCall("save", memory.RecallSaveTool, `{"text":"The user prefers metric units.","metadata":{"kind":"preference"}}`),
		llmtest.Text("Noted."),
		recallCall("find", memory.RecallSearchTool, `{"query":"metric","filter":{"kind":"preference"}}`),
		llmtest.Text("Metric."),
	)
	plain := llmtest.NewFake(t, llmtest.Text("No idea."))
	s, err := startAgents(t, config, map[string]*llmtest.Fake{"auto": auto, "plain": plain})
	if err != nil {
		t.Fatal(err)
	}
	c := s.client(t, "")
	if _, err := c.Run(t.Context(), agentclient.RunRequest{Agent: "auto", Message: "I like metric units."}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(t.Context(), agentclient.RunRequest{Agent: "auto", Message: "Which units do I like?"}); err != nil {
		t.Fatal(err)
	}
	reqs := auto.Requests()
	if out, ok := toolResult(reqs[1], "save"); !ok || !strings.Contains(out, `"id":"m_`) {
		t.Errorf("save: %q, ok %v", out, ok)
	}
	if n := len(reqs[0].Messages[0].Content); n != 1 {
		t.Errorf("the first message, with nothing to recall, has %d blocks", n)
	}
	second := reqs[2]
	if len(second.Messages) != 1 {
		t.Fatalf("run 2 is not a new conversation: %d messages", len(second.Messages))
	}
	turn := second.Messages[0]
	if len(turn.Content) != 2 || turn.Content[1].(llm.Text).Text != "Which units do I like?" {
		t.Fatalf("user turn = %+v", turn)
	}
	block := turn.Content[0].(llm.Text).Text
	if !strings.HasPrefix(block, `<recalled source="memory">`) || !strings.Contains(block, "The user prefers metric units.") {
		t.Errorf("recalled block = %q", block)
	}
	if second.System != reqs[0].System || strings.Contains(second.System, "metric") {
		t.Errorf("system prompt changed: %q, then %q", reqs[0].System, second.System)
	}
	if out, ok := toolResult(reqs[3], "find"); !ok || !strings.Contains(out, "metric units") {
		t.Errorf("search: %q, ok %v", out, ok)
	}
	for _, name := range []string{memory.RecallGetTool, memory.RecallSaveTool, memory.RecallSearchTool} {
		if !slices.Contains(toolNames(second), name) {
			t.Errorf("tools %v lack %s", toolNames(second), name)
		}
	}
	if slices.Contains(toolNames(second), memory.ToolName) {
		t.Error("an agent with only an index has the memory tool")
	}

	// auto_recall off: the same item is there, and nothing is recalled.
	if _, err := c.Run(t.Context(), agentclient.RunRequest{Agent: "plain", Message: "Which units do I like?"}); err != nil {
		t.Fatal(err)
	}
	if turn := plain.Requests()[0].Messages[0]; len(turn.Content) != 1 {
		t.Errorf("auto_recall off: user turn = %+v", turn)
	}
}

// TestRecallConfigErrors: a bad index section fails startup, naming it.
func TestRecallConfigErrors(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ memory, want string }{
		{`{}`, "agent.memory: give a store, an index or both"},
		{`{"index": {"backend": "pinecone"}}`, `agent.memory.index.backend "pinecone"`},
		{`{"index": {"backend": "sqlite"}}`, "needs a path"},
		{`{"index": {"backend": "memory", "path": "x.db"}}`, "leave out path"},
		{`{"index": {"backend": "memory", "scope": "team"}}`, `scope "team"`},
		{`{"index": {"backend": "memory", "k": 99}}`, "agent.memory.index.k 99"},
		{`{"index": {"backend": "memory", "max_items": -1}}`, "negative limit"},
		{`{"index": {"backend": "memory"}, "native_tool": true}`, "native memory tool needs a store"},
	} {
		config := `{"agent": {"model": {"id": "m"}, "memory": ` + c.memory + `}}`
		_, err := startAgents(t, config, map[string]*llmtest.Fake{"m": llmtest.NewFake(t)})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("memory %s: %v, want an error with %q", c.memory, err, c.want)
		}
	}
}
