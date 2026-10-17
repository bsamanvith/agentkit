//go:build !nomcp

package main

// The toolset type "mcp" as the binary uses it: two external servers with a
// tool of the same name in one agent, and the real binary re-serving an
// external server's tools with an unreachable one skipped.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/mcp/client"
	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// upstreams serves two MCP toolsets, /mcp/a and /mcp/b, each with a tool
// named "search" that says which server answered.
func upstreams(t *testing.T) *httptest.Server {
	t.Helper()
	regs := map[string]tool.Registry{}
	for _, name := range []string{"a", "b"} {
		search, err := tool.Func("search", "Search "+name+".", func(_ context.Context, in struct {
			Q string `json:"q"`
		},
		) (struct {
			From string `json:"from"`
		}, error,
		) {
			return struct {
				From string `json:"from"`
			}{name + ": " + in.Q}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if regs[name], err = tool.NewRegistry([]tool.Tool{search}); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := server.New(regs, server.WithBasePath("/mcp"))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.CloseClientConnections() // clients keep a notification stream open
		ts.Close()
	})
	return ts
}

// Two servers that both have "search" are two toolsets; an agent using both
// calls them a__search and b__search.
func TestMCPClientNames(t *testing.T) {
	t.Parallel()
	up := upstreams(t)
	catalog, err := toolset.NewCatalog(client.Factory())
	if err != nil {
		t.Fatal(err)
	}
	regs, _, err := toolset.Build(t.Context(), catalog, []toolset.Spec{
		{Name: "a", Type: client.Type, Settings: json.RawMessage(`{"url": "` + up.URL + `/mcp/a"}`)},
		{Name: "b", Type: client.Type, Settings: json.RawMessage(`{"url": "` + up.URL + `/mcp/b"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentapi.MergeTools(t.Context(), regs, false); err == nil {
		t.Error("two servers' search tools merged without a prefix")
	}
	merged, err := agentapi.MergeTools(t.Context(), regs, true)
	if err != nil {
		t.Fatal(err)
	}
	defs, _ := merged.List(t.Context())
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	if !slices.Equal(names, []string{"a__search", "b__search"}) {
		t.Errorf("tools = %v, want a__search and b__search", names)
	}
	for _, name := range []string{"a", "b"} {
		out, err := merged.Invoke(t.Context(), name+"__search", json.RawMessage(`{"q": "go"}`))
		if want := `{"from":"` + name + `: go"}`; err != nil || string(out) != want {
			t.Errorf("%s__search = %s, %v; want %s", name, out, err, want)
		}
	}
}

// TestE2EServeMCPClient: the binary uses an external MCP server as a toolset
// and serves its tools again at /mcp/a; an unreachable optional server is
// skipped and named in /readyz.
func TestE2EServeMCPClient(t *testing.T) {
	t.Parallel()
	up := upstreams(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String() + "/mcp"
	_ = l.Close()
	p := startServe(t, `{"mcp": {"toolsets": [
		{"name": "a", "type": "mcp", "settings": {"url": "`+up.URL+`/mcp/a"}},
		{"name": "gone", "type": "mcp", "settings": {"url": "`+dead+`", "connect_timeout": "2s"}, "optional": true}
	]}}`, 10*time.Second)
	var ready struct {
		Components []struct{ Name, Status, Reason string }
	}
	if code := p.getJSON(t, "/readyz", &ready); code != http.StatusOK || len(ready.Components) != 1 ||
		ready.Components[0].Status != "degraded" || !strings.Contains(ready.Components[0].Reason, `"gone"`) {
		t.Errorf("/readyz = %d %+v, want mcp-server degraded by toolset \"gone\"", code, ready)
	}
	cs := connectMCP(t, p.url+"/mcp/a", nil)
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "search", Arguments: map[string]any{"q": "hi"}})
	if err != nil || res.IsError {
		t.Fatalf("search through the binary: %v %+v", err, res)
	}
	if got, _ := json.Marshal(res.StructuredContent); string(got) != `{"from":"a: hi"}` {
		t.Errorf("search through the binary = %s", got)
	}
}
