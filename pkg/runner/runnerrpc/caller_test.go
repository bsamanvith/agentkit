package runnerrpc_test

import (
	"context"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// A remote run acts for the caller's tenant and user, which its tools read
// their credentials by.
func TestRemoteRunActsForTheCaller(t *testing.T) {
	t.Parallel()
	seen := make(chan string, 1)
	a := agentFunc(func(ctx context.Context, yield func(harness.Event, error) bool) {
		tn, _ := tenancy.From(ctx)
		u, _ := tenancy.User(ctx)
		seen <- tn.ID() + "/" + u
		yield(harness.Event{Kind: harness.EventTextDelta, Turn: 1, Text: "x"}, nil)
	})
	acme, err := tenancy.NewTenant("acme")
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenancy.WithUser(tenancy.With(t.Context(), acme), "ann")
	for _, err := range remote(t, a).Run(ctx, harness.Input{Message: llm.User(llm.Text{Text: "x"})}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := <-seen; got != "acme/ann" {
		t.Errorf("remote run acted for %q, want acme/ann", got)
	}
}
