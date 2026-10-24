//go:build live

package main

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agenttools"
)

// TestLiveAgentsOverMCP stands in for Claude Code, which CI does not have:
// a Go MCP client of /mcp/agentkit on agentkit serve with
// deploy/local/agent/search.json, against Claude and the real Tavily,
// keyless. agent_run on the search agent answers, with a tavily_search call
// in its tool-call summary.
func TestLiveAgentsOverMCP(t *testing.T) {
	t.Parallel()
	key, token := os.Getenv("ANTHROPIC_API_KEY"), os.Getenv("ANTHROPIC_AUTH_TOKEN")
	if key == "" && token == "" {
		t.Skip("set ANTHROPIC_API_KEY (the server runs with an empty HOME, so an `ant` profile is not seen)")
	}
	config := searchConfig(t)
	env := []string{
		envComponents + "=agent,mcp-server", envAPIToken + "=" + e2eToken,
		"ANTHROPIC_API_KEY=" + key, "ANTHROPIC_AUTH_TOKEN=" + token,
	}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "SSL_CERT_FILE"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	p := startServe(t, config, 20*time.Second, env...)
	cs := connectMCP(t, p.url+"/mcp/agentkit", &http.Client{Transport: bearer{e2eToken, http.DefaultTransport}})

	var run agenttools.Run
	mustCallAgentkit(t, cs, "agent_run", map[string]any{
		"agent":   "search",
		"message": "What is the latest stable release of the Go programming language? Search the web, and cite the source URL.",
	}, &run)
	t.Logf("run: %+v", run)
	searched := false
	for _, c := range run.ToolCalls {
		searched = searched || (c.Name == "tavily_search" && c.OK)
	}
	if run.Status != "done" || !strings.Contains(run.Answer, "http") || !searched {
		t.Fatalf("agent_run = %+v: want a finished run, an answer citing a URL and a successful tavily_search call", run)
	}
}
