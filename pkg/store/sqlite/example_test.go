package sqlite_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// A conversation outlives the process that wrote it: a store opened later on
// the same file reads it back.
func Example() {
	dir, err := os.MkdirTemp("", "agentkit")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "agentkit.db")
	acme, _ := tenancy.NewTenant("acme")
	ctx := tenancy.With(context.Background(), acme)

	s, err := sqlite.Open(ctx, path)
	if err != nil {
		fmt.Println(err)
		return
	}
	err = s.Append(ctx, "conv-1", store.Entry{
		Seq:      1,
		Kind:     store.KindPrompt,
		Messages: []llm.Message{llm.User(llm.Text{Text: "hello"})},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	_ = s.Close() // the process ends

	s, err = sqlite.Open(ctx, path)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer s.Close()
	view, _ := s.Context(ctx, "conv-1")
	fmt.Println("next entry goes at", view.Seq+1, "after", view.Entries[0].Messages[0].Text())
	// Output:
	// next entry goes at 2 after hello
}
