package agentapi_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/tool/toolrpc"
	"github.com/bsamanvith/agentkit/pkg/tool/tooltest"
)

// dial serves s over HTTP and returns a connection to it.
func dial(t *testing.T, s *rpc.Server) rpc.Conn {
	t.Helper()
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	conn, err := rpc.Dial(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// remoteAgents is the split topology in one test: the toolsets served by one
// server, a runner that calls them by another, and the agent API calling the
// runner. Each hop is over HTTP.
func remoteAgents(t *testing.T, m *model) []agentapi.Agent {
	t.Helper()
	tools := rpc.NewServer()
	if err := toolrpc.Register(tools, []toolrpc.Toolset{{Name: "fx", Type: "fixtures"}},
		map[string]tool.Registry{"fx": registry(t, tooltest.Fixtures()...)}); err != nil {
		t.Fatal(err)
	}
	h := agentHarness(t, m, toolrpc.NewClient(dial(t, tools)).Registry("fx"))
	runners := rpc.NewServer()
	if err := runnerrpc.Register(runners, map[string]runner.Agent{"remote": h}); err != nil {
		t.Fatal(err)
	}
	return []agentapi.Agent{{Name: "remote", Runner: runnerrpc.NewClient(dial(t, runners)).Agent("remote", harness.ContextWindow{})}}
}

// TestAgentOnARemoteRunner: an agent whose turns run in a runner elsewhere
// serves runs as a local one does: tool calls, the answer, and the stored
// conversation the next run continues.
func TestAgentOnARemoteRunner(t *testing.T) {
	t.Parallel()
	m := script(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "echo", Input: json.RawMessage(`{"text":"ab","times":2}`)}),
		llmtest.Text("abab"),
		llmtest.Text("again"),
	)
	r := newRig(t, m, rigConfig{agents: remoteAgents(t, m)})

	first := r.run("echo ab twice", "")
	evs := first.all()
	if got := types(evs); fmt.Sprint(got) != fmt.Sprint([]string{
		agentapi.TypeRunStart, agentapi.TypeToolCall, agentapi.TypeToolResult, agentapi.TypeTurnDone,
		agentapi.TypeTextDelta, agentapi.TypeTurnDone, agentapi.TypeRunDone,
	}) {
		t.Fatalf("events = %v", got)
	}
	if end := first.last(); end.Answer != "abab" {
		t.Errorf("answer = %q, want abab", end.Answer)
	}
	for _, e := range evs {
		if e.Type == agentapi.TypeToolResult && (e.IsError || e.Content != `{"text":"abab"}`) {
			t.Errorf("tool result = %+v, want the echo tool's output", e)
		}
	}

	second := r.run("and again", first.conversationID()).all()
	if end := second[len(second)-1]; end.Type != agentapi.TypeRunDone || end.Answer != "again" {
		t.Fatalf("second run ended %+v", end)
	}
	reqs := m.requests()
	if n := len(reqs[2].Messages); n != 5 {
		t.Errorf("the second run sent %d messages, want the stored 4 and the new one", n)
	}
}

// TestRemoteRunnerFailureIsTheModelsError: a failure in the runner reaches
// the client as the model's error, retryable as it is locally.
func TestRemoteRunnerFailureIsTheModelsError(t *testing.T) {
	t.Parallel()
	m := script(t, llmtest.Fail(fmt.Errorf("%w: busy", llm.ErrOverloaded)))
	r := newRig(t, m, rigConfig{agents: remoteAgents(t, m)})
	end := r.run("hi", "").all()
	last := end[len(end)-1]
	if last.Type != agentapi.TypeRunError || last.Error == nil || last.Error.Code != agentapi.CodeOverloaded || !last.Error.Retryable {
		t.Errorf("run ended %+v (error %+v), want a retryable overloaded error", last, last.Error)
	}
}
