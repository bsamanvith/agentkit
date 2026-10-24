package main

// The HubSpot connector as the binary serves it: over MCP at /mcp/hubspot,
// in process, against a fake upstream named by the base_url setting.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/connectors/hubspot"
	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// TestHubSpotOverMCP: /mcp/hubspot lists the two read tools by default; a
// search through the SDK client sends the token from HUBSPOT_ACCESS_TOKEN
// as a bearer token and returns the mapped records.
func TestHubSpotOverMCP(t *testing.T) {
	t.Parallel()
	var auth []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		_, _ = io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/crm/v3/objects/companies/search" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total": 1, "results": [{"id": "9001", "properties": {"name": "Northwind Traders", "domain": null},
			"createdAt": "2025-11-19T09:00:00Z", "updatedAt": "2026-10-21T12:00:00Z"}]}`)
	}))
	t.Cleanup(upstream.Close)

	catalog, err := toolset.NewCatalog(hubspot.Factory(hubspot.WithLookupEnv(func(k string) (string, bool) {
		return "pat-test-1", k == credentialEnv["hubspot"]
	})))
	if err != nil {
		t.Fatal(err)
	}
	regs, problems, err := toolset.Build(t.Context(), catalog, []toolset.Spec{{
		Name: "hubspot", Type: "hubspot", Settings: json.RawMessage(`{"base_url": "` + upstream.URL + `"}`),
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
	cs := connectMCP(t, front.URL+"/mcp/hubspot", nil)
	t.Cleanup(func() { _ = cs.Close() })

	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"hubspot_get_record", "hubspot_search"}) {
		t.Errorf("tools = %v, want the read tools only", names)
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "hubspot_search", Arguments: map[string]any{"object": "companies", "query": "northwind"}})
	if err != nil || res.IsError {
		t.Fatalf("CallTool: %v %+v", err, res)
	}
	got, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(got), `"name":"Northwind Traders"`) || strings.Contains(string(got), "domain") {
		t.Errorf("structured result = %s, want the company without its null property", got)
	}
	if strings.Join(auth, ",") != "Bearer pat-test-1" {
		t.Errorf("Authorization = %q, want the environment's token as a bearer token", auth)
	}
}
