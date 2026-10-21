//go:build live

package openaicompat_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/option"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/openaicompat"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// liveDefaultModel is the model used against OpenAI when
// OPENAICOMPAT_TEST_MODEL is not set.
const liveDefaultModel = "gpt-5-mini"

// liveModel returns a model on the configured endpoint, or skips: OpenAI
// with OPENAI_API_KEY, or any server at OPENAI_BASE_URL (for Ollama,
// http://localhost:11434/v1) with OPENAICOMPAT_TEST_MODEL naming the model.
func liveModel(t *testing.T) *openaicompat.Model {
	t.Helper()
	base, key := os.Getenv("OPENAI_BASE_URL"), os.Getenv("OPENAI_API_KEY")
	model := os.Getenv("OPENAICOMPAT_TEST_MODEL")
	switch {
	case base == "" && key == "":
		t.Skip("no endpoint: set OPENAI_API_KEY, or OPENAI_BASE_URL and OPENAICOMPAT_TEST_MODEL (Ollama: http://localhost:11434/v1)")
	case base != "" && model == "":
		t.Skip("OPENAI_BASE_URL is set but OPENAICOMPAT_TEST_MODEL is not")
	case model == "":
		model = liveDefaultModel
	}
	opts := []openaicompat.Option{openaicompat.WithModel(model)}
	if strings.HasPrefix(base, "http://") {
		// A key over plain HTTP: the SDK allows it to loopback only, with this.
		opts = append(opts, openaicompat.WithClientOptions(option.WithUnsafeAllowHTTP()))
	}
	m, err := openaicompat.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestLiveToolCall makes one real streaming request that calls a tool, then
// sends the result back (with the turn's reasoning, if any) and gets the
// answer.
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
