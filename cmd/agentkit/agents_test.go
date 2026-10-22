//go:build !noagent

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// serveAgents builds the agent component from config as serve does, with
// the model of each agent a fake chosen by its model ID, and serves it on an
// httptest server. It returns a client of it.
func serveAgents(t *testing.T, config string, models map[string]*llmtest.Fake) *agentclient.Client {
	t.Helper()
	_, client := serveAgentsAt(t, config, models)
	return client
}

// serveAgentsAt is serveAgents that also returns the server's URL.
func serveAgentsAt(t *testing.T, config string, models map[string]*llmtest.Fake) (string, *agentclient.Client) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	c := cli{
		getenv: func(string) string { return "" },
		newModel: func(f modelFlags) (llm.Model, error) {
			if m, ok := models[f.model]; ok {
				return m, nil
			}
			return nil, fmt.Errorf("no fake model %q", f.model)
		},
	}
	env := serveEnv{components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second}
	log := slog.New(slog.DiscardHandler)
	built, problems, err := c.buildToolsets(t.Context(), env, cfg, path, log)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := c.newAgents(t.Context(), cfg, built, problems, env, nil, scoped.Memory(nil), nil, clock.Real(), log)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(m.Component.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Component.Shutdown(ctx)
	})
	client, err := agentclient.New(ts.URL, agentclient.WithHTTPClient(ts.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return ts.URL, client
}

func toolNames(req llm.Request) []string {
	var names []string
	for _, d := range req.Tools {
		names = append(names, d.Name)
	}
	slices.Sort(names)
	return names
}

// agentTools are the tool names GET /v1/agents gives an agent.
func agentTools(a wire.Agent) []string {
	var names []string
	for _, ts := range a.Toolsets {
		names = append(names, ts.Tools...)
	}
	slices.Sort(names)
	return names
}

// TestServeTwoAgents: an "agents" config runs each agent with its own
// model, system prompt and tools (the fakes record what each was given,
// which is what GET /v1/agents says), a conversation stays with its agent,
// and with no agent named "default" a run must name one.
func TestServeTwoAgents(t *testing.T) {
	t.Parallel()
	const config = `{
	  "mcp": {"toolsets": [
	    {"name": "demo-a", "type": "demo", "settings": {"echo_prefix": "[a] "}},
	    {"name": "demo-b", "type": "demo", "settings": {"echo_prefix": "[b] "}, "exclude": ["current_time"]}
	  ]},
	  "agent_api": {"max_concurrent_runs": 4},
	  "agents": [
	    {"name": "plain", "description": "Plain.", "model": {"id": "model-plain"}, "system": "You are plain.", "toolsets": ["demo-a"]},
	    {"name": "search", "model": {"id": "model-search", "effort": "low"}, "system": "You search.", "toolsets": ["demo-a", "demo-b"], "prefix_tool_names": true}
	  ]
	}`
	plain := llmtest.NewFake(t, llmtest.Text("plain 1"), llmtest.Text("plain 2"))
	search := llmtest.NewFake(t, llmtest.Text("search 1"))
	c := serveAgents(t, config, map[string]*llmtest.Fake{"model-plain": plain, "model-search": search})
	ctx := t.Context()

	first, err := c.Run(ctx, agentclient.RunRequest{Message: "hi", Agent: "plain"})
	if err != nil || first.Agent != "plain" || first.Answer != "plain 1" {
		t.Fatalf("run with plain = %+v, %v", first, err)
	}
	if run, err := c.Run(ctx, agentclient.RunRequest{Message: "hi", Agent: "search"}); err != nil || run.Agent != "search" || run.Answer != "search 1" {
		t.Fatalf("run with search = %+v, %v", run, err)
	}
	if run, err := c.Run(ctx, agentclient.RunRequest{Message: "more", ConversationID: first.ConversationID}); err != nil || run.Answer != "plain 2" {
		t.Errorf("follow-up without an agent = %+v, %v; want plain's", run, err)
	}
	_, err = c.Run(ctx, agentclient.RunRequest{Message: "more", ConversationID: first.ConversationID, Agent: "search"})
	if !errors.Is(err, agentclient.ErrAgentMismatch) {
		t.Errorf("continuing plain's conversation with search: %v, want agent_mismatch", err)
	}
	if _, err := c.Run(ctx, agentclient.RunRequest{Message: "hi"}); !errors.Is(err, agentclient.ErrInvalidRequest) {
		t.Errorf("a run naming no agent, with none named default: %v, want invalid_request", err)
	}

	agents, err := c.Agents(ctx)
	if err != nil || len(agents) != 2 || agents[0].Name != "plain" || agents[1].Name != "search" {
		t.Fatalf("Agents = %+v, %v; want plain and search", agents, err)
	}
	fakes := map[string]*llmtest.Fake{"plain": plain, "search": search}
	for _, a := range agents {
		full, err := c.Agent(ctx, a.Name)
		if err != nil {
			t.Fatal(err)
		}
		for i, req := range fakes[a.Name].Requests() {
			if req.System != full.System || !slices.Equal(toolNames(req), agentTools(a)) {
				t.Errorf("%s's model, request %d: system %q and tools %v; GET /v1/agents/%s says %q and %v",
					a.Name, i, req.System, toolNames(req), a.Name, full.System, agentTools(a))
			}
		}
		if a.Default || a.Model.ID != "model-"+a.Name {
			t.Errorf("agent %+v", a)
		}
	}
	if got := agentTools(agents[1]); len(got) != 5 || !strings.HasPrefix(got[0], "demo-a__") || slices.Contains(got, "demo-b__current_time") {
		t.Errorf("search's tools %v, want demo-a's three and demo-b's two, prefixed", got)
	}
	if agents[0].Description != "Plain." || agents[1].Model.Effort != "low" {
		t.Errorf("agents %+v", agents)
	}
	sets, err := c.Toolsets(ctx)
	if err != nil || len(sets) != 2 || sets[0].Name != "demo-a" || sets[1].Name != "demo-b" || sets[1].ToolCount != 2 ||
		sets[0].MCPPath != "" || sets[0].Credential.Status != wire.CredentialNotNeeded {
		t.Errorf("Toolsets = %+v, %v; want demo-a and demo-b, ready, not served over MCP (mcp-server is off)", sets, err)
	}
	page, err := c.Conversations(ctx, agentclient.ConversationsQuery{Agent: "plain"})
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].ID != first.ConversationID {
		t.Errorf("plain's conversations = %+v, %v", page, err)
	}
}

