//go:build live

package gemini_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/gemini"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// liveModel returns a model on the Gemini API, or skips without a key
// (GEMINI_API_KEY or GOOGLE_API_KEY; make test-live also takes GEMINI_KEY).
// GEMINI_TEST_MODEL names the model (default gemini.DefaultModel).
func liveModel(t *testing.T, opts ...gemini.Option) *gemini.Model {
	t.Helper()
	if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
		t.Skip("no key: set GEMINI_API_KEY")
	}
	m, err := gemini.New(append([]gemini.Option{gemini.WithModel(os.Getenv("GEMINI_TEST_MODEL"))}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestLiveToolCall makes one real streaming request that calls a tool, then
// sends the result back (with its thought signature) and gets the answer.
func TestLiveToolCall(t *testing.T) {
	t.Parallel()
	m := liveModel(t)
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

// TestLiveThoughts asks for thought summaries at a low thinking level, then
// sends the reply back, thoughts and signatures included, for a second
// answer.
func TestLiveThoughts(t *testing.T) {
	t.Parallel()
	m := liveModel(t, gemini.WithOptions(gemini.Options{ThinkingLevel: "low", IncludeThoughts: true}))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	req := llm.Request{Messages: []llm.Message{llm.User(llm.Text{Text: "Is 391 a prime number? Answer yes or no, then one sentence why."})}}
	events, err := drain(m.Generate(ctx, req))
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	checkEvents(t, events)
	first := *events[len(events)-1].Response
	var thoughts int
	for _, b := range first.Message.Content {
		if o, ok := b.(llm.Opaque); ok {
			thoughts++
			t.Logf("opaque block: %.120s", o.Data)
		}
	}
	t.Logf("first turn: stop %+v, usage %+v, %d blocks, %d opaque, text %q", first.Stop, first.Usage, len(first.Message.Content), thoughts, first.Message.Text())
	if first.Stop.Kind != llm.StopEndTurn || first.Message.Text() == "" {
		t.Fatalf("first turn = %+v, want a text answer", first)
	}

	req.Messages = append(req.Messages, first.Message, llm.User(llm.Text{Text: "And 397?"}))
	second, err := llm.Collect(m.Generate(ctx, req))
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	t.Logf("second turn: stop %+v, usage %+v, text %q", second.Stop, second.Usage, second.Message.Text())
	if second.Stop.Kind != llm.StopEndTurn || second.Message.Text() == "" {
		t.Errorf("second turn = %+v, want a text answer", second)
	}
}
