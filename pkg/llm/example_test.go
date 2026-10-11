package llm_test

import (
	"context"
	"fmt"
	"iter"
	"strconv"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// greeter is a minimal Model: it streams one text block.
type greeter struct{}

func (greeter) Generate(_ context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		text := "Hello! You said: " + req.Messages[len(req.Messages)-1].Text()
		reply := llm.Response{
			Message: llm.Assistant(llm.Text{Text: text}),
			Stop:    llm.StopReason{Kind: llm.StopEndTurn},
			Usage:   llm.Usage{InputTokens: 5, OutputTokens: 4},
		}
		for _, ev := range []llm.Event{
			{Kind: llm.EventTextDelta, Index: 0, Text: text[:7]},
			{Kind: llm.EventTextDelta, Index: 0, Text: text[7:]},
			{Kind: llm.EventBlockDone, Index: 0, Block: llm.Text{Text: text}},
			{Kind: llm.EventMessageDone, Response: &reply},
		} {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func ExampleCollect() {
	var m llm.Model = greeter{}
	req := llm.Request{Messages: []llm.Message{llm.User(llm.Text{Text: "hi"})}}

	// Stream: show text as it arrives.
	var pieces []string
	for ev, err := range m.Generate(context.Background(), req) {
		if err != nil {
			panic(err)
		}
		if ev.Kind == llm.EventTextDelta {
			pieces = append(pieces, strconv.Quote(ev.Text))
		}
	}
	fmt.Println(strings.Join(pieces, " "))

	// Or wait for the whole answer.
	resp, err := llm.Collect(m.Generate(context.Background(), req))
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Message.Text(), resp.Stop.Kind, resp.Usage.OutputTokens)
	// Output:
	// "Hello! " "You said: hi"
	// Hello! You said: hi end_turn 4
}
