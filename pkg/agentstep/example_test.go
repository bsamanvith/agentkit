package agentstep_test

import (
	"context"
	"fmt"
	"iter"

	"github.com/bsamanvith/agentkit/pkg/agentstep"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// echoModel answers every prompt by repeating it.
type echoModel struct{}

func (echoModel) Generate(_ context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	resp := llm.Response{
		Message: llm.Assistant(llm.Text{Text: "You said: " + req.Messages[len(req.Messages)-1].Text()}),
		Stop:    llm.StopReason{Kind: llm.StopEndTurn},
	}
	return func(yield func(llm.Event, error) bool) {
		yield(llm.Event{Kind: llm.EventMessageDone, Response: &resp}, nil)
	}
}

func ExampleNew() {
	ctx := context.Background()
	tools, _ := tool.NewRegistry(nil)
	h, _ := harness.New(echoModel{}, tools)
	saver := agentstep.NewMemorySaver() // a store in production
	ask, _ := agentstep.New("ask", h, saver, agentstep.Keys{Input: "question", Output: "reply"})

	st := flow.NewState()
	_ = flow.Set(st, "question", "hello")
	st, err := flow.Run(ctx, "run-1", flow.Sequence("demo", ask), flow.WithCheckpointer(flow.NewMemory()), flow.WithState(st))
	if err != nil {
		panic(err)
	}
	out, _ := flow.Get[agentstep.Output](st, "reply")
	fmt.Println(out.Answer)

	saved, _ := saver.Load(ctx, "run-1", "demo/ask")
	fmt.Println(len(saved), "messages saved")
	// Output:
	// You said: hello
	// 2 messages saved
}
