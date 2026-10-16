package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/connectors/tavily"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// tavilyCanary is the key in the fake environment; it must not reach any
// MCP response.
const tavilyCanary = "tvly-CANARY-cmd-3b8e1d"

func tavilyFixture(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "connectors", "tavily", "testdata", dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// tavilyCassette is the body of a cassette's recorded response.
func tavilyCassette(t *testing.T, name string) []byte {
	t.Helper()
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(tavilyFixture(t, "cassettes", name+".res.http"))), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSearchConfigBuilds: deploy/local/agent/search.json is a valid serve
// config, and its toolsets build from this binary's catalog (tavily
// compiled in, no key needed; exa and parallel skipped as optional without
// EXA_API_KEY and PARALLEL_API_KEY).
func TestSearchConfigBuilds(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "deploy", "local", "agent", "search.json")
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// The files toolset's root is relative to the working directory: point
	// it at a temporary folder, so the test leaves the source tree alone.
	for i, s := range cfg.MCP.Toolsets {
		if s.Name == "files" {
			cfg.MCP.Toolsets[i].Settings = json.RawMessage(fmt.Sprintf(`{"root": %q, "create_root": true}`, filepath.Join(t.TempDir(), "workspace")))
		}
	}
	env := serveEnv{components: []string{"agent", mcpServerName}}
	built, problems, err := buildToolsets(t.Context(), env, cfg, path, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// The real catalog reads the keys through os.LookupEnv: a keyed toolset
	// is built when its variable is set and skipped for the missing key when
	// it is not.
	skipped := map[string]toolset.Problem{}
	for _, p := range problems {
		skipped[p.Toolset] = p
	}
	for name, variable := range map[string]string{"exa": "EXA_API_KEY", "parallel": "PARALLEL_API_KEY"} {
		p, isSkipped := skipped[name]
		delete(skipped, name)
		if _, set := os.LookupEnv(variable); set {
			if isSkipped || built[name] == nil {
				t.Errorf("%s: problem %v, want it built with %s set", name, p.Err, variable)
			}
			continue
		}
		missingKey := strings.Contains(p.Error(), variable)
		if name == "exa" {
			missingKey = errors.Is(p.Err, exa.ErrMissingKey)
		}
		if !isSkipped || !missingKey {
			t.Errorf("%s: problem %v, want it skipped for its missing %s", name, p.Err, variable)
		}
	}
	if len(skipped) != 0 {
		t.Errorf("other toolsets skipped: %v", skipped)
	}
	defs, err := built["tavily"].List(t.Context())
	if err != nil || len(defs) != 2 || defs[0].Name != "tavily_extract" || defs[1].Name != "tavily_search" {
		t.Errorf("tavily toolset: %v %v, want tavily_extract and tavily_search", defs, err)
	}
	toolsets := map[string][]string{}
	for _, a := range cfg.Agents {
		toolsets[a.Name] = a.Toolsets
	}
	if want := map[string][]string{"default": {"demo"}, "search": {"tavily", "exa", "parallel"}, "workspace": {"files", "math"}}; !maps.EqualFunc(toolsets, want, slices.Equal) {
		t.Errorf("agents' toolsets %v, want %v", toolsets, want)
	}
}

// bodyRecorder keeps every response body an HTTP client receives.
type bodyRecorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (b *bodyRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	b.mu.Lock()
	b.bodies = append(b.bodies, body)
	b.mu.Unlock()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, err
}

// TestMCPTavily serves a tavily toolset at /mcp/tavily through the MCP
// server this binary uses, against a fake Tavily replaying recorded
// responses, and drives it with the MCP SDK client: the listing has exactly
// the two tools with their schemas, a search returns the mapped result, and
// failures are tool errors. The key reaches no MCP response, even when
// Tavily echoes it.
func TestMCPTavily(t *testing.T) {
	t.Parallel()
	var (
		mu      sync.Mutex
		replies = []struct {
			status int
			body   []byte
		}{
			{200, tavilyCassette(t, "search-go-release")},
			{400, []byte(`{"detail": {"error": "bad query from ` + tavilyCanary + `"}}`)},
		}
		auth []string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth = append(auth, r.Header.Get("Authorization"))
		if len(replies) == 0 {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.WriteHeader(replies[0].status)
		_, _ = w.Write(replies[0].body)
		replies = replies[1:]
	}))
	defer upstream.Close()

	catalog, err := toolset.NewCatalog(tavily.Factory(
		tavily.WithBaseURL(upstream.URL),
		tavily.WithLookupEnv(func(k string) (string, bool) { return tavilyCanary, k == "TAVILY_API_KEY" }),
	))
	if err != nil {
		t.Fatal(err)
	}
	regs, _, err := toolset.Build(t.Context(), catalog, []toolset.Spec{{Name: "tavily", Type: tavily.Type}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := newMCPServer(t.Context(), defaultMCPConfig(), regs, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(m.Pattern, m.Component.Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rec := &bodyRecorder{}
	cs := connectMCP(t, srv.URL+"/mcp/tavily", &http.Client{Transport: rec, Timeout: time.Minute})
	defer cs.Close()

	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
		if tl.InputSchema == nil || tl.OutputSchema == nil || !strings.Contains(tl.Description, "not instructions") {
			t.Errorf("%s: input schema %v, output schema %v, description %q", tl.Name, tl.InputSchema != nil, tl.OutputSchema != nil, tl.Description)
		}
	}
	if !slices.Equal(names, []string{"tavily_extract", "tavily_search"}) {
		t.Errorf("tools %v, want tavily_extract and tavily_search", names)
	}

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "tavily_search", Arguments: map[string]any{
		"query": "latest Go programming language release", "time_range": "month",
	}})
	if err != nil || res.IsError {
		t.Fatalf("search: %v %+v", err, res)
	}
	var got, want any
	gotJSON, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(gotJSON, &got)
	_ = json.Unmarshal(tavilyFixture(t, "golden", "search-go-release.output.json"), &want)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("search result over MCP differs from the mapped output (-want +got):\n%s", diff)
	}

	for _, args := range []map[string]any{
		{"query": "go release"},           // Tavily answers 400, echoing the key
		{"query": "go", "max_results": 0}, // refused before any request
	} {
		res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "tavily_search", Arguments: args})
		if err != nil || !res.IsError {
			t.Errorf("%v: %v %+v, want a tool error", args, err, res)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(auth, []string{"Bearer " + tavilyCanary, "Bearer " + tavilyCanary}) {
		t.Errorf("upstream saw Authorization %q, want the key on each of the two requests", auth)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, b := range rec.bodies {
		if bytes.Contains(b, []byte(tavilyCanary)) {
			t.Errorf("the key is in an MCP response: %s", b)
		}
	}
}

// TestE2EServeTavilyOptional runs agentkit serve with two tavily toolsets:
// one keyless, served; one that needs a key it does not have, optional, so
// skipped and named in /readyz while the server runs.
func TestE2EServeTavilyOptional(t *testing.T) {
	t.Parallel()
	const config = `{"mcp": {"toolsets": [
		{"name": "tavily", "type": "tavily", "settings": {"auth": "keyless"}},
		{"name": "tavily-keyed", "type": "tavily", "settings": {"auth": "key", "api_key_env": "AGENTKIT_TEST_NO_TAVILY_KEY"}, "optional": true}
	]}}`
	p := startServe(t, config, 10*time.Second)

	var ready struct {
		Status     string
		Components []struct{ Name, Status, Reason string }
	}
	code := p.getJSON(t, "/readyz", &ready)
	if code != http.StatusOK || len(ready.Components) != 1 || ready.Components[0].Status != "degraded" ||
		!strings.Contains(ready.Components[0].Reason, `"tavily-keyed"`) ||
		!strings.Contains(ready.Components[0].Reason, "AGENTKIT_TEST_NO_TAVILY_KEY") {
		t.Errorf("/readyz = %d %+v, want mcp-server degraded by tavily-keyed, naming its variable", code, ready)
	}
	cs := connectMCP(t, p.url+"/mcp/tavily", nil)
	defer cs.Close()
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(list.Tools) != 2 {
		t.Errorf("/mcp/tavily: %v %v, want its two tools", list, err)
	}
}
