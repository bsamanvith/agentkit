package main

// The Parallel connector as the binary serves it: over MCP at /mcp/parallel
// (in process, against a fake upstream named by the base_url setting), and
// skipped and reported in /readyz when its key is missing (the real binary).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/connectors/parallel"
	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

const parallelCanary = "pk-canary-mcp-5d1e9b7a3c"

// TestParallelOverMCP: /mcp/parallel lists exactly parallel_search and
// parallel_extract with input and output schemas; a call through the SDK
// client returns the mapped result; an upstream echoing the key leaks it
// into no MCP response.
func TestParallelOverMCP(t *testing.T) {
	t.Parallel()
	var keyHeaders []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyHeaders = append(keyHeaders, r.Header.Values("X-Api-Key")...)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/search" && strings.Contains(string(body), `"echo"`):
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"type": "error", "error": {"ref_id": "r", "message": "bad request from `+parallelCanary+`"}}`)
		case r.URL.Path == "/v1/search":
			_, _ = io.WriteString(w, `{"search_id": "search_1", "session_id": "session_1", "results": [
				{"url": "https://go.dev/doc/devel/release", "title": "Release History", "publish_date": "2026-08-12",
				 "excerpts": ["go1.27 (released 2026-08-12)", "Go 1.27 is a major release of Go."]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	catalog, err := toolset.NewCatalog(parallel.Factory(parallel.WithLookupEnv(func(k string) (string, bool) {
		return parallelCanary, k == "PARALLEL_API_KEY"
	})))
	if err != nil {
		t.Fatal(err)
	}
	regs, problems, err := toolset.Build(t.Context(), catalog, []toolset.Spec{{
		Name: "parallel", Type: "parallel", Settings: json.RawMessage(`{"base_url": "` + upstream.URL + `"}`),
	}})
	if err != nil || len(problems) != 0 {
		t.Fatalf("Build: %v %v", err, problems)
	}
	srv, err := server.New(regs, server.WithBasePath("/mcp"))
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(srv)
	t.Cleanup(front.Close)
	cs := connectMCP(t, front.URL+"/mcp/parallel", nil)
	t.Cleanup(func() { _ = cs.Close() })

	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
		if tl.InputSchema == nil || tl.OutputSchema == nil || tl.Description == "" {
			t.Errorf("%s: missing description or schema", tl.Name)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"parallel_extract", "parallel_search"}) {
		t.Errorf("tools = %v, want parallel_extract and parallel_search", names)
	}

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "parallel_search", Arguments: map[string]any{"query": "latest Go release"}})
	if err != nil || res.IsError {
		t.Fatalf("CallTool: %v %+v", err, res)
	}
	got, _ := json.Marshal(res.StructuredContent)
	want := `{"results":[{"content":"go1.27 (released 2026-08-12)\n…\nGo 1.27 is a major release of Go.","published_date":"2026-08-12","title":"Release History","url":"https://go.dev/doc/devel/release"}]}`
	if string(got) != want {
		t.Errorf("structured result = %s, want %s", got, want)
	}

	res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "parallel_search", Arguments: map[string]any{"query": "echo"}})
	if err != nil || !res.IsError {
		t.Fatalf("echoing upstream: %v %+v, want a tool error result", err, res)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), parallelCanary) || !strings.Contains(string(raw), "[redacted]") {
		t.Errorf("MCP error result %s: want the echoed key redacted", raw)
	}
	if len(keyHeaders) != 2 || keyHeaders[0] != parallelCanary || keyHeaders[1] != parallelCanary {
		t.Errorf("x-api-key headers = %q, want the key once per request", keyHeaders)
	}
}

// TestE2EServeParallelWithoutKey: the real binary with an optional parallel
// toolset and no PARALLEL_API_KEY serves the rest, and /readyz shows
// mcp-server degraded with the reason naming the variable.
func TestE2EServeParallelWithoutKey(t *testing.T) {
	t.Parallel()
	p := startServe(t, `{"mcp": {"toolsets": [
		{"name": "demo", "type": "demo"},
		{"name": "parallel", "type": "parallel", "optional": true}
	]}}`, 10*time.Second)
	var ready struct {
		Status     string
		Components []struct{ Name, Status, Reason string }
	}
	code := p.getJSON(t, "/readyz", &ready)
	if code != http.StatusOK || len(ready.Components) != 1 || ready.Components[0].Status != "degraded" ||
		!strings.Contains(ready.Components[0].Reason, `"parallel"`) || !strings.Contains(ready.Components[0].Reason, "PARALLEL_API_KEY") {
		t.Errorf("/readyz = %d %+v, want mcp-server degraded by toolset \"parallel\", naming PARALLEL_API_KEY", code, ready)
	}
	var listing struct{ Toolsets []struct{ Name string } }
	if code := p.getJSON(t, "/mcp/", &listing); code != http.StatusOK || len(listing.Toolsets) != 1 || listing.Toolsets[0].Name != "demo" {
		t.Errorf("GET /mcp/ = %d %+v, want only demo", code, listing)
	}
}
