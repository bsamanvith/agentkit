package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// T047 criterion "over MCP": /mcp/exa lists exactly exa_search and
// exa_extract with input and output schemas, and a call through the MCP
// client returns what the tool returns in process. The key is in no MCP
// response, even when the fake Exa echoes it.
func TestServeExaOverMCP(t *testing.T) {
	t.Parallel()
	const key = "exa-canary-key-cmd-9d8c7b6a5f4e"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/contents" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"requestId": "r", "error": "bad request for key ` + key + `", "tag": "INVALID_REQUEST"}`))
			return
		}
		_, _ = w.Write([]byte(`{"results": [{"title": "Go", "url": "https://go.dev/", "publishedDate": "2026-08-12T00:00:00.000Z",
			"highlights": ["Build simple, secure, scalable systems with Go.", "Your key is ` + key + `"]}]}`))
	}))
	t.Cleanup(fake.Close)
	catalog, err := toolset.NewCatalog(exa.Factory(
		exa.WithBaseURL(fake.URL),
		exa.WithLookupEnv(func(k string) (string, bool) { return key, k == "EXA_API_KEY" }),
	))
	if err != nil {
		t.Fatal(err)
	}
	regs, problems, err := toolset.Build(t.Context(), catalog, []toolset.Spec{{Name: "exa", Type: "exa"}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := newMCPServer(t.Context(), defaultMCPConfig(), regs, problems, nil, nil, "127.0.0.1:0", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(m.Component.Handler())
	t.Cleanup(srv.Close)
	tap := &bodyTap{base: srv.Client().Transport}
	cs := connectMCP(t, srv.URL+"/mcp/exa", &http.Client{Transport: tap})
	t.Cleanup(func() { _ = cs.Close() })

	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
		if tl.InputSchema == nil || tl.OutputSchema == nil {
			t.Errorf("%s: input schema %v, output schema %v; want both", tl.Name, tl.InputSchema, tl.OutputSchema)
		}
	}
	if want := []string{"exa_extract", "exa_search"}; !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}

	args := map[string]any{"query": "the Go language", "max_results": 2}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "exa_search", Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("exa_search over MCP: %v %+v", err, res)
	}
	in, _ := json.Marshal(args)
	direct, err := regs["exa"].Invoke(t.Context(), "exa_search", in)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	viaMCP, _ := json.Marshal(res.StructuredContent)
	if json.Unmarshal(direct, &want) != nil || json.Unmarshal(viaMCP, &got) != nil {
		t.Fatalf("outputs are not JSON: %s, %s", direct, viaMCP)
	}
	if string(mustMarshal(t, got)) != string(mustMarshal(t, want)) {
		t.Errorf("over MCP:\n%s\nin process:\n%s", viaMCP, direct)
	}
	if !strings.Contains(string(viaMCP), "[redacted]") {
		t.Errorf("output %s: want the echoed key redacted", viaMCP)
	}

	res, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "exa_extract", Arguments: map[string]any{"urls": []string{"https://go.dev/"}}})
	if err != nil || !res.IsError {
		t.Fatalf("exa_extract over MCP: %v %+v, want a tool error", err, res)
	}

	if b := tap.bodies(); strings.Contains(b, key) || !strings.Contains(b, "bad request for key") {
		t.Errorf("MCP responses contain the key, or not the 400's message: %s", b)
	}
}

// T047 criterion "settings validation": an optional exa toolset whose key
// variable is unset is skipped with the variable named, the others are
// served, and the MCP server reports itself degraded (shown in /readyz).
func TestServeExaWithoutKeyIsDegraded(t *testing.T) {
	t.Parallel()
	const unset = "AGENTKIT_TEST_EXA_KEY_NEVER_SET" // read through os.LookupEnv by the real catalog
	cfg := serveConfig{MCP: defaultMCPConfig()}
	cfg.MCP.Toolsets = []toolset.Spec{
		{Name: "demo", Type: "demo"},
		{Name: "search", Type: "exa", Optional: true, Settings: json.RawMessage(`{"api_key_env": "` + unset + `"}`)},
	}
	log := slog.New(slog.DiscardHandler)
	regs, problems, err := buildToolsets(t.Context(), serveEnv{components: []string{mcpServerName}}, cfg, "config.json", log)
	if err != nil {
		t.Fatal(err)
	}
	if _, built := regs["search"]; built || regs["demo"] == nil {
		t.Errorf("toolsets built: %v, want demo only", regs)
	}
	if len(problems) != 1 || !errors.Is(problems[0].Err, exa.ErrMissingKey) || !strings.Contains(problems[0].Error(), unset) {
		t.Fatalf("problems = %v, want the exa toolset skipped naming %s", problems, unset)
	}
	m, err := newMCPServer(t.Context(), cfg.MCP, regs, problems, nil, nil, "127.0.0.1:0", log)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Component.Ready(t.Context()); !errors.Is(err, component.ErrDegraded) || !strings.Contains(err.Error(), unset) {
		t.Errorf("Ready = %v, want degraded naming %s", err, unset)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// bodyTap keeps every response body the MCP client receives.
type bodyTap struct {
	base http.RoundTripper
	mu   sync.Mutex
	buf  bytes.Buffer
}

func (b *bodyTap) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := b.base.RoundTrip(r)
	if err != nil {
		return resp, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	b.mu.Lock()
	b.buf.Write(body)
	b.mu.Unlock()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, err
}

func (b *bodyTap) bodies() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
