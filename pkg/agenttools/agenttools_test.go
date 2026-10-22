package agenttools_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agenttools"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// modes are the two ways the toolset reaches the agent API.
var modes = map[string]bool{"in process": false, "url": true}

// TestAgentsOverMCP: through the MCP SDK client, agents_list lists the
// agents; agent_run runs one and returns its answer, IDs, tool calls and
// usage; a second agent_run with the conversation_id continues the
// conversation (the model gets the history); run_get, conversations_list and
// conversation_get show the same. Both in process and by URL.
func TestAgentsOverMCP(t *testing.T) {
	t.Parallel()
	for name, remote := range modes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			helper := llmtest.NewFake(t,
				llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "echo", Input: json.RawMessage(`{"text":"ping"}`)}).WithUsage(llm.Usage{InputTokens: 10, OutputTokens: 5}),
				llmtest.Text("first answer").WithUsage(llm.Usage{InputTokens: 20, OutputTokens: 7}),
				llmtest.Text("second answer"),
			)
			r := newRig(t, helper, llmtest.NewFake(t), rigOptions{remote: remote})
			cs := r.client(keyFull)

			var agents agenttools.AgentsOutput
			mustCall(t, cs, "agents_list", nil, &agents)
			want := []agenttools.Agent{
				{Name: "default", Description: "helps", Default: true, Toolsets: []string{"demo"}},
				{Name: "wide", Description: "sees more", Toolsets: []string{"demo", "secret"}},
			}
			if !equalJSON(agents.Agents, want) {
				t.Errorf("agents_list = %+v, want %+v", agents.Agents, want)
			}

			var first agenttools.Run
			mustCall(t, cs, "agent_run", map[string]any{"agent": "default", "message": "hello"}, &first)
			if first.Status != wire.StatusDone || first.Answer != "first answer" || first.RunID == "" || first.ConversationID == "" || first.Agent != "default" {
				t.Fatalf("agent_run = %+v", first)
			}
			if want := []agenttools.ToolCall{{Name: "echo", Input: `{"text":"ping"}`, OK: true}}; !equalJSON(first.ToolCalls, want) {
				t.Errorf("tool calls = %+v, want %+v", first.ToolCalls, want)
			}
			if first.Usage.InputTokens != 30 || first.Usage.OutputTokens != 12 || first.Turns != 2 {
				t.Errorf("usage %+v, turns %d: want 30 in, 12 out, 2 turns", first.Usage, first.Turns)
			}

			var second agenttools.Run
			mustCall(t, cs, "agent_run", map[string]any{"message": "and again", "conversation_id": first.ConversationID}, &second)
			if second.Status != wire.StatusDone || second.Answer != "second answer" || second.ConversationID != first.ConversationID || second.RunID == first.RunID {
				t.Fatalf("second agent_run = %+v (first %+v)", second, first)
			}
			reqs := helper.Requests()
			if got := texts(reqs[len(reqs)-1].Messages); !slices.Contains(got, "hello") || !slices.Contains(got, "first answer") || !slices.Contains(got, "and again") {
				t.Errorf("the second run's model call has %q: want the conversation's history", got)
			}

			var got agenttools.Run
			mustCall(t, cs, "run_get", map[string]any{"run_id": first.RunID}, &got)
			if !equalJSON(got, first) {
				t.Errorf("run_get = %+v, want what agent_run returned, %+v", got, first)
			}

			var convs agenttools.ConversationsOutput
			mustCall(t, cs, "conversations_list", map[string]any{"agent": "default", "limit": 10}, &convs)
			if len(convs.Conversations) != 1 || convs.Conversations[0].ID != first.ConversationID || convs.Conversations[0].Title != "hello" || convs.Conversations[0].MessageCount != 6 {
				t.Errorf("conversations_list = %+v", convs)
			}
			var conv agenttools.Conversation
			mustCall(t, cs, "conversation_get", map[string]any{"conversation_id": first.ConversationID}, &conv)
			var roles []string
			for _, m := range conv.Messages {
				roles = append(roles, m.Role)
			}
			if conv.Agent != "default" || conv.Status != wire.StatusIdle || strings.Join(roles, ",") != "user,assistant,user,assistant,user,assistant" {
				t.Fatalf("conversation_get = %+v", conv)
			}
			if c := conv.Messages[1].ToolCalls; len(c) != 1 || c[0].Name != "echo" || c[0].Input != `{"text":"ping"}` {
				t.Errorf("the reply's tool calls = %+v", c)
			}
			if res := conv.Messages[2].ToolResults; len(res) != 1 || !res[0].OK || !strings.Contains(res[0].Content, "ping") {
				t.Errorf("the tool results = %+v", res)
			}
			if conv.Messages[5].Text != "second answer" {
				t.Errorf("last message %+v", conv.Messages[5])
			}
		})
	}
}

