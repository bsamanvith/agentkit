package main

import (
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestGateway starts the example as configured by the shipped config.json and
// checks, with the SDK client, that each path serves exactly its own tools.
func TestGateway(t *testing.T) {
	t.Parallel()
	cfg, err := decodeConfig(defaultConfig)
	if err != nil {
		t.Fatalf("decodeConfig: %v", err)
	}
	handler, err := newHandler(t.Context(), cfg)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	connect := func(t *testing.T, name string) *mcp.ClientSession {
		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
		cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
			Endpoint: ts.URL + "/mcp/" + name, DisableStandaloneSSE: true,
		}, nil)
		if err != nil {
			t.Fatalf("connect %s: %v", name, err)
		}
		t.Cleanup(func() { _ = cs.Close() })
		return cs
	}
	tools := func(t *testing.T, cs *mcp.ClientSession) []string {
		res, err := cs.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		var names []string
		for _, tl := range res.Tools {
			names = append(names, tl.Name)
		}
		slices.Sort(names)
		return names
	}
	call := func(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("CallTool %s: res=%+v err=%v", name, res, err)
		}
		sc, _ := res.StructuredContent.(map[string]any)
		return sc
	}

	all := []string{"calculate", "current_time", "echo"}
	for name, want := range map[string][]string{
		"demo-eu":    all,
		"demo-ist":   all,
		"demo-maths": {"calculate"},
	} {
		if got := tools(t, connect(t, name)); !slices.Equal(got, want) {
			t.Errorf("/mcp/%s tools = %v, want %v", name, got, want)
		}
	}

	// Two instances of one connector, configured differently.
	if got := call(t, connect(t, "demo-eu"), "echo", map[string]any{"text": "hi"})["text"]; got != "[eu] hi" {
		t.Errorf("demo-eu echo = %v", got)
	}
	if got := call(t, connect(t, "demo-ist"), "echo", map[string]any{"text": "hi"})["text"]; got != "[ist] hi" {
		t.Errorf("demo-ist echo = %v", got)
	}
	if got := call(t, connect(t, "demo-maths"), "calculate", map[string]any{"op": "mul", "a": 6, "b": 7})["result"]; got != float64(42) {
		t.Errorf("demo-maths 6*7 = %v", got)
	}
}
