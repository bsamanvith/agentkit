package embed_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/embed"
	"github.com/bsamanvith/agentkit/pkg/memory/inmem"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// An index searching by meaning, with an OpenAI-compatible server (here a
// stand-in that answers every text with the same vector).
func ExampleNewOpenAI() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data": [{"index": 0, "embedding": [0.6, 0.8]}]}`)
	}))
	defer srv.Close()

	e, err := embed.NewOpenAI(embed.Config{Model: "local-model", Dimensions: 2, BaseURL: srv.URL + "/v1"})
	if err != nil {
		panic(err)
	}
	x, err := inmem.NewIndex(memory.IndexConfig{Embedder: e, Modes: []memory.Mode{memory.ModeSemantic}})
	if err != nil {
		panic(err)
	}
	tn, _ := tenancy.NewTenant("acme")
	ctx := tenancy.With(context.Background(), tn)
	if err := x.Put(ctx, memory.Item{ID: "seat", Text: "I always take an aisle seat."}); err != nil {
		panic(err)
	}
	res, err := x.Search(ctx, memory.Query{Text: "flight preferences"})
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s %.2f %s\n", res[0].Item.ID, res[0].Score, res[0].Mode)
	// Output: seat 1.00 semantic
}
