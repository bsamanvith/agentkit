//go:build live

package main

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
)

// TestLiveServeSearch runs agentkit serve with deploy/local/agent/search.json
// against Claude and the real Tavily, keyless (TAVILY_API_KEY is not passed
// to the server): a question about the present, asked of the "search" agent,
// gets a tavily_search call and an answer citing a URL.
func TestLiveServeSearch(t *testing.T) {
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
	a := agentClient{t: t, url: p.url, token: e2eToken, agent: "search"}

	events := a.stream("What is the latest stable release of the Go programming language? Search the web, and cite the source URL.", "", nil)
	calls, results := toolEvents(events)
	end := events[len(events)-1]
	t.Logf("run: %v; answer %q; usage %+v", eventTypes(events), end.Answer, end.Usage)
	for i, c := range calls {
		t.Logf("call %d: %s %s", i, c.Name, c.Input)
		if i < len(results) {
			t.Logf("result %d: %.600s", i, results[i].Content)
		}
	}
	// The agent may load its skill first; it must then search, and the
	// search must succeed.
	i := slices.IndexFunc(calls, func(c agentapi.Event) bool { return c.Name == "tavily_search" })
	if i < 0 {
		t.Fatalf("calls %+v: want a tavily_search", calls)
	}
	j := slices.IndexFunc(results, func(r agentapi.Event) bool { return r.ToolUseID == calls[i].ToolUseID })
	if j < 0 || results[j].IsError {
		t.Fatalf("results %+v: want tavily_search's to succeed", results)
	}
	if end.Type != agentapi.TypeRunDone || !strings.Contains(end.Answer, "http") {
		t.Fatalf("end %+v: want a finished run whose answer cites a URL", end)
	}
	p.mu.Lock()
	logs := p.logs.String()
	p.mu.Unlock()
	if !strings.Contains(logs, `"mode":"keyless"`) {
		t.Errorf("the server did not log keyless auth")
	}
}
