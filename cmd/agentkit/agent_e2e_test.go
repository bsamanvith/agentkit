package main

// End-to-end test of the agent API: the built binary serving agent and
// mcp-server, against a local server replaying Claude responses recorded from
// the real API (testdata/e2e/serve-agent). Record again with:
//
//	AGENTKIT_E2E_RECORD=1 ANTHROPIC_API_KEY=... go test -count=1 -run TestE2EServeAgent ./cmd/agentkit/

import (
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
)

const e2eToken = "e2e-token-0123456789abcdef"

// agentConfigJSON serves two demo toolsets over MCP and gives the agent both,
// with prefixed tool names (both have echo and calculate).
const agentConfigJSON = `{
  "mcp": {
    "toolsets": [
      {"name": "demo-a", "type": "demo", "settings": {"echo_prefix": "[a] "}},
      {"name": "demo-b", "type": "demo", "settings": {"echo_prefix": "[b] "}, "exclude": ["current_time"]}
    ]
  },
  "agent": {
    "model": {"effort": "low", "max_tokens": 8000},
    "system": "You are a test agent. Use the tools exactly as asked, then answer in one short sentence.",
    "toolsets": ["demo-a", "demo-b"],
    "prefix_tool_names": true,
    "max_turns": 6
  }
}`

// agentClient talks to the agent API of a serve process, through
// pkg/agentclient.
type agentClient struct {
	t     *testing.T
	url   string
	token string
}

func (a agentClient) client() *agentclient.Client {
	a.t.Helper()
	// A stream that never ends fails, not hangs.
	c, err := agentclient.New(a.url, agentclient.WithToken(a.token), agentclient.WithHTTPClient(&http.Client{Timeout: 2 * time.Minute}))
	if err != nil {
		a.t.Fatal(err)
	}
	return c
}

// stream posts a run and reads its events to the end, calling each for every
// event as it arrives.
func (a agentClient) stream(message, conversationID string, each func(agentapi.Event)) []agentapi.Event {
	a.t.Helper()
	s, err := a.client().Stream(a.t.Context(), agentclient.RunRequest{Message: message, ConversationID: conversationID})
	if err != nil {
		a.t.Fatalf("POST /v1/runs: %v", err)
	}
	defer s.Close()
	var events []agentapi.Event
	for {
		e, err := s.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			a.t.Fatalf("after %d events: %v", len(events), err)
		}
		events = append(events, e)
		if each != nil {
			each(e)
		}
	}
	checkAgentStream(a.t, events)
	return events
}

// checkAgentStream checks what holds for every run's stream: it starts with
// run_start, seq counts from 0, exactly one run_done or run_error comes last,
// and the last turn's text deltas are the answer.
func checkAgentStream(t *testing.T, events []agentapi.Event) {
	t.Helper()
	if len(events) < 2 || events[0].Type != agentapi.TypeRunStart {
		t.Fatalf("stream %v does not start with run_start", eventTypes(events))
	}
	deltas := map[int]string{}
	last, terminal := 0, 0
	for i, e := range events {
		if e.Seq != i || e.V != agentapi.WireVersion || e.RunID != events[0].RunID {
			t.Errorf("event %d: %+v", i, e)
		}
		switch e.Type {
		case agentapi.TypeTextDelta:
			deltas[e.Turn] += e.Text
		case agentapi.TypeTurnDone:
			last = e.Turn
		case agentapi.TypeRunDone, agentapi.TypeRunError:
			terminal++
		}
	}
	end := events[len(events)-1]
	if terminal != 1 || (end.Type != agentapi.TypeRunDone && end.Type != agentapi.TypeRunError) {
		t.Fatalf("stream %v: want one run_done or run_error, last", eventTypes(events))
	}
	if end.Type == agentapi.TypeRunDone && end.Answer != deltas[last] {
		t.Errorf("answer %q, but the last turn's deltas are %q", end.Answer, deltas[last])
	}
}

