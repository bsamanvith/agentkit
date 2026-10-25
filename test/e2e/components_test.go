//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

// component is one process of the split deployment, as an image runs it: the
// components its /readyz must list, the paths it must serve, and the paths of
// the other images it must not.
type component struct {
	who, url   string
	components []string
	serves     []probe
	notServes  []probe
}

// probe is one request; ok reports whether the answer is the surface it
// names. A probe in notServes must get 404 instead.
type probe struct {
	method, path, body string
	header             map[string]string
	ok                 func(body string) bool
}

// TestComponents is the per-image smoke (T102): each component of the split
// deployment (agentkit-api, agentkit-runner, agentkit-toolsets) reports ready
// on /readyz for its own components only, serves its own surface, and serves
// none of the others'. make smoke runs it against the processes, built with
// each image's tags; scripts/smoke.sh --images (CI's image job) against the
// images. It needs AGENTKIT_SMOKE_RUNNER_URL, AGENTKIT_SMOKE_TOOLSETS_URL and
// AGENTKIT_RPC_TOKEN, which a deployment whose runner and toolsets are not
// reachable from outside (make k8s-smoke) does not set: it is skipped there,
// and fails instead under AGENTKIT_SMOKE_STRICT=1 (scripts/smoke.sh).
func TestComponents(t *testing.T) {
	tg := newTarget(t)
	runnerURL := strings.TrimRight(os.Getenv("AGENTKIT_SMOKE_RUNNER_URL"), "/")
	toolsetsURL := strings.TrimRight(os.Getenv("AGENTKIT_SMOKE_TOOLSETS_URL"), "/")
	rpcToken := os.Getenv("AGENTKIT_RPC_TOKEN")
	if runnerURL == "" || toolsetsURL == "" || rpcToken == "" {
		skipOrFail(t, "set AGENTKIT_SMOKE_RUNNER_URL, AGENTKIT_SMOKE_TOOLSETS_URL and AGENTKIT_RPC_TOKEN to check each component (make smoke does)")
	}
	api := map[string]string{"Authorization": "Bearer " + tg.token}
	rpc := map[string]string{"Authorization": "Bearer " + rpcToken, "Content-Type": "application/json"}
	mcp := map[string]string{
		"Authorization": "Bearer " + tg.token, "Content-Type": "application/json",
		"Accept": "application/json, text/event-stream",
	}
	contains := func(s string) func(string) bool {
		return func(body string) bool { return strings.Contains(body, s) }
	}
	var (
		agents   = probe{method: http.MethodGet, path: "/v1/agents", header: api, ok: contains(`"default"`)}
		console  = probe{method: http.MethodGet, path: "/", ok: contains(`<div id="root"`)}
		runner   = probe{method: http.MethodPost, path: "/rpc/runner/Agents", body: "{}", header: rpc, ok: contains(`"default"`)}
		toolsets = probe{method: http.MethodPost, path: "/rpc/toolsets/Toolsets", body: "{}", header: rpc, ok: contains(`"demo"`)}
		// The memory of agents on the runner (the notes agent's), which the agent API keeps.
		memory  = probe{method: http.MethodPost, path: "/rpc/memory/Toolsets", body: "{}", header: rpc, ok: contains(`"notes"`)}
		mcpDemo = probe{
			method: http.MethodPost, path: "/mcp/demo", header: mcp, ok: contains(`"serverInfo"`),
			body: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}`,
		}
	)
	for _, c := range []component{
		{"api", tg.url, []string{"agent", "web", "memory"}, []probe{agents, console, memory}, []probe{runner, toolsets, mcpDemo}},
		{"runner", runnerURL, []string{"runner"}, []probe{runner}, []probe{agents, console, toolsets, mcpDemo, memory}},
		{"toolsets", toolsetsURL, []string{"toolsets", "mcp-server"}, []probe{toolsets, mcpDemo}, []probe{agents, console, runner, memory}},
	} {
		t.Run(c.who, func(t *testing.T) {
			var ready struct {
				Status     string `json:"status"`
				Components []struct{ Name, Status, Reason string }
			}
			code, body := get(t, tg.client, c.url, probe{method: http.MethodGet, path: "/readyz"})
			if err := json.Unmarshal([]byte(body), &ready); err != nil {
				t.Fatalf("%s: GET /readyz: %d: %v in %s", c.who, code, err, body)
			}
			var names []string
			for _, r := range ready.Components {
				names = append(names, r.Name)
				if r.Status != "ready" {
					t.Errorf("%s: /readyz: %s is %s: %s", c.who, r.Name, r.Status, r.Reason)
				}
			}
			if code != http.StatusOK || ready.Status != "ready" || !slices.Equal(names, c.components) {
				t.Errorf("%s: GET /readyz: %d, %q with %v, want ready with exactly %v", c.who, code, ready.Status, names, c.components)
			}
			for _, p := range c.serves {
				if code, body := get(t, tg.client, c.url, p); code != http.StatusOK || !p.ok(body) {
					t.Errorf("%s: %s %s: %d, want its surface: %.300s", c.who, p.method, p.path, code, body)
				}
			}
			for _, p := range c.notServes {
				if code, body := get(t, tg.client, c.url, p); code != http.StatusNotFound {
					t.Errorf("%s: %s %s: %d, want 404 (another image's surface): %.300s", c.who, p.method, p.path, code, body)
				}
			}
		})
	}
}

// get sends p to base and returns the status and body.
func get(t *testing.T, client *http.Client, base string, p probe) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), p.method, base+p.path, strings.NewReader(p.body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range p.header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s%s: %v (is it up?)", p.method, base, p.path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, string(b)
}