// TestAgentRunTimeout: an agent_run past its timeout returns status running
// with the run and conversation IDs; the run is not interrupted (the API
// here interrupts at once a run nobody follows), and run_get later shows it
// done.
func TestAgentRunTimeout(t *testing.T) {
	t.Parallel()
	for name, remote := range modes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := newGated(llmtest.NewFake(t, llmtest.Text("slow answer")))
			r := newRig(t, model, llmtest.NewFake(t), rigOptions{remote: remote, settings: map[string]any{"timeout": "2m"}, interruptAtOnce: true, fakeClock: true})
			cs := r.client(keyFull)

			run := r.runPastTimeout(cs, model, 2*time.Minute, map[string]any{"message": "take your time"})
			if run.Status != wire.StatusRunning || run.RunID == "" || run.ConversationID == "" || !strings.Contains(run.Note, "run_get") {
				t.Fatalf("agent_run past its timeout = %+v: want status running, the IDs and a note", run)
			}
			var got agenttools.Run
			mustCall(t, cs, "run_get", map[string]any{"run_id": run.RunID}, &got)
			if got.Status != wire.StatusRunning {
				t.Fatalf("run_get right after = %+v: want it still running", got)
			}
			close(model.release)
			if got = waitEnd(t, cs, run.RunID); got.Status != wire.StatusDone || got.Answer != "slow answer" || got.ConversationID != run.ConversationID {
				t.Errorf("run_get = %+v: want it done, with its answer", got)
			}
		})
	}
}

// TestCallTimeoutCutsTheWait: with an MCP call timeout (500ms) under the
// toolset's timeout, agent_run returns status running a tenth before it
// (450ms), so the server does not cut the call off.
func TestCallTimeoutCutsTheWait(t *testing.T) {
	t.Parallel()
	model := newGated(llmtest.NewFake(t, llmtest.Text("late")))
	r := newRig(t, model, llmtest.NewFake(t), rigOptions{callTimeout: 500 * time.Millisecond, fakeClock: true})
	cs := r.client(keyFull)
	run := r.runPastTimeout(cs, model, 450*time.Millisecond, map[string]any{"message": "hi"})
	if run.Status != wire.StatusRunning || run.RunID == "" {
		t.Fatalf("agent_run = %+v: want running before the call timeout", run)
	}
	close(model.release)
	waitEnd(t, cs, run.RunID)
}

// TestAuth: the toolset needs credentials; each caller acts with its own key
// at the API: its tenant's conversations only, only the agents whose
// toolsets it may all use, and only what its scopes allow.
func TestAuth(t *testing.T) {
	t.Parallel()
	for name, remote := range modes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, llmtest.NewFake(t, llmtest.Text("t1's answer")), llmtest.NewFake(t), rigOptions{remote: remote})
			if _, err := r.connect(""); err == nil {
				t.Error("connected with no credentials")
			}
			if _, err := r.connect("not-a-key-0123456789abcdef"); err == nil {
				t.Error("connected with a wrong key")
			}
			if _, err := r.connect(keyNoMCP); err == nil {
				t.Error("connected with a key that does not allow the agentkit toolset")
			}

			full := r.client(keyFull)
			var run agenttools.Run
			mustCall(t, full, "agent_run", map[string]any{"message": "secret plans"}, &run)

			other := r.client(keyOther)
			var convs agenttools.ConversationsOutput
			mustCall(t, other, "conversations_list", nil, &convs)
			if len(convs.Conversations) != 0 {
				t.Errorf("tenant t2 lists %+v: want none of t1's conversations", convs.Conversations)
			}
			for tool, args := range map[string]map[string]any{
				"conversation_get": {"conversation_id": run.ConversationID},
				"run_get":          {"run_id": run.RunID},
				"agent_run":        {"message": "me too", "conversation_id": run.ConversationID},
			} {
				if e := call(t, other, tool, args, nil); !strings.Contains(e, "not_found") {
					t.Errorf("%s of t1's run as t2 = %q: want not_found", tool, e)
				}
			}
			mustCall(t, full, "conversations_list", nil, &convs)
			if len(convs.Conversations) != 1 {
				t.Errorf("tenant t1 lists %+v: want its conversation", convs.Conversations)
			}

			narrow := r.client(keyNarrow)
			var agents agenttools.AgentsOutput
			mustCall(t, narrow, "agents_list", nil, &agents)
			if len(agents.Agents) != 1 || agents.Agents[0].Name != "default" {
				t.Errorf("agents of a key without toolset secret = %+v: want default only", agents.Agents)
			}
			if e := call(t, narrow, "agent_run", map[string]any{"agent": "wide", "message": "hi"}, nil); !strings.Contains(e, "wide") {
				t.Errorf("running agent wide with that key = %q: want an error naming no agent wide", e)
			}

			reader := r.client(keyReader)
			mustCall(t, reader, "agents_list", nil, &agents)
			if e := call(t, reader, "agent_run", map[string]any{"message": "hi"}, nil); !strings.Contains(e, "forbidden") {
				t.Errorf("agent_run with a read-only key = %q: want forbidden", e)
			}
		})
	}
}

