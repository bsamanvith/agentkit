package memory_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/inmem"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// The memory tool over the in-memory store: a model saves a preference in
// one conversation and reads it back in another. Calls are scoped by the
// tenant in the context.
func Example() {
	store, err := inmem.New(memory.Config{Scope: memory.ScopeTenant})
	if err != nil {
		panic(err)
	}
	mt, err := memory.NewTool(store)
	if err != nil {
		panic(err)
	}
	acme, _ := tenancy.NewTenant("acme")
	ctx := tenancy.With(context.Background(), acme)

	for _, call := range []string{
		`{"command":"create","path":"/memories/preferences.md","file_text":"Units: metric\n"}`,
		`{"command":"view","path":"/memories/preferences.md"}`,
	} {
		out, err := mt.Invoke(ctx, json.RawMessage(call))
		if err != nil {
			panic(err)
		}
		var text string
		_ = json.Unmarshal(out, &text)
		fmt.Println(text)
	}
	// Output:
	// File created successfully at: /memories/preferences.md
	// Here's the content of /memories/preferences.md with line numbers:
	//      1	Units: metric
}

// A keyword search: a quoted phrase must occur as it is, and the results come
// best first.
func ExampleIndex() {
	x, err := inmem.NewIndex(memory.IndexConfig{})
	if err != nil {
		panic(err)
	}
	acme, _ := tenancy.NewTenant("acme")
	ctx := tenancy.With(context.Background(), acme)
	for _, it := range []memory.Item{
		{ID: "bg", Text: "We ship with a blue green deployment."},
		{ID: "colours", Text: "The deployment dashboard is green, or blue when paused."},
		{ID: "canary", Text: "Canary deployment comes first."},
	} {
		if err := x.Put(ctx, it); err != nil {
			panic(err)
		}
	}
	for _, q := range []string{`"blue green" deployment`, "deployment canary"} {
		res, err := x.Search(ctx, memory.Query{Text: q, K: 3})
		if err != nil {
			panic(err)
		}
		fmt.Print(q, ":")
		for _, r := range res {
			fmt.Print(" ", r.Item.ID)
		}
		fmt.Println()
	}
	// Output:
	// "blue green" deployment: bg
	// deployment canary: canary bg colours
}
