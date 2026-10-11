package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// calculator is a minimal model: it asks for the add tool, then reports the
// result it is given.
type calculator struct{}

func (calculator) Generate(_ context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	resp := llm.Response{
		Message: llm.Assistant(llm.ToolUse{ID: "call-1", Name: "add", Input: json.RawMessage(`{"a":2,"b":3}`)}),
		Stop:    llm.StopReason{Kind: llm.StopToolUse},
	}
	if last := req.Messages[len(req.Messages)-1]; len(req.Messages) > 1 {
		got := last.Content[0].(llm.ToolResult).Content
		resp = llm.Response{
			Message: llm.Assistant(llm.Text{Text: "2 + 3 = " + got}),
			Stop:    llm.StopReason{Kind: llm.StopEndTurn},
		}
	}
	return func(yield func(llm.Event, error) bool) {
		yield(llm.Event{Kind: llm.EventMessageDone, Response: &resp}, nil)
	}
}

func ExampleHarness_Run() {
	type input struct {
		A int `json:"a"`
		B int `json:"b"`
	}
	add, err := tool.Func("add", "Add two integers.",
		func(_ context.Context, in input) (int, error) { return in.A + in.B, nil })
	if err != nil {
		panic(err)
	}
	reg, err := tool.NewRegistry([]tool.Tool{add})
	if err != nil {
		panic(err)
	}
	h, err := harness.New(calculator{}, reg)
	if err != nil {
		panic(err)
	}

	for ev, err := range h.Run(context.Background(), harness.Input{Message: harness.UserText("What is 2 + 3?")}) {
		if err != nil {
			panic(err)
		}
		switch ev.Kind {
		case harness.EventToolStart:
			fmt.Printf("calling %s %s\n", ev.ToolUse.Name, ev.ToolUse.Input)
		case harness.EventToolDone:
			fmt.Printf("result %s (error: %v)\n", ev.ToolResult.Content, ev.ToolResult.IsError)
		case harness.EventRunDone:
			fmt.Printf("%s [%d turns, %d messages]\n", ev.Result.Final.Text(), ev.Result.Turns, len(ev.Result.Transcript))
		}
	}
	// Output:
	// calling add {"a":2,"b":3}
	// result 5 (error: false)
	// 2 + 3 = 5 [2 turns, 4 messages]
}
