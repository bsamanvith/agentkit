//go:build live

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
)

// TestLiveServeSearch runs agentkit serve with deploy/local/agent/search.json
// against Claude and the real Tavily, keyless (TAVILY_API_KEY is not passed
// to the server): a question about the present gets a tavily_search call and
// an answer citing a URL.
func TestLiveServeSearch(t *testing.T) {
	t.Parallel()
	key, token := os.Getenv("ANTHROPIC_API_KEY"), os.Getenv("ANTHROPIC_AUTH_TOKEN")
	if key == "" && token == "" {
		t.Skip("set ANTHROPIC_API_KEY (the server runs with an empty HOME, so an `ant` profile is not seen)")
	}
	config, err := os.ReadFile(filepath.Join("..", "..", "deploy", "local", "agent", "search.json"))
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		envComponents + "=agent,mcp-server", envAPIToken + "=" + e2eToken,
		"ANTHROPIC_API_KEY=" + key, "ANTHROPIC_AUTH_TOKEN=" + token,
	}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "SSL_CERT_FILE"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	p := startServe(t, string(config), 20*time.Second, env...)
	a := agentClient{t: t, url: p.url, token: e2eToken}

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
	if len(calls) == 0 || calls[0].Name != "tavily_search" || results[0].IsError {
		t.Fatalf("calls %+v, results %+v: want a successful tavily_search first", calls, results)
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
