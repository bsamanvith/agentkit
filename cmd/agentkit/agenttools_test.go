package main

// agentkit serve with the agentkit toolset (T063): the agents over MCP, in
// process and across two servers, with the API keys of T040.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/agenttools"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// The keys of agentsOverMCPConfig: t1 and t2 may use everything; narrow
// (t1) only the toolsets agentkit and demo, so not the agent "wide".
const (
	mcpKeyT1     = "t1-key-0123456789abcdefgh"
	mcpKeyT2     = "t2-key-0123456789abcdefgh"
	mcpKeyNarrow = "narrow-key-0123456789abcd"
)

// agentsOverMCPConfig serves two agents and, with mcp-server, the agentkit
// toolset; agentkitSettings are its settings.
func agentsOverMCPConfig(agentkitSettings string) string {
	return `{
  "mcp": {"toolsets": [
    {"name": "demo", "type": "demo"},
    {"name": "secret", "type": "demo"},
    {"name": "agentkit", "type": "agentkit", "settings": ` + agentkitSettings + `}
  ]},
  "agents": [
    {"name": "helper", "description": "helps", "model": {"id": "helper-model"}, "toolsets": ["demo"]},
    {"name": "wide", "model": {"id": "wide-model"}, "toolsets": ["demo", "secret"], "prefix_tool_names": true}
  ],
  "auth": {"api_keys": [
    {"id": "t1", "hash": "` + auth.HashKey(mcpKeyT1) + `", "tenant": "t1", "scopes": ["*"], "toolsets": ["*"]},
    {"id": "t2", "hash": "` + auth.HashKey(mcpKeyT2) + `", "tenant": "t2", "scopes": ["*"], "toolsets": ["*"]},
    {"id": "narrow", "hash": "` + auth.HashKey(mcpKeyNarrow) + `", "tenant": "t1", "scopes": ["*"], "toolsets": ["agentkit", "demo"]}
  ]}
}`
}

// serveInProcess runs agentkit serve in this process (cli.serve) with the
// config, the environment and fake models by model ID, and returns its base
// URL once it listens. It stops at the end of the test.
func serveInProcess(t *testing.T, config string, env map[string]string, models map[string]llm.Model) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	c := cli{
		stdout: io.Discard, stderr: pw,
		getenv: func(k string) string {
			if k == envAddr && env[k] == "" {
				return "127.0.0.1:0"
			}
			return env[k]
		},
		newModel: func(f modelFlags) (llm.Model, error) {
			if m, ok := models[f.model]; ok {
				return m, nil
			}
			return nil, fmt.Errorf("no fake model %q", f.model)
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		err := c.serve(ctx, []string{"-config", path})
		_ = pw.CloseWithError(fmt.Errorf("serve returned: %w", err))
		served <- err
	}()
	listening := make(chan string, 1)
	var logs strings.Builder
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		sc := bufio.NewScanner(pr)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			logs.Write(sc.Bytes())
			logs.WriteByte('\n')
			var rec struct{ Msg, Addr string }
			if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Msg == "listening" {
				listening <- rec.Addr
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(30 * time.Second):
			t.Error("serve did not stop")
		}
		<-scanned
		if t.Failed() {
			t.Logf("serve log:\n%s", logs.String())
		}
	})
	select {
	case addr := <-listening:
		return "http://" + addr
	case err := <-served:
		served <- err
		t.Fatalf("serve returned before listening: %v", err)
	case <-time.After(time.Minute):
		t.Fatal("serve did not listen within a minute")
	}
	return ""
}

// agentkitSession is an MCP SDK client session of /mcp/agentkit at base,
// sending key.
func agentkitSession(t *testing.T, base, key string) (*mcp.ClientSession, error) {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "agentkit-test", Version: "0"}, nil)
	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: base + "/mcp/agentkit", HTTPClient: &http.Client{Transport: bearer{key, http.DefaultTransport}}, DisableStandaloneSSE: true,
	}, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

// callAgentkit calls a tool and decodes its structured result into out; it
// returns the text of an error result.
func callAgentkit(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) string {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		return b.String()
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: %v in %s", name, err, raw)
		}
	}
	return ""
}

