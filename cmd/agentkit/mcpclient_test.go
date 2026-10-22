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
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
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

// mcp-tools prints the pins that, pasted into a toolset's settings, allow
// exactly the server's current tools.
func TestMCPToolsCommand(t *testing.T) {
	t.Parallel()
	ts := upstreams(t)
	var out, errOut strings.Builder
	if code := (cli{stdout: &out, stderr: &errOut}).main(t.Context(), []string{"mcp-tools", "-header", "X-Trace: 1", ts.URL + "/mcp/a"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	_, block, ok := strings.Cut(out.String(), "settings:\n")
	if !ok || !strings.Contains(out.String(), "search") || !strings.Contains(out.String(), "Search a.") {
		t.Fatalf("output:\n%s", out.String())
	}
	var got struct {
		Pins map[string]string `json:"pins"`
	}
	if err := json.Unmarshal([]byte(block), &got); err != nil || len(got.Pins) != 1 {
		t.Fatalf("pins block %q: %v", block, err)
	}
	pins, _ := json.Marshal(got.Pins)
	tools, err := client.Factory().Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(`{"url": "` + ts.URL + `/mcp/a", "pins": ` + string(pins) + `}`)})
	if err != nil || len(tools) != 1 {
		t.Fatalf("building with the printed pins: %d tools, %v", len(tools), err)
	}
	_ = tools[0].(interface{ Close() error }).Close()

	for _, args := range [][]string{{"mcp-tools"}, {"mcp-tools", "-header", "nocolon", "x"}, {"mcp-tools", "http://127.0.0.1:1/mcp"}} {
		if code := (cli{stdout: &out, stderr: &errOut}).main(t.Context(), args); code == 0 {
			t.Errorf("%v: exit 0", args)
		}
	}
}

// TestE2EToolsetsAddedAtRunTime: an admin adds an MCP server to the serving
// binary with agentkit toolsets, reviews and approves its tool, and the
// agent that names it lists it; disabling and deleting take it away.
func TestE2EToolsetsAddedAtRunTime(t *testing.T) {
	t.Parallel()
	up := upstreams(t)
	p := startServe(t, `{
		"mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
		"runtime_toolsets": {"allow_hosts": ["127.0.0.1"]},
		"agents": [{"name": "ops", "toolsets": ["demo"], "runtime_toolsets": ["linear"]}]
	}`, 10*time.Second, envComponents+"=agent", envAPIToken+"="+e2eToken, "ANTHROPIC_API_KEY=e2e-dummy-key")
	cmd := func(want int, args ...string) string {
		t.Helper()
		var stdout, stderr strings.Builder
		env := map[string]string{envServer: p.url, envAPIToken: e2eToken}
		c := cli{stdout: &stdout, stderr: &stderr, getenv: func(k string) string { return env[k] }}
		if code := c.main(t.Context(), append([]string{"toolsets"}, args...)); code != want {
			t.Fatalf("agentkit toolsets %q = %d, want %d; stderr: %s", args, code, want, stderr.String())
		}
		return stdout.String() + stderr.String()
	}
	agentTools := func() []string {
		t.Helper()
		var ag wire.Agent
		cl, err := (cli{getenv: func(k string) string { return map[string]string{envAPIToken: e2eToken}[k] }}).client(clientFlags{server: p.url})
		if err != nil {
			t.Fatal(err)
		}
		if ag, err = cl.Agent(t.Context(), "ops"); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, ts := range ag.Toolsets {
			names = append(names, ts.Tools...)
		}
		return names
	}

	if out := cmd(0, "add", "-name", "linear", up.URL+"/mcp/a"); !strings.Contains(out, "review_needed") || !regexp.MustCompile(`search\s+new`).MatchString(out) {
		t.Errorf("add printed\n%s\nwant search, new, to review", out)
	}
	if out := cmd(exitClient, "add", "-name", "linear", up.URL+"/mcp/a"); !strings.Contains(out, "toolset_exists") {
		t.Errorf("adding it again printed %s", out)
	}
	if names := agentTools(); slices.Contains(names, "linear__search") {
		t.Errorf("before approval, the agent's tools are %v", names)
	}
	if out := cmd(0, "approve", "linear"); !regexp.MustCompile(`search\s+approved`).MatchString(out) {
		t.Errorf("approve printed\n%s", out)
	}
	if out := cmd(0, "list"); !regexp.MustCompile(`linear\s+mcp\s+ready\s+1\s+added`).MatchString(out) {
		t.Errorf("list printed\n%s", out)
	}
	if names := agentTools(); !slices.Contains(names, "linear__search") {
		t.Errorf("after approval, the agent's tools are %v, want linear__search", names)
	}
	if out := cmd(exitClient, "approve", "linear", "nope"); !strings.Contains(out, `no tool "nope"`) {
		t.Errorf("approving an unknown tool printed %s", out)
	}
	if out := cmd(0, "disable", "linear"); !strings.Contains(out, "disabled") {
		t.Errorf("disable printed %s", out)
	}
	if out := cmd(0, "delete", "linear"); !strings.Contains(out, "deleted linear") {
		t.Errorf("delete printed %s", out)
	}
	if out := cmd(exitClient, "review", "linear"); !strings.Contains(out, "not_found") {
		t.Errorf("reviewing a deleted toolset printed %s", out)
	}
	if out := cmd(exitClient, "add", "up"); !strings.Contains(out, "usage") {
		t.Errorf("add without -name printed %s", out)
	}
}
