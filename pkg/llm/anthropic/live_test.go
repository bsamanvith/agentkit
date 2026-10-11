//go:build live

package anthropic_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// requireCredentials skips the test unless the SDK can find credentials: an
// API key or auth token in the environment, or an active `ant auth login`
// profile.
func requireCredentials(t *testing.T) {
	t.Helper()
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		return
	}
	if ant, err := exec.LookPath("ant"); err == nil && exec.Command(ant, "auth", "status").Run() == nil {
		return
	}
	t.Skip("no Anthropic credentials: set ANTHROPIC_API_KEY or run `ant auth login`")
}

// TestLiveToolCall makes one real streaming request that calls a tool, then
// sends the result back (with the turn's thinking blocks) and gets the answer.
func TestLiveToolCall(t *testing.T) {
	t.Parallel()
	requireCredentials(t)
	m, err := anthropic.New(anthropic.WithMaxTokens(8000))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	req := llm.Request{
		System: "You are a test harness. Follow the instructions exactly.",
		Tools: []tool.Definition{{
			Name:        "echo",
			Description: "Returns the text it is given.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		}},
		Messages: []llm.Message{llm.User(llm.Text{Text: "Call the echo tool once with the text \"pineapple\", then tell me what it returned."})},
		Provider: anthropic.Options{Effort: anthropic.EffortLow},
	}
	events, err := drain(m.Generate(ctx, req))
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	checkEvents(t, events)
	first := *events[len(events)-1].Response
	t.Logf("first turn: stop %+v, usage %+v, %d blocks", first.Stop, first.Usage, len(first.Message.Content))
	if first.Stop.Kind != llm.StopToolUse {
		t.Fatalf("stop = %+v, want tool_use", first.Stop)
	}
	var results []llm.Block
	for _, b := range first.Message.Content {
		if u, ok := b.(llm.ToolUse); ok {
			var in struct{ Text string }
			if u.Name != "echo" || json.Unmarshal(u.Input, &in) != nil {
				t.Fatalf("unexpected tool call %s %s", u.Name, u.Input)
			}
			results = append(results, llm.ToolResult{ToolUseID: u.ID, Content: in.Text})
		}
	}
	if len(results) == 0 {
		t.Fatal("no tool call in a tool_use reply")
	}

	req.Messages = append(req.Messages, first.Message, llm.User(results...))
	second, err := llm.Collect(m.Generate(ctx, req))
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	t.Logf("second turn: stop %+v, usage %+v, text %q", second.Stop, second.Usage, second.Message.Text())
	if second.Stop.Kind != llm.StopEndTurn || second.Message.Text() == "" {
		t.Errorf("second turn = %+v, want a text answer", second)
	}
}