func mustCallAgentkit(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	if e := callAgentkit(t, cs, name, args, out); e != "" {
		t.Fatalf("%s(%v): %s", name, args, e)
	}
}

// TestServeAgentsOverMCP: serve with agent and mcp-server serves the agents
// at /mcp/agentkit, in process: through the MCP SDK client a key lists the
// agents, runs one and continues its conversation; the endpoint needs
// credentials; another tenant sees none of it; a key that does not allow
// all of an agent's toolsets does not see that agent. Then the same through
// a second server that runs only mcp-server and names the first one's URL,
// as in the split deployment.
func TestServeAgentsOverMCP(t *testing.T) {
	t.Parallel()
	helper := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "echo", Input: json.RawMessage(`{"text":"hi"}`)}),
		llmtest.Text("first"),
		llmtest.Text("second"),
		llmtest.Text("over the split"),
	)
	models := map[string]llm.Model{"helper-model": helper, "wide-model": llmtest.NewFake(t)}
	base := serveInProcess(t, agentsOverMCPConfig(`{}`), map[string]string{envComponents: "agent,mcp-server"}, models)

	if _, err := agentkitSession(t, base, ""); err == nil {
		t.Error("/mcp/agentkit with no credentials: connected")
	}
	t1, err := agentkitSession(t, base, mcpKeyT1)
	if err != nil {
		t.Fatal(err)
	}
	var agents agenttools.AgentsOutput
	mustCallAgentkit(t, t1, "agents_list", nil, &agents)
	if len(agents.Agents) != 2 || agents.Agents[0].Name != "helper" || !slices.Equal(agents.Agents[1].Toolsets, []string{"demo", "secret"}) {
		t.Errorf("agents_list = %+v", agents.Agents)
	}
	var run, next agenttools.Run
	mustCallAgentkit(t, t1, "agent_run", map[string]any{"agent": "helper", "message": "hello"}, &run)
	if run.Status != "done" || run.Answer != "first" || run.RunID == "" || run.ConversationID == "" || len(run.ToolCalls) != 1 || !run.ToolCalls[0].OK {
		t.Fatalf("agent_run = %+v", run)
	}
	mustCallAgentkit(t, t1, "agent_run", map[string]any{"message": "more", "conversation_id": run.ConversationID}, &next)
	if next.Answer != "second" || next.ConversationID != run.ConversationID {
		t.Fatalf("second agent_run = %+v", next)
	}
	if got := helper.Requests(); len(got) != 3 || len(got[2].Messages) != 5 {
		t.Errorf("the continued run's model call has %d messages: want the history and the new message (5)", len(got[len(got)-1].Messages))
	}

	t2, err := agentkitSession(t, base, mcpKeyT2)
	if err != nil {
		t.Fatal(err)
	}
	var convs agenttools.ConversationsOutput
	mustCallAgentkit(t, t2, "conversations_list", nil, &convs)
	if len(convs.Conversations) != 0 {
		t.Errorf("tenant t2 lists %+v", convs.Conversations)
	}
	if e := callAgentkit(t, t2, "conversation_get", map[string]any{"conversation_id": run.ConversationID}, nil); !strings.Contains(e, "not_found") {
		t.Errorf("t1's conversation as t2: %q", e)
	}
	narrow, err := agentkitSession(t, base, mcpKeyNarrow)
	if err != nil {
		t.Fatal(err)
	}
	mustCallAgentkit(t, narrow, "agents_list", nil, &agents)
	if len(agents.Agents) != 1 || agents.Agents[0].Name != "helper" {
		t.Errorf("agents of the narrow key = %+v: want helper only", agents.Agents)
	}
	mustCallAgentkit(t, narrow, "conversations_list", nil, &convs)
	if len(convs.Conversations) != 1 {
		t.Errorf("the narrow key (tenant t1) lists %+v: want t1's conversation", convs.Conversations)
	}

	// The split: an MCP server alone, whose agentkit toolset calls the
	// first server's API with each caller's key.
	front := serveInProcess(t, agentsOverMCPConfig(`{"url": "`+base+`", "timeout": "1m"}`), map[string]string{envComponents: "mcp-server"}, nil)
	if _, err := agentkitSession(t, front, ""); err == nil {
		t.Error("the front's /mcp/agentkit with no credentials: connected")
	}
	ft1, err := agentkitSession(t, front, mcpKeyT1)
	if err != nil {
		t.Fatal(err)
	}
	var split agenttools.Run
	mustCallAgentkit(t, ft1, "agent_run", map[string]any{"message": "again", "conversation_id": run.ConversationID}, &split)
	if split.Answer != "over the split" || split.ConversationID != run.ConversationID {
		t.Errorf("agent_run through the front = %+v", split)
	}
	ft2, err := agentkitSession(t, front, mcpKeyT2)
	if err != nil {
		t.Fatal(err)
	}
	if e := callAgentkit(t, ft2, "run_get", map[string]any{"run_id": split.RunID}, nil); !strings.Contains(e, "not_found") {
		t.Errorf("t1's run through the front as t2: %q", e)
	}
}