// TestServeLegacyAgentConfig: a config with one "agent" section (the form
// before several agents) serves it as the agent "default", the one a run
// naming no agent gets.
func TestServeLegacyAgentConfig(t *testing.T) {
	t.Parallel()
	const config = `{
	  "mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
	  "agent": {"model": {"id": "m"}, "system": "Be brief.", "toolsets": ["demo"], "max_concurrent_runs": 2}
	}`
	m := llmtest.NewFake(t, llmtest.Text("one"), llmtest.Text("two"))
	c := serveAgents(t, config, map[string]*llmtest.Fake{"m": m})
	ctx := t.Context()
	if run, err := c.Run(ctx, agentclient.RunRequest{Message: "hi"}); err != nil || run.Agent != agentapi.DefaultAgent {
		t.Errorf("a run naming no agent = %+v, %v; want the default agent's", run, err)
	}
	if run, err := c.Run(ctx, agentclient.RunRequest{Message: "hi", Agent: agentapi.DefaultAgent}); err != nil || run.Agent != agentapi.DefaultAgent {
		t.Errorf("a run naming default = %+v, %v", run, err)
	}
	a, err := c.Agent(ctx, agentapi.DefaultAgent)
	if err != nil || !a.Default || a.System != "Be brief." || a.Model.ID != "m" || len(a.Toolsets) != 1 || a.Toolsets[0].Name != "demo" {
		t.Errorf("the default agent = %+v, %v", a, err)
	}
	for _, req := range m.Requests() {
		if req.System != "Be brief." || !slices.Equal(toolNames(req), agentTools(a)) {
			t.Errorf("model request: system %q, tools %v; want the agent's", req.System, toolNames(req))
		}
	}
}