func eventTypes(events []agentapi.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

func toolEvents(events []agentapi.Event) (calls, results []agentapi.Event) {
	for _, e := range events {
		switch e.Type {
		case agentapi.TypeToolCall:
			calls = append(calls, e)
		case agentapi.TypeToolResult:
			results = append(results, e)
		}
	}
	return calls, results
}

// TestE2EServeAgent runs agentkit serve with agent and mcp-server against
// recorded Claude responses: a run that calls a tool of one toolset, a
// follow-up in the same conversation that calls a tool of the other, then
// SIGTERM while a third run streams: the stream finishes and the process
// exits 0.
func TestE2EServeAgent(t *testing.T) {
	t.Parallel()
	c, api := newReplayAPI(t, "serve-agent")
	c.holdAt = 5
	const deadline = 20 * time.Second
	p := startServe(t, agentConfigJSON, deadline,
		envComponents+"=agent,mcp-server", envAPIToken+"="+e2eToken,
		"ANTHROPIC_BASE_URL="+api.URL, "ANTHROPIC_API_KEY=e2e-dummy-key")
	a := agentClient{t: t, url: p.url, token: e2eToken}

	var ready struct {
		Status     string
		Components []struct{ Name, Status string }
	}
	if code := p.getJSON(t, "/readyz", &ready); code != http.StatusOK || len(ready.Components) != 2 ||
		ready.Components[0].Name != "agent" || ready.Components[0].Status != "ready" {
		t.Errorf("/readyz = %d %+v, want agent and mcp-server ready", code, ready)
	}
	if _, err := (agentClient{t: t, url: p.url}).client().Stream(t.Context(), agentclient.RunRequest{Message: "hi"}); !errors.Is(err, agentclient.ErrUnauthorized) {
		t.Errorf("run without the token: %v, want 401 unauthorized", err)
	}

	first := a.stream("Use the demo-b__echo tool to echo the word pineapple, then tell me exactly what the tool returned.", "", nil)
	calls, results := toolEvents(first)
	if len(calls) == 0 || calls[0].Name != "demo-b__echo" || !strings.Contains(results[0].Content, "[b] pineapple") {
		t.Errorf("first run's tool calls %+v, results %+v; want demo-b__echo answering [b] pineapple", calls, results)
	}
	if end := first[len(first)-1]; end.Type != agentapi.TypeRunDone || !strings.Contains(end.Answer, "pineapple") {
		t.Errorf("first run ended with %+v", end)
	}
	conv := first[0].ConversationID

	second := a.stream("Now use demo-a__calculate to multiply 48271 by 9733, and tell me the result.", conv, nil)
	calls, results = toolEvents(second)
	if second[0].ConversationID != conv || len(calls) == 0 || calls[0].Name != "demo-a__calculate" || results[0].Content != `{"result":469821643}` {
		t.Errorf("follow-up: conversation %s, tool calls %+v, results %+v", second[0].ConversationID, calls, results)
	}
	if end := second[len(second)-1]; end.Type != agentapi.TypeRunDone || !strings.Contains(strings.ReplaceAll(end.Answer, ",", ""), "469821643") {
		t.Errorf("follow-up ended with %+v", end)
	}
	if got, err := a.client().GetConversation(t.Context(), conv); err != nil || got.Status != agentapi.StatusIdle || len(got.Messages) < 8 {
		t.Errorf("conversation = %+v (%v), want idle with both runs (8 messages or more)", got, err)
	}
	checkConversation(t, c.all()) // the follow-up carried the first run unchanged

	// The same process serves the toolsets over MCP.
	cs := connectMCP(t, p.url+"/mcp/demo-a", nil)
	if text, err := echo(t, cs, "hi"); err != nil || text != "[a] hi" {
		t.Errorf("MCP echo = %q %v", text, err)
	}
	_ = cs.Close()

	// SIGTERM while a run streams: the model's answer is held until the
	// listener has closed, then the stream finishes.
	done := make(chan []agentapi.Event, 1)
	go func() { done <- a.stream("Reply with just the word: ready", "", nil) }()
	select {
	case <-c.held:
	case <-time.After(2 * time.Minute):
		t.Fatal("the third run never reached the model")
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	signalled := time.Now()
	testkit.Eventually(t, deadline, func() bool {
		conn, err := net.Dial("tcp", p.addr)
		if err == nil {
			_ = conn.Close()
		}
		return err != nil
	})
	close(c.release)
	third := <-done
	if end := third[len(third)-1]; end.Type != agentapi.TypeRunDone {
		t.Errorf("run during the shutdown ended with %+v, want run_done", end)
	}
	select {
	case <-p.done:
	case <-time.After(deadline + 5*time.Second):
		t.Fatalf("agentkit serve still running %s after SIGTERM", time.Since(signalled))
	}
	if code := p.cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("exit %d after SIGTERM, want 0", code)
	}
	if !slices.ContainsFunc(c.all(), func(r request) bool { return strings.Contains(string(r.body), "demo-b__echo") }) {
		t.Error("the model was not offered the prefixed tools")
	}
}