// TestServeAgentkitToolsetMistakes: an agent naming the agentkit toolset
// fails startup, saying agents cannot use it; so do an agentkit toolset with
// no URL when the agent API does not run in the process, and a name used
// twice.
func TestServeAgentkitToolsetMistakes(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		components, config, says string
	}{
		"an agent names it": {
			components: "agent,mcp-server",
			config:     `{"mcp":{"toolsets":[{"name":"demo","type":"demo"},{"name":"ak","type":"agentkit"}]},"agents":[{"name":"a","toolsets":["demo","ak"]}]}`,
			says:       `agents.a.toolsets names "ak", of type agentkit: that toolset serves the agents to MCP clients, and agents cannot use it`,
		},
		"an agent names it, legacy form": {
			components: "agent",
			config:     `{"mcp":{"toolsets":[{"name":"ak","type":"agentkit"}]},"agent":{"toolsets":["ak"]}}`,
			says:       `agent.toolsets names "ak", of type agentkit`,
		},
		"the runner": {
			components: "runner",
			config:     `{"mcp":{"toolsets":[{"name":"ak","type":"agentkit"}]},"agent":{"toolsets":["ak"]}}`,
			says:       `of type agentkit`,
		},
		"no agent API here, no url": {
			components: "mcp-server",
			config:     `{"mcp":{"toolsets":[{"name":"ak","type":"agentkit"}]}}`,
			says:       "url is required",
		},
		"a name twice": {
			components: "agent,mcp-server",
			config:     `{"mcp":{"toolsets":[{"name":"ak","type":"demo"},{"name":"ak","type":"agentkit"}]}}`,
			says:       "duplicate toolset name",
		},
		"bad settings": {
			components: "agent,mcp-server",
			config:     `{"mcp":{"toolsets":[{"name":"ak","type":"agentkit","settings":{"timeout":"forever"}}]}}`,
			says:       "forever",
		},
	} {
		path := filepath.Join(t.TempDir(), "agentkit.json")
		if err := os.WriteFile(path, []byte(tc.config), 0o600); err != nil {
			t.Fatal(err)
		}
		env := map[string]string{envComponents: tc.components, envAddr: "127.0.0.1:0", envRPCToken: "rpc-token-0123456789"}
		c := cli{
			stdout: io.Discard, stderr: io.Discard, getenv: func(k string) string { return env[k] },
			newModel: func(modelFlags) (llm.Model, error) { return llmtest.NewFake(t), nil },
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		err := c.serve(ctx, []string{"-config", path})
		cancel()
		if err == nil || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: serve = %v, want an error saying %q", name, err, tc.says)
		}
	}
}

// TestServeAgentkitOptional: an optional agentkit toolset that cannot be
// built is skipped and named in /readyz; the other toolsets are served.
func TestServeAgentkitOptional(t *testing.T) {
	t.Parallel()
	base := serveInProcess(t, `{"mcp":{"toolsets":[{"name":"demo","type":"demo"},{"name":"ak","type":"agentkit","optional":true}]}}`,
		map[string]string{envComponents: "mcp-server"}, nil)
	resp, err := http.Get(base + "/readyz") //nolint:noctx // test against a local server
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `skipped toolset \"ak\"`) || !strings.Contains(string(b), "url is required") {
		t.Errorf("/readyz = %d %s: want the agentkit toolset skipped, saying why", resp.StatusCode, b)
	}
}
