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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
)

const splitToken = "rpc-token-0123456789abcdef"

func TestE2ESplitDeployment(t *testing.T) {
	t.Parallel()
	c, api := newReplayAPI(t, "serve-agent")
	common := []string{envRPCToken + "=" + splitToken, envDependencyWait + "=30s"}

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

	a := agentClient{t: t, url: front.url, token: e2eToken}
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
	if resp, err := http.Get(front.url + "/"); err != nil || resp.StatusCode != http.StatusOK { //nolint:noctx // local server
		t.Errorf("console: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	if _, err := a.client().Stream(t.Context(), agentclient.RunRequest{Message: "hi", Agent: "nope"}); err == nil {
		t.Error("a run of an unknown agent started")
	}

	// The components refuse calls without the token.
	resp, err := http.Post(run.url+"/rpc/runner/Agents", "application/json", strings.NewReader("{}")) //nolint:noctx // local server
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("runner without the token: %v %v, want 401", resp, err)
	} else {
		resp.Body.Close()
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
