package flow_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/flow"
)

// A sequence that drafts a report, waits for a human sign-off, then publishes.
// The first Run stops at the approval; Resume with a decision picks up there
// without drafting again.
func ExampleRun() {
	ctx := context.Background()
	cp := flow.NewMemory()

	report := flow.Sequence("report",
		flow.Func("draft", func(_ context.Context, st *flow.State) error {
			fmt.Println("drafting")
			return flow.Set(st, "text", "Q3 summary")
		}),
		flow.Approve("sign-off"),
		flow.Func("publish", func(_ context.Context, st *flow.State) error {
			text, err := flow.Get[string](st, "text")
			fmt.Println("publishing:", text)
			return err
		}),
	)

	_, err := flow.Run(ctx, "run-1", report, flow.WithCheckpointer(cp))
	var waiting *flow.SuspendedError
	if errors.As(err, &waiting) {
		fmt.Println("waiting for", waiting.Paths)
	}

	_, err = flow.Resume(ctx, "run-1", report,
		flow.WithCheckpointer(cp),
		flow.WithDecision("report/sign-off", flow.Decision{Approved: true}))
	fmt.Println("resumed:", err)
	// Output:
	// drafting
	// waiting for [report/sign-off]
	// publishing: Q3 summary
	// resumed: <nil>
}
