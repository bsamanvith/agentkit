//go:build live

package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// TestLiveAskSearch runs agentkit ask -json against agentkit serve with
// deploy/local/agent/search.json, real Claude and the real Tavily, keyless:
// the "search" agent answers a question about the present with a
// tavily_search call, and the run ends with run_done.
func TestLiveAskSearch(t *testing.T) {
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
	var proxy []string
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "SSL_CERT_FILE"} {
		if v, ok := os.LookupEnv(k); ok {
			proxy = append(proxy, k+"="+v)
		}
	}
	p := startServe(t, config, 20*time.Second, append(env, proxy...)...)

	cmd := exec.Command(binary(t), "ask", "-json", "-agent", "search",
		"What is the latest stable release of the Go programming language? Search the web, and cite the source URL.")
	cmd.Env = []string{
		"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH"),
		envServer + "=" + p.url, envAPIToken + "=" + e2eToken,
	}
	cmd.Env = append(cmd.Env, proxy...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	t.Logf("agentkit ask: %v\nstderr:\n%s", err, &stderr)
	if err != nil {
		t.Fatalf("exit %d", cmd.ProcessState.ExitCode())
	}
	events := parseJSONL(t, stdout.String())
	checkAgentStream(t, events)
	calls, results := toolEvents(events)
	end := events[len(events)-1]
	t.Logf("run: %v; answer %q; usage %+v", eventTypes(events), end.Answer, end.Usage)
	if len(calls) == 0 || calls[0].Name != "tavily_search" || results[0].IsError {
		t.Errorf("calls %+v, results %+v: want a successful tavily_search first", calls, results)
	}
	if end.Type != wire.TypeRunDone {
		t.Errorf("last event %+v, want run_done", end)
	}
	checkCLIOutput(t, stdout.String()+stderr.String(), e2eToken)
	if strings.Contains(stdout.String(), key) && key != "" {
		t.Error("the output holds the Anthropic key")
	}
}