// TestLoadServeConfigForms: the three forms of the agent config load to the
// same agents and limits: no section (the default agent, default limits),
// the "agent" section, and "agents" with "agent_api".
func TestLoadServeConfigForms(t *testing.T) {
	t.Parallel()
	load := func(config string) serveConfig {
		t.Helper()
		path := filepath.Join(t.TempDir(), "agentkit.json")
		if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadServeConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	none := load(`{}`)
	legacy := load(`{"agent": {"system": "S", "max_turns": 3, "max_runs": 7}}`)
	agents := load(`{"agents": [{"name": "default", "system": "S", "max_turns": 3}], "agent_api": {"max_runs": 7}}`)
	if len(none.Agents) != 1 || none.Agents[0].Name != agentapi.DefaultAgent || none.API != defaultAPIConfig() {
		t.Errorf("no section: %+v", none)
	}
	want := defaultAgentDef()
	want.Name, want.System, want.MaxTurns = agentapi.DefaultAgent, "S", 3
	api := defaultAPIConfig()
	api.MaxRuns = 7
	for name, cfg := range map[string]serveConfig{"agent": legacy, "agents": agents} {
		if len(cfg.Agents) != 1 || fmt.Sprint(cfg.Agents[0]) != fmt.Sprint(want) || cfg.API != api {
			t.Errorf("%s section: agents %+v, limits %+v; want %+v and %+v", name, cfg.Agents, cfg.API, want, api)
		}
	}
}

// TestToolsetCatalogCutsKeys: a skip reason that quotes a credential (a
// connector echoing what it was given) is listed without the value.
func TestToolsetCatalogCutsKeys(t *testing.T) {
	t.Parallel()
	const key = "canary-exa-77d1e3"
	c := cli{getenv: func(k string) string { return map[string]string{"EXA_API_KEY": key}[k] }}
	specs := []toolset.Spec{{Name: "exa", Type: "exa"}}
	problems := []toolset.Problem{{Toolset: "exa", Type: "exa", Err: fmt.Errorf("exa: key %s was rejected", key)}}
	got := c.toolsetCatalog(serveConfig{}, specs, nil, problems, serveEnv{components: []string{"agent"}})
	if len(got) != 1 || got[0].Skipped == "" || strings.Contains(got[0].Skipped, key) || got[0].Credential.Env != "EXA_API_KEY" {
		t.Errorf("toolsetCatalog = %+v; want exa skipped with a reason that does not hold the key", got)
	}
}

// TestE2EServeCatalog: the built binary, serving the agent API and the MCP
// server with keyed toolsets, lists every toolset in GET /v1/toolsets: built
// ones with their MCP URL, the optional one whose key is missing as skipped
// with the reason, each credential by its variable's name. The keys in the
// environment (canaries) appear in no response.
func TestE2EServeCatalog(t *testing.T) {
	t.Parallel()
	const config = `{
	  "mcp": {"toolsets": [
	    {"name": "demo", "type": "demo"},
	    {"name": "web", "type": "tavily", "settings": {"auth": "auto"}},
	    {"name": "exa", "type": "exa", "optional": true},
	    {"name": "parallel", "type": "parallel", "settings": {"api_key_env": "MY_PARALLEL_KEY"}, "optional": true}
	  ]},
	  "agents": [
	    {"name": "plain", "toolsets": ["demo"]},
	    {"name": "search", "toolsets": ["web", "exa", "parallel"], "system": "Search."}
	  ]
	}`
	canaries := []string{"canary-tavily-5a1e0c", "canary-exa-9b7d2f", "canary-anthropic-4c8e1a", "canary-token-0123456789abcdef"}
	p := startServe(t, config, 5*time.Second,
		envComponents+"=agent,mcp-server", envAPIToken+"="+canaries[3],
		"TAVILY_API_KEY="+canaries[0], "EXA_API_KEY="+canaries[1], "ANTHROPIC_API_KEY="+canaries[2], "ANTHROPIC_BASE_URL=http://127.0.0.1:1")
	rec := &bodyRecorder{}
	c, err := agentclient.New(p.url, agentclient.WithToken(canaries[3]), agentclient.WithHTTPClient(&http.Client{Transport: rec, Timeout: time.Minute}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	sets, err := c.Toolsets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]wire.Toolset{}
	for _, ts := range sets {
		got[ts.Name] = ts
	}
	for name, want := range map[string]struct {
		status, typ string
		cred        wire.Credential
	}{
		"demo":     {wire.ToolsetReady, "demo", wire.Credential{Status: wire.CredentialNotNeeded}},
		"web":      {wire.ToolsetReady, "tavily", wire.Credential{Status: wire.CredentialConfigured, Env: "TAVILY_API_KEY"}},
		"exa":      {wire.ToolsetReady, "exa", wire.Credential{Status: wire.CredentialConfigured, Env: "EXA_API_KEY"}},
		"parallel": {wire.ToolsetSkipped, "parallel", wire.Credential{Status: wire.CredentialMissing, Env: "MY_PARALLEL_KEY"}},
	} {
		ts := got[name]
		if ts.Status != want.status || ts.Type != want.typ || ts.Credential != want.cred || c.MCPURL(ts) != p.url+"/mcp/"+name {
			t.Errorf("toolset %s = %+v (MCP URL %q), want %s %s with credential %+v at /mcp/%s", name, ts, c.MCPURL(ts), want.status, want.typ, want.cred, name)
		}
		if (ts.Status == wire.ToolsetReady) != (ts.ToolCount > 0) {
			t.Errorf("toolset %s has %d tools", name, ts.ToolCount)
		}
	}
	if r := got["parallel"].Reason; !strings.Contains(r, "MY_PARALLEL_KEY") {
		t.Errorf("parallel skipped for %q, want the missing MY_PARALLEL_KEY", r)
	}
	if len(sets) != 4 {
		t.Errorf("%d toolsets, want 4", len(sets))
	}
	for _, name := range []string{"demo", "web", "exa", "parallel"} {
		if tools, err := c.Tools(ctx, name); err != nil || tools.Status != got[name].Status || len(tools.Tools) != got[name].ToolCount {
			t.Errorf("Tools(%s) = %+v, %v", name, tools, err)
		}
	}
	agents, err := c.Agents(ctx)
	if err != nil || len(agents) != 2 {
		t.Fatalf("Agents = %+v, %v", agents, err)
	}
	for _, a := range agents {
		if _, err := c.Agent(ctx, a.Name); err != nil {
			t.Error(err)
		}
	}
	if s := agents[1].Toolsets; len(s) != 3 || s[2].Name != "parallel" || s[2].Status != wire.ToolsetSkipped || len(s[2].Tools) != 0 {
		t.Errorf("search's toolsets %+v, want parallel skipped", s)
	}
	if _, err := c.Conversations(ctx, agentclient.ConversationsQuery{}); err != nil {
		t.Error(err)
	}
	var ready struct {
		Components []struct{ Name, Status string }
	}
	if code := p.getJSON(t, "/readyz", &ready); code != http.StatusOK || len(ready.Components) != 2 ||
		ready.Components[0].Name != agentapi.Name || ready.Components[0].Status != "degraded" {
		t.Errorf("/readyz = %d %+v, want the agent degraded (parallel skipped)", code, ready)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.bodies) < 9 {
		t.Errorf("recorded %d responses", len(rec.bodies))
	}
	for _, body := range rec.bodies {
		for _, canary := range canaries {
			if strings.Contains(string(body), canary) {
				t.Errorf("a response holds the key %s: %s", canary, body)
			}
		}
	}
}

// TestServeCompactsByTheModelsWindow: an agent's conversation is compacted
// once a request fills model.compact_at of model.context_window.
func TestServeCompactsByTheModelsWindow(t *testing.T) {
	t.Parallel()
	const config = `{
	  "mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
	  "agents": [{"name": "a", "model": {"id": "m", "context_window": 100, "compact_at": 0.5}}]
	}`
	m := llmtest.NewFake(t,
		llmtest.Text("first").WithUsage(llm.Usage{InputTokens: 50}),
		llmtest.Text("the summary"),
		llmtest.Text("second"),
	)
	c := serveAgents(t, config, map[string]*llmtest.Fake{"m": m})
	first, err := c.Run(t.Context(), agentclient.RunRequest{Message: "hi", Agent: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if run, err := c.Run(t.Context(), agentclient.RunRequest{Message: "more", ConversationID: first.ConversationID}); err != nil || run.Answer != "second" {
		t.Fatalf("second run = %+v, %v", run, err)
	}
	reqs := m.Requests()
	if len(reqs) != 3 || reqs[1].System != harness.CompactionPrompt || !strings.HasPrefix(reqs[2].Messages[0].Text(), runner.SummaryPrefix) {
		t.Errorf("requests %+v, want the second to ask for a summary and the third to start with it", reqs)
	}
}