// TestRunErrors: requests the API refuses are tool errors with its message:
// an unknown agent, another agent's conversation, a busy conversation (with
// the run going on), an empty message; a run that fails is a result.
func TestRunErrors(t *testing.T) {
	t.Parallel()
	model := newGated(llmtest.NewFake(t, llmtest.Text("one"), llmtest.Fail(errors.New("the model broke"))))
	r := newRig(t, model, llmtest.NewFake(t), rigOptions{fakeClock: true})
	cs := r.client(keyFull)

	if e := call(t, cs, "agent_run", map[string]any{"agent": "nobody", "message": "hi"}, nil); !strings.Contains(e, "nobody") {
		t.Errorf("unknown agent: %q", e)
	}
	run := r.runPastTimeout(cs, model, agenttools.DefaultTimeout, map[string]any{"message": "first"})
	if run.Status != wire.StatusRunning {
		t.Fatalf("agent_run = %+v: want it still running", run)
	}
	if e := call(t, cs, "agent_run", map[string]any{"message": "second", "conversation_id": run.ConversationID}, nil); !strings.Contains(e, "conversation_busy") || !strings.Contains(e, run.RunID) {
		t.Errorf("a busy conversation: %q: want conversation_busy naming run %s", e, run.RunID)
	}
	if e := call(t, cs, "agent_run", map[string]any{"agent": "wide", "message": "x", "conversation_id": run.ConversationID}, nil); !strings.Contains(e, "agent_mismatch") {
		t.Errorf("another agent's conversation: %q", e)
	}
	if e := call(t, cs, "agent_run", map[string]any{"message": ""}, nil); !strings.Contains(e, "invalid_request") {
		t.Errorf("an empty message: %q", e)
	}
	if e := call(t, cs, "agent_run", map[string]any{"agent": 7, "message": "x"}, nil); e == "" {
		t.Error("an agent that is not a string: no error")
	}
	if e := call(t, cs, "run_get", map[string]any{"run_id": "nope"}, nil); !strings.Contains(e, "not_found") {
		t.Errorf("unknown run: %q", e)
	}
	close(model.release)
	waitEnd(t, cs, run.RunID)

	var failed agenttools.Run
	mustCall(t, cs, "agent_run", map[string]any{"message": "break", "conversation_id": run.ConversationID}, &failed)
	if failed.Status != wire.StatusFailed || failed.Error == nil || failed.Error.Code == "" {
		t.Errorf("a run whose model fails = %+v: want status failed with its error", failed)
	}
}

// TestToolCallSummary: each call's input is cut to MaxInputBytes on a rune
// boundary, a failed call carries its error, and a call left with no result
// is not ok.
func TestToolCallSummary(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", 300)
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(
			llm.ToolUse{ID: "a", Name: "echo", Input: json.RawMessage(`{"text":"` + long + `"}`)},
			llm.ToolUse{ID: "b", Name: "calculate", Input: json.RawMessage(`{"op":"div","a":1,"b":0}`)},
		),
		llmtest.Text("done"),
	)
	r := newRig(t, model, llmtest.NewFake(t), rigOptions{})
	var run agenttools.Run
	mustCall(t, r.client(keyFull), "agent_run", map[string]any{"message": "go"}, &run)
	if len(run.ToolCalls) != 2 {
		t.Fatalf("tool calls %+v", run.ToolCalls)
	}
	echo, calc := run.ToolCalls[0], run.ToolCalls[1]
	in := strings.TrimSuffix(echo.Input, "…")
	if !echo.OK || len(in) > agenttools.MaxInputBytes || !strings.HasPrefix(`{"text":"`+long, in) || in == echo.Input {
		t.Errorf("echo %+v: want ok, its input cut to %d bytes at a rune boundary and marked", echo, agenttools.MaxInputBytes)
	}
	if calc.OK || calc.Error == "" || calc.Name != "calculate" {
		t.Errorf("calculate %+v: want not ok, with its error", calc)
	}
}

