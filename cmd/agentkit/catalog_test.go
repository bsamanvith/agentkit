package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// fakeDemo is a catalog whose demo type is a fake with the real demo
// definitions: echo answers in capitals, the other tools are unscripted.
func fakeDemo(t *testing.T) (*toolsettest.FakeToolset, func() (*toolset.Catalog, error)) {
	t.Helper()
	defs := toolsettest.Definitions(t, func(toolsettest.Env) toolset.Factory { return toolset.Demo() }, nil)
	f := toolsettest.FakeFactory(t, toolset.DemoType, defs,
		toolsettest.OutputFunc("echo", func(_ context.Context, input json.RawMessage) (any, error) {
			var in struct{ Text string }
			err := json.Unmarshal(input, &in)
			return map[string]string{"text": "FAKE " + in.Text}, err
		}))
	return f, func() (*toolset.Catalog, error) { return toolset.NewCatalog(f) }
}

// TestRunWithAFakeCatalog: agentkit run builds its tools from the catalog a
// test hands it, so the agent calls the fake.
func TestRunWithAFakeCatalog(t *testing.T) {
	t.Parallel()
	f, catalog := fakeDemo(t)
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "echo", Input: json.RawMessage(`{"text": "hi"}`)}),
		llmtest.Text("Done."),
	)
	var stdout, stderr bytes.Buffer
	c := cli{stdout: &stdout, stderr: &stderr, catalog: catalog, newModel: func(modelFlags) (llm.Model, error) { return model, nil }}
	if code := c.main(t.Context(), []string{"run", "Echo hi."}); code != 0 {
		t.Fatalf("exit %d\n%s", code, &stderr)
	}
	mustContain(t, stdout.String(), `← echo (`, `): {"text":"FAKE hi"}`)
	if calls := f.Calls(); len(calls) != 1 || calls[0].Tool != "echo" {
		t.Errorf("fake calls %+v, want one echo", calls)
	}
}

// TestServeMCPWithAFakeCatalog: agentkit serve builds the configured
// toolsets from the catalog a test hands it and serves them over MCP.
func TestServeMCPWithAFakeCatalog(t *testing.T) {
	t.Parallel()
	_, catalog := fakeDemo(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"mcp": {"toolsets": [{"name": "demo", "type": "demo", "include": ["echo"]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	c := cli{catalog: catalog}
	built, problems, err := c.buildToolsets(t.Context(), serveEnv{components: []string{mcpServerName}}, cfg, path, log)
	if err != nil {
		t.Fatal(err)
	}
	m, err := newMCPServer(t.Context(), cfg.MCP, built, problems, log)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(m.Component.Handler())
	t.Cleanup(ts.Close)
	cs := connectMCP(t, ts.URL+"/mcp/demo", nil)
	t.Cleanup(func() { _ = cs.Close() })
	if got, err := echo(t, cs, "hi"); err != nil || got != "FAKE hi" {
		t.Errorf("echo over MCP = %q, %v; want the fake's %q", got, err, "FAKE hi")
	}
}
