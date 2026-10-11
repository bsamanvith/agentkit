package server_test

import (
	"context"
	"fmt"
	"net/http/httptest"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/tool/tooltest"
)

func ExampleNew() {
	reg, err := tool.NewRegistry(tooltest.Fixtures())
	if err != nil {
		panic(err)
	}
	srv, err := server.New(map[string]tool.Registry{"fixtures": reg})
	if err != nil {
		panic(err)
	}
	ts := httptest.NewServer(srv) // in a real program: mux.Handle("/mcp/", srv)
	defer ts.Close()

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "example", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp/fixtures", DisableStandaloneSSE: true}, nil)
	if err != nil {
		panic(err)
	}
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		panic(err)
	}
	for _, t := range tools.Tools {
		fmt.Println(t.Name)
	}
	// Output:
	// block
	// echo
	// fail
}