// TestSettings: the toolset's settings are strict, and its definitions are
// classified, with an output schema each.
func TestSettings(t *testing.T) {
	t.Parallel()
	inProc, err := agenttools.New(agenttools.WithHandler(http404{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inProc.Close() })
	noAPI, err := agenttools.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = noAPI.Close() })
	for name, tc := range map[string]struct {
		f        *agenttools.Factory
		settings string
		says     string // "" for valid settings
	}{
		"empty, in process":   {f: inProc, settings: ``},
		"url":                 {f: noAPI, settings: `{"url":"http://agent:8080/prefix","timeout":"90s"}`},
		"unknown field":       {f: inProc, settings: `{"timout":"1m"}`, says: "timout"},
		"bad timeout":         {f: inProc, settings: `{"timeout":"soon"}`, says: "soon"},
		"zero timeout":        {f: inProc, settings: `{"timeout":"0s"}`, says: "timeout"},
		"long timeout":        {f: inProc, settings: `{"timeout":"2h"}`, says: "timeout"},
		"no url, no API here": {f: noAPI, settings: `{}`, says: "url is required"},
		"relative url":        {f: noAPI, settings: `{"url":"agent:8080"}`, says: "url"},
		"url with a query":    {f: noAPI, settings: `{"url":"http://agent:8080/?x=1"}`, says: "url"},
		"url with a user":     {f: noAPI, settings: `{"url":"http://u:p@agent:8080"}`, says: "url"},
		"ftp url":             {f: noAPI, settings: `{"url":"ftp://agent"}`, says: "url"},
	} {
		tools, err := tc.f.Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(tc.settings)})
		switch {
		case tc.says == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.says == "":
			checkDefinitions(t, tools)
		case !errors.Is(err, toolset.ErrInvalidSettings) || !strings.Contains(err.Error(), tc.says):
			t.Errorf("%s: %v: want ErrInvalidSettings saying %q", name, err, tc.says)
		}
	}
	if _, err := agenttools.New(agenttools.WithCallTimeout(-time.Second)); err == nil {
		t.Error("New with a negative call timeout: no error")
	}
}

func checkDefinitions(t *testing.T, tools []tool.Tool) {
	t.Helper()
	var names []string
	for _, tl := range tools {
		d := tl.Definition()
		names = append(names, d.Name)
		if !d.Classified() || len(d.OutputSchema) == 0 || d.Description == "" {
			t.Errorf("%s: classified %v, output schema %s, description %q", d.Name, d.Classified(), d.OutputSchema, d.Description)
		}
		if k, _ := d.Class(); (k == tool.KindRead) == (d.Name == "agent_run") {
			t.Errorf("%s is %s: want agent_run the only tool that is not read-only", d.Name, k)
		}
	}
	if want := []string{"agents_list", "agent_run", "run_get", "conversations_list", "conversation_get"}; !slices.Equal(names, want) {
		t.Errorf("tools %v, want %v", names, want)
	}
}

// TestClose: Close stops the followers of runs agent_run stopped waiting
// for (the API then interrupts them after its grace), and agent_run fails
// after it.
func TestClose(t *testing.T) {
	t.Parallel()
	model := newGated(llmtest.NewFake(t))
	r := newRig(t, model, llmtest.NewFake(t), rigOptions{interruptAtOnce: true, fakeClock: true})
	cs := r.client(keyFull)
	run := r.runPastTimeout(cs, model, agenttools.DefaultTimeout, map[string]any{"message": "hi"})
	if err := r.factory.Close(); err != nil {
		t.Fatal(err)
	}
	if got := waitEnd(t, cs, run.RunID); got.Error == nil || got.Error.Code != wire.CodeRunInterrupted {
		t.Errorf("run after Close = %+v: want it interrupted", got)
	}
	if e := call(t, cs, "agent_run", map[string]any{"message": "again"}, nil); !strings.Contains(e, agenttools.ErrClosed.Error()) {
		t.Errorf("agent_run after Close = %q", e)
	}
	var agents agenttools.AgentsOutput
	mustCall(t, cs, "agents_list", nil, &agents)
}

func texts(msgs []llm.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Text())
	}
	return out
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
