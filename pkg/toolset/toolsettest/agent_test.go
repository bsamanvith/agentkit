package toolsettest_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest/internal/toy"
)

// toolResults runs one agent turn loop of model over tools and returns the
// results of its tool calls, in order.
func toolResults(t *testing.T, model llm.Model, tools tool.Registry) []llm.ToolResult {
	t.Helper()
	h, err := harness.New(model, tools)
	if err != nil {
		t.Fatal(err)
	}
	var results []llm.ToolResult
	for ev, err := range h.Run(t.Context(), harness.Input{Message: harness.UserText("Find Go.")}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if ev.Kind == harness.EventToolDone {
			results = append(results, ev.ToolResult)
		}
	}
	return results
}

func search(id, input string) llmtest.Reply {
	return llmtest.ToolCalls(llm.ToolUse{ID: id, Name: "toy_search", Input: json.RawMessage(input)})
}

func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sameJSON(t *testing.T, got string, want []byte) bool {
	t.Helper()
	var g, w any
	if json.Unmarshal([]byte(got), &g) != nil || json.Unmarshal(want, &w) != nil {
		return false
	}
	return mustJSON(t, g) == mustJSON(t, w)
}

// TestAgentWithReplayedConnector: a fake model calls the real toy, served
// from its cassettes, and gets the recorded result.
func TestAgentWithReplayedConnector(t *testing.T) {
	t.Parallel()
	model := llmtest.NewFake(t, search("c1", `{"query": "go", "max_results": 2}`), llmtest.Text("Found Go."))
	res := toolResults(t, model, toolsettest.Replay(t, toyConnector(toy.BugNone)))
	if len(res) != 1 || res[0].IsError || !sameJSON(t, res[0].Content, golden(t, "search_go")) {
		t.Errorf("tool results %+v, want search_go's golden output", res)
	}
}

// TestAgentWithFake: a fake model calls a Fake of the toy; scripted output
// comes back, and input the real schema rejects reaches the model as an
// error.
func TestAgentWithFake(t *testing.T) {
	t.Parallel()
	f := toolsettest.NewFake(t, toyDefs(t), toolsettest.Output("toy_search", hit))
	model := llmtest.NewFake(t, search("c1", `{"query": "go"}`), search("c2", `{"max_results": 2}`), llmtest.Text("Found Go."))
	res := toolResults(t, model, f)
	if len(res) != 2 || res[0].IsError || !sameJSON(t, res[0].Content, []byte(mustJSON(t, hit))) || !res[1].IsError {
		t.Errorf("tool results %+v, want the scripted hit, then an input error", res)
	}
	if calls := f.Calls(); len(calls) != 2 {
		t.Errorf("the fake recorded %d calls, want 2", len(calls))
	}
}

// TestFakeFactoryInACatalog: a FakeFactory with the toy's type, in a catalog,
// is built from a config like a real connector and serves over MCP and to
// the agent, with no network.
func TestFakeFactoryInACatalog(t *testing.T) {
	t.Parallel()
	catalog, err := toolset.NewCatalog(fakeToy(t))
	if err != nil {
		t.Fatal(err)
	}
	var specs []toolset.Spec
	config := `[{"name": "search", "type": "toy", "settings": {"api_key_env": "TOY_API_KEY"}, "include": ["toy_search"]}]`
	if err := json.Unmarshal([]byte(config), &specs); err != nil {
		t.Fatal(err)
	}
	sets, problems, err := toolset.Build(t.Context(), catalog, specs)
	if err != nil || len(problems) > 0 {
		t.Fatalf("Build: %v %v", err, problems)
	}

	srv, err := server.New(sets)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(t.Context(),
		&mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp/search", DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	listed, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(listed.Tools) != 1 || listed.Tools[0].Name != "toy_search" {
		t.Fatalf("ListTools = %+v, %v; want toy_search only", listed, err)
	}
	got, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "toy_search", Arguments: map[string]any{"query": "go"}})
	if err != nil || got.IsError || !sameJSON(t, got.Content[0].(*mcp.TextContent).Text, golden(t, "search_go")) {
		t.Errorf("CallTool over MCP = %+v, %v; want search_go's golden output", got, err)
	}

	model := llmtest.NewFake(t, search("c1", `{"query": "cobol"}`), llmtest.Text("Nothing."))
	res := toolResults(t, model, sets["search"])
	if len(res) != 1 || res[0].IsError || !sameJSON(t, res[0].Content, golden(t, "search_nothing")) {
		t.Errorf("tool results %+v, want search_nothing's golden output", res)
	}
}
