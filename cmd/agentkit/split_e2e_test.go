package main

// End-to-end test of the split deployment (T102): three processes, each the
// binary built with its image's tags (the Makefile's TAGS_api, TAGS_runner
// and TAGS_toolsets), the agent API calling the runner and the runner calling
// the toolsets over /rpc/. The model is the serve-agent replay, so the runs
// are TestE2EServeAgent's.

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/auth/cfaccess"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt/jwttest"
)

const splitToken = "rpc-token-0123456789abcdef"

func TestE2ESplitDeployment(t *testing.T) {
	t.Parallel()
	t.Run("plain", func(t *testing.T) {
		t.Parallel()
		splitDeployment(t, false)
	})
	// Every process gets the Access variables, as one shared environment
	// (Compose's anchor) gives them: the API checks assertions, and the
	// runner and toolsets, called with the RPC token alone, are not gated.
	t.Run("behind Cloudflare Access", func(t *testing.T) {
		t.Parallel()
		splitDeployment(t, true)
	})
}

func splitDeployment(t *testing.T, access bool) {
	c, api := newReplayAPI(t, "serve-agent")
	common := []string{envRPCToken + "=" + splitToken, envDependencyWait + "=30s"}
	var header http.Header
	if access {
		team := jwttest.NewIssuer(t)
		kid := team.AddKey(t, "RS256")
		const aud = "5917a0c2b7e4d3f6a8c9b0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1"
		assertion, _ := accessAssertions(t, team, kid, aud)
		header = http.Header{cfaccess.Header: {assertion(nil)}}
		common = append(common, envCFAccessTeamDomain+"="+team.URL(), envCFAccessAUD+"="+aud)
	}

	tools := startServeBin(t, taggedBinary(t, "toolsets"), agentConfigJSON, 5*time.Second,
		append(common, envComponents+"=toolsets,mcp-server")...)
	// The runner holds the model's key, and nothing else does.
	run := startServeBin(t, taggedBinary(t, "runner"), agentConfigJSON, 5*time.Second,
		append(common, envComponents+"=runner", envToolsetsURL+"="+tools.url,
			"ANTHROPIC_BASE_URL="+api.URL, "ANTHROPIC_API_KEY=e2e-dummy-key")...)
	front := startServeBin(t, taggedBinary(t, "api"), agentConfigJSON, 5*time.Second,
		append(common, envComponents+"=agent,web", envAPIToken+"="+e2eToken,
			envRunnerURL+"="+run.url, envToolsetsURL+"="+tools.url)...)

	for _, p := range []*serveProcess{tools, run, front} {
		var ready struct{ Status string }
		if code := p.getJSON(t, "/readyz", &ready); code != http.StatusOK || ready.Status != "ready" {
			t.Errorf("%s/readyz = %d %+v", p.url, code, ready)
		}
	}

	a := agentClient{t: t, url: front.url, token: e2eToken, header: header}
	if access {
		// The API's token alone does not get past Access.
		if _, err := (agentClient{t: t, url: front.url, token: e2eToken}).client().Agents(t.Context()); err == nil {
			t.Error("the API answered a request with no assertion")
		}
	}
	first := a.stream("Use the demo-b__echo tool to echo the word pineapple, then tell me exactly what the tool returned.", "", nil)
	calls, results := toolEvents(first)
	if len(calls) == 0 || calls[0].Name != "demo-b__echo" || !strings.Contains(results[0].Content, "[b] pineapple") {
		t.Errorf("first run's tool calls %+v, results %+v; want demo-b__echo answering [b] pineapple", calls, results)
	}
	conv := first[0].ConversationID
	second := a.stream("Now use demo-a__calculate to multiply 48271 by 9733, and tell me the result.", conv, nil)
	calls, results = toolEvents(second)
	if len(calls) == 0 || calls[0].Name != "demo-a__calculate" || results[0].Content != `{"result":469821643}` {
		t.Errorf("follow-up: tool calls %+v, results %+v", calls, results)
	}
	checkConversation(t, c.all()) // the runner sent the stored conversation back
	third := a.stream("Reply with just the word: ready", "", nil)
	for _, run := range [][]agentapi.Event{first, second, third} {
		if end := run[len(run)-1]; end.Type != agentapi.TypeRunDone {
			t.Errorf("run ended with %+v", end)
		}
	}

	// The API lists the toolsets the toolsets component serves, and the
	// console is served with it.
	sets, err := a.client().Toolsets(t.Context())
	if err != nil || len(sets) != 2 || sets[0].Status != "ready" || sets[0].ToolCount != 3 {
		t.Errorf("Toolsets = %+v, %v; want demo-a and demo-b, ready", sets, err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, front.url+"/", nil)
	for k, vs := range header {
		req.Header[k] = vs
	}
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("console: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	if _, err := a.client().Stream(t.Context(), agentclient.RunRequest{Message: "hi", Agent: "nope"}); err == nil {
		t.Error("a run of an unknown agent started")
	}

	// The runner was given the Access variables and checks no assertions:
	// it says so. The toolsets process serves mcp-server too, so it checks
	// them, but lets its /rpc/ through on the RPC token: the runs above
	// called both.
	if access {
		run.mu.Lock()
		logs := run.logs.String()
		run.mu.Unlock()
		if !strings.Contains(logs, "Cloudflare Access is configured but not checked here") {
			t.Error("the runner does not say Access is not checked there")
		}
		var ready struct{ Components []any }
		if tools.getJSON(t, "/readyz", &ready); len(ready.Components) != 0 {
			t.Errorf("the toolsets process told a caller with no assertion its components: %+v", ready)
		}
	}

	// The components refuse calls without the token.
	resp, err := http.Post(run.url+"/rpc/runner/Agents", "application/json", strings.NewReader("{}")) //nolint:noctx // local server
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("runner without the token: %v %v, want 401", resp, err)
	} else {
		resp.Body.Close()
	}

	// With the toolsets component down, the API is degraded and lists its
	// toolsets as unavailable, instead of answering 500.
	_ = tools.cmd.Process.Kill()
	<-tools.done
	var down struct {
		Components []struct{ Name, Status, Reason string }
	}
	code := front.getJSONHeader(t, "/readyz", header, &down)
	i := slices.IndexFunc(down.Components, func(c struct{ Name, Status, Reason string }) bool { return c.Name == "agent" })
	if code != http.StatusOK || i < 0 || down.Components[i].Status != "degraded" || !strings.Contains(down.Components[i].Reason, "toolsets at") {
		t.Errorf("API /readyz with the toolsets down = %d %+v, want the agent component degraded", code, down)
	}
	sets, err = a.client().Toolsets(t.Context())
	if err != nil || len(sets) != 2 || sets[0].Status != "unavailable" || sets[0].Reason == "" {
		t.Errorf("Toolsets with the toolsets down = %+v, %v; want both unavailable", sets, err)
	}
	if _, err := a.client().Agents(t.Context()); err != nil {
		t.Errorf("Agents with the toolsets down: %v", err)
	}
}

