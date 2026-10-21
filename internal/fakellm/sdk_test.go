package fakellm_test

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Criterion 5: the official Anthropic Go SDK, pointed at fakellm, completes
// requests: a streamed tool call, its result sent back, and a plain answer.
func TestOfficialSDK(t *testing.T) {
	t.Parallel()
	srv := newServer(t, nil, "s", `
{"match": "sunny", "response": {"text": "It is sunny."}}
{"match": "weather", "response": {"tool_calls": [{"name": "get_weather", "input": {"city": "Paris"}}]}}
`)
	client := anthropic.NewClient(
		option.WithBaseURL(srv.URL), option.WithAPIKey("fake"), option.WithMaxRetries(0),
	)
	params := anthropic.MessageNewParams{
		Model:     "claude-test",
		MaxTokens: 100,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("What is the weather?"))},
		Tools: []anthropic.ToolUnionParam{{OfTool: &anthropic.ToolParam{
			Name:        "get_weather",
			InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{"city": map[string]any{"type": "string"}}},
		}}},
	}

	stream := client.Messages.NewStreaming(t.Context(), params)
	var first anthropic.Message
	for stream.Next() {
		if err := first.Accumulate(stream.Current()); err != nil {
			t.Fatal(err)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if first.StopReason != anthropic.StopReasonToolUse || len(first.Content) != 1 {
		t.Fatalf("streamed message %+v", first)
	}
	call := first.Content[0].AsToolUse()
	if call.Name != "get_weather" || string(call.Input) != `{"city":"Paris"}` {
		t.Fatalf("tool call %+v", call)
	}

	params.Messages = append(params.Messages, first.ToParam(),
		anthropic.NewUserMessage(anthropic.NewToolResultBlock(call.ID, "sunny, 21C", false)))
	second, err := client.Messages.New(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if second.StopReason != anthropic.StopReasonEndTurn || len(second.Content) != 1 || second.Content[0].Text != "It is sunny." {
		t.Fatalf("message %+v", second)
	}
	if second.Usage.InputTokens == 0 || second.Usage.OutputTokens == 0 {
		t.Errorf("usage %+v", second.Usage)
	}
}
