package toolsettest

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// mcpFor returns a session serving the tools a call is built with: cs, which
// serves the connector's, unless the call has settings of its own.
func (r *rig) mcpFor(call Call, cs *mcp.ClientSession) *mcp.ClientSession {
	r.t.Helper()
	if len(call.Settings) == 0 {
		return cs
	}
	return serveMCP(r.t, r.toolsFor(call), r.env.Logger)
}

// serveMCP serves tools with pkg/mcp/server and connects the SDK's client.
func serveMCP(t testing.TB, tools []tool.Tool, log *slog.Logger) *mcp.ClientSession {
	t.Helper()
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatalf("[mcp] registry: %v", err)
	}
	srv, err := server.New(map[string]tool.Registry{"conformance": reg}, server.WithLogger(log))
	if err != nil {
		t.Fatalf("[mcp] server: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "toolsettest", Version: "0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: ts.URL + "/mcp/conformance", DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("[mcp] connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// listMCP returns the tools listed over MCP by name, as definitions.
func listMCP(t testing.TB, cs *mcp.ClientSession) map[string]tool.Definition {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("[mcp] ListTools: %v", err)
	}
	out := map[string]tool.Definition{}
	for _, tl := range res.Tools {
		d := tool.Definition{Name: tl.Name, Description: tl.Description}
		d.InputSchema, _ = json.Marshal(tl.InputSchema)
		if tl.OutputSchema != nil {
			d.OutputSchema, _ = json.Marshal(tl.OutputSchema)
		}
		out[tl.Name] = d
	}
	return out
}

// mcpResult is a tool call's result over MCP, reduced to what the suite checks.
type mcpResult struct {
	IsError    bool
	Text       string // all text content, concatenated
	Structured json.RawMessage
}

// callMCP calls a tool over MCP. A protocol error fails the test.
func callMCP(t testing.TB, cs *mcp.ClientSession, name string, input json.RawMessage) mcpResult {
	t.Helper()
	var args any
	if err := json.Unmarshal(input, &args); err != nil {
		t.Fatalf("[mcp] input %s: %v", input, err)
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("[mcp] CallTool %s: %v", name, err)
	}
	out := mcpResult{IsError: res.IsError}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out.Text += tc.Text
		}
	}
	if res.StructuredContent != nil {
		out.Structured, _ = json.Marshal(res.StructuredContent)
	}
	return out
}