// TestE2ESplitAPIWithoutRunner: the API image has no model adapter, so it
// refuses to start agents without a runner, and says so.
func TestE2ESplitAPIWithoutRunner(t *testing.T) {
	t.Parallel()
	cmd := exec.Command(taggedBinary(t, "api"), "serve")
	cmd.Env = []string{"HOME=" + t.TempDir(), envComponents + "=agent", envAddr + "=127.0.0.1:0"}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), envRunnerURL) {
		t.Errorf("agentkit-api serve without a runner: %v\n%s\nwant a failure naming %s", err, out, envRunnerURL)
	}
}

var tagged sync.Map // variant → func() (string, error)

// taggedBinary builds the binary of an image variant, with the tags the
// Makefile gives it (TAGS_<variant>), once per test process.
func taggedBinary(t *testing.T, variant string) string {
	t.Helper()
	dir := filepath.Dir(binary(t)) // removed by TestMain
	build, _ := tagged.LoadOrStore(variant, sync.OnceValues(func() (string, error) {
		tags, err := makefileTags(variant)
		if err != nil {
			return "", err
		}
		bin := filepath.Join(dir, "agentkit-"+variant)
		out, err := exec.Command("go", "build", "-tags", tags, "-o", bin, ".").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("go build -tags %q: %w\n%s", tags, err, out)
		}
		return bin, nil
	}))
	bin, err := build.(func() (string, error))()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

// makefileTags reads TAGS_<variant> from the Makefile.
func makefileTags(variant string) (string, error) {
	f, err := os.Open(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if tags, ok := strings.CutPrefix(sc.Text(), "TAGS_"+variant+" := "); ok {
			return tags, nil
		}
	}
	return "", fmt.Errorf("the Makefile has no TAGS_%s", variant)
}
