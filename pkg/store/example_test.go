package store_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// A writer appends after the last entry it read. A retry of the same entry is
// harmless; a writer whose view is stale conflicts instead of interleaving.
func Example() {
	acme, _ := tenancy.NewTenant("acme")
	ctx := tenancy.With(context.Background(), acme)
	var s store.ConversationStore = memory.New()

	view, _ := s.Context(ctx, "conv-1") // empty: Seq is 0
	prompt := store.Entry{
		Seq:      view.Seq + 1,
		Kind:     store.KindPrompt,
		Messages: []llm.Message{llm.User(llm.Text{Text: "What is 2+2?"})},
		RunID:    "run-1",
	}
	fmt.Println("append:", s.Append(ctx, "conv-1", prompt))
	fmt.Println("retry:", s.Append(ctx, "conv-1", prompt))

	stale := store.Entry{Seq: view.Seq + 1, Kind: store.KindPrompt, Messages: []llm.Message{llm.User(llm.Text{Text: "Hi"})}}
	fmt.Println("stale writer conflicts:", errors.Is(s.Append(ctx, "conv-1", stale), store.ErrConflict))

	view, _ = s.Context(ctx, "conv-1")
	fmt.Println("context:", len(view.Entries), "entry, next seq", view.Seq+1)

	_, err := s.Context(context.Background(), "conv-1")
	fmt.Println("no tenant:", errors.Is(err, tenancy.ErrNoTenant))
	// Output:
	// append: <nil>
	// retry: <nil>
	// stale writer conflicts: true
	// context: 1 entry, next seq 2
	// no tenant: true
}
