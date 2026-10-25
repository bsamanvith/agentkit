package agentapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

func call(id, name, input string) llm.ToolUse {
	return llm.ToolUse{ID: id, Name: name, Input: json.RawMessage(input)}
}

// textAndCalls is a reply with some text, then tool calls.
func textAndCalls(text string, calls ...llm.ToolUse) llmtest.Reply {
	r := llmtest.ToolCalls(calls...)
	r.Response.Message.Content = append([]llm.Block{llm.Text{Text: text}}, r.Response.Message.Content...)
	return r
}

// demoTools builds two demo toolsets told apart by their echo prefix.
func demoTools(t *testing.T) map[string]tool.Registry {
	t.Helper()
	catalog, err := toolset.NewCatalog(toolset.Demo())
	if err != nil {
		t.Fatal(err)
	}
	sets, _, err := toolset.Build(t.Context(), catalog, []toolset.Spec{
		{Name: "a", Type: toolset.DemoType, Settings: json.RawMessage(`{"echo_prefix":"[a] "}`)},
		{Name: "b", Type: toolset.DemoType, Settings: json.RawMessage(`{"echo_prefix":"[b] "}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return sets
}

func registry(t *testing.T, tools ...tool.Tool) tool.Registry {
	t.Helper()
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

type textIn struct {
	Text string `json:"text"`
}

// waitTool is a tool that runs until its context is done, and counts its calls.
func waitTool(t *testing.T, calls *atomic.Int32) tool.Tool {
	t.Helper()
	tl, err := tool.Func("wait", "Wait.", func(ctx context.Context, _ textIn) (textIn, error) {
		calls.Add(1)
		<-ctx.Done()
		return textIn{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

// TestRunStreamsEventsInOrder: a run with text and two tool calls in its
// first turn, then an answer, streams its events in order and ends with one
// run_done whose answer is the last turn's deltas (rig.check verifies all of
// it after every test; here the shape is also spelled out).
func TestRunStreamsEventsInOrder(t *testing.T) {
	t.Parallel()
	sets := demoTools(t)
	r := newRig(t, script(t,
		textAndCalls("Let me check both.", call("c1", "echo", `{"text":"one"}`), call("c2", "calculate", `{"op":"mul","a":6,"b":7}`)).
			WithUsage(llm.Usage{InputTokens: 10, OutputTokens: 5}),
		llmtest.Text("Both done: one, 42.").WithUsage(llm.Usage{InputTokens: 20, OutputTokens: 7, CacheReadTokens: 3}),
	), rigConfig{tools: sets["a"]})

	s := r.run("echo one and multiply 6 by 7", "")
	got := types(s.events)
	// Between run_start and the first turn_done: text deltas, then each call's
	// tool_call before its tool_result (the two calls may interleave).
	if got[0] != agentapi.TypeRunStart || got[len(got)-1] != agentapi.TypeRunDone {
		t.Fatalf("events %v", got)
	}
	n := map[string]int{}
	for _, typ := range got {
		n[typ]++
	}
	if n[agentapi.TypeToolCall] != 2 || n[agentapi.TypeToolResult] != 2 || n[agentapi.TypeTurnDone] != 2 {
		t.Errorf("events %v: want 2 tool calls, 2 results, 2 turns", got)
	}
	end := s.last()
	if end.Answer != "Both done: one, 42." || end.Turns != 2 || *end.Usage != (llm.Usage{InputTokens: 30, OutputTokens: 12, CacheReadTokens: 3}) {
		t.Errorf("run_done = %+v", end)
	}
	for _, e := range s.events {
		if e.Type == agentapi.TypeToolResult && e.Call == 1 && e.Content != `{"result":42}` {
			t.Errorf("calculate result %q", e.Content)
		}
	}
}

// TestJSONResponse: with Accept: application/json the response is the run,
// with the same answer and record a stream would have given.
func TestJSONResponse(t *testing.T) {
	t.Parallel()
	r := newRig(t, script(t, llmtest.Text("Hello there.")), rigConfig{})
	run := r.runJSON("hi", "")
	if run.Status != agentapi.StatusDone || run.Answer != "Hello there." || run.Turns != 1 || run.Record == nil || len(run.Record.Turns) != 1 || run.FinishedAt == nil {
		t.Fatalf("run = %+v", run)
	}
	if got := r.getRun(run.ID); !jsonEqual(got, run) {
		t.Errorf("GET /v1/runs/%s = %s, want %s", run.ID, jsonOf(got), jsonOf(run))
	}
	c := r.getConversation(run.ConversationID)
	if len(c.Messages) != 2 || c.Messages[0].Message.Text() != "hi" || c.Messages[1].Message.Text() != "Hello there." || c.Status != agentapi.StatusIdle {
		t.Errorf("conversation = %s", jsonOf(c))
	}
}

// TestFollowUpContinuesTheConversation: a follow-up with conversation_id
// sends the model the earlier turns unchanged, then the new message.
func TestFollowUpContinuesTheConversation(t *testing.T) {
	t.Parallel()
	sets := demoTools(t)
	m := script(t,
		llmtest.ToolCalls(call("c1", "echo", `{"text":"x"}`)),
		llmtest.Text("Echoed."),
		llmtest.Text("Second answer."),
		llmtest.Text("Third answer."),
	)
	r := newRig(t, m, rigConfig{tools: sets["a"]})
	first := r.run("echo x", "")
	conv := first.conversationID()
	second := r.run("and now?", conv)
	third := r.run("and then?", conv)
	if second.conversationID() != conv || third.conversationID() != conv || second.last().Answer != "Second answer." {
		t.Fatalf("follow-ups went to %s and %s, want %s", second.conversationID(), third.conversationID(), conv)
	}
	reqs := m.requests()
	if len(reqs) != 4 {
		t.Fatalf("%d model requests, want 4", len(reqs))
	}
	// The second run's request is the whole first run, unchanged, plus the
	// new message; the third extends the second the same way.
	want := append(slices.Clone(reqs[1].Messages), first.last().Final.Clone(), harness.UserText("and now?"))
	if !jsonEqual(reqs[2].Messages, want) {
		t.Errorf("follow-up request\n%s\nwant\n%s", jsonOf(reqs[2].Messages), jsonOf(want))
	}
	want = append(append(slices.Clone(reqs[2].Messages), second.last().Final.Clone()), harness.UserText("and then?"))
	if !jsonEqual(reqs[3].Messages, want) {
		t.Errorf("third request\n%s\nwant\n%s", jsonOf(reqs[3].Messages), jsonOf(want))
	}
	if c := r.getConversation(conv); len(c.Messages) != 8 {
		t.Errorf("conversation has %d messages, want 8", len(c.Messages))
	}
}

// TestToolsFromTwoToolsets: the agent calls tools of two toolsets merged in
// process; without prefixes their clashing names fail, with prefixes each
// call reaches its own toolset.
func TestToolsFromTwoToolsets(t *testing.T) {
	t.Parallel()
	sets := demoTools(t)
	if _, err := agentapi.MergeTools(t.Context(), sets, false); !errors.Is(err, agentapi.ErrToolNameClash) {
		t.Fatalf("merge without prefix: %v, want ErrToolNameClash", err)
	}
	merged, err := agentapi.MergeTools(t.Context(), sets, true)
	if err != nil {
		t.Fatal(err)
	}
	defs, err := merged.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	want := []string{"a__calculate", "a__current_time", "a__echo", "b__calculate", "b__current_time", "b__echo"}
	if !slices.Equal(names, want) {
		t.Fatalf("merged tools %v, want %v", names, want)
	}
	// Apart from the name, each merged definition is its toolset's own: the
	// schemas and the metadata policies read (kind, risk, tags) survive.
	for _, d := range defs {
		set, own, _ := strings.Cut(d.Name, agentapi.ToolPrefixSeparator)
		src, err := sets[set].List(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(src, func(s tool.Definition) bool { return s.Name == own })
		if i < 0 {
			t.Fatalf("%s: no tool %q in toolset %q", d.Name, own, set)
		}
		if src[i].Kind == "" {
			t.Fatalf("toolset %q's %s is unclassified; the check needs metadata to compare", set, own)
		}
		d.Name = own
		if !reflect.DeepEqual(d, src[i]) {
			t.Errorf("merged %s%s%s = %+v, want the toolset's %+v", set, agentapi.ToolPrefixSeparator, own, d, src[i])
		}
	}
	m := script(t,
		llmtest.ToolCalls(call("c1", "a__echo", `{"text":"hi"}`), call("c2", "b__echo", `{"text":"hi"}`)),
		llmtest.Text("Done."),
	)
	r := newRig(t, m, rigConfig{tools: merged})
	s := r.run("echo hi in both", "")
	results := map[string]string{}
	for _, e := range s.events {
		if e.Type == agentapi.TypeToolResult {
			results[e.ToolUseID] = e.Content
		}
	}
	if results["c1"] != `{"text":"[a] hi"}` || results["c2"] != `{"text":"[b] hi"}` {
		t.Errorf("tool results %v, want each toolset's own echo", results)
	}
	if tools := m.requests()[0].Tools; len(tools) != 6 {
		t.Errorf("the model was offered %d tools, want 6", len(tools))
	}

	// Two different toolsets without a clash merge without prefixes.
	one := registry(t, waitTool(t, new(atomic.Int32)))
	if _, err := agentapi.MergeTools(t.Context(), map[string]tool.Registry{"a": sets["a"], "w": one}, false); err != nil {
		t.Errorf("merge without a clash: %v", err)
	}
}

// graceRig is a rig whose server reports each reconnect grace it starts on
// the returned channel.
func graceRig(t *testing.T, m *model, cfg rigConfig) (*rig, chan string) {
	armed := make(chan string, 16)
	cfg.api = append(cfg.api, agentapi.WithGraceHook(func(id string) { armed <- id }))
	return newRig(t, m, cfg), armed
}

// TestClientDisconnect: a chat run outlives its stream for the reconnect
// grace (T100). A client that comes back within it follows the run to its
// end; past it the run is interrupted, keeping its completed turns, and no
// model or tool call happens after that. This replaces T036's "a client
// that goes away cancels its run" (owner, 2026-10-13).
func TestClientDisconnect(t *testing.T) {
	t.Parallel()
	t.Run("past_the_grace_mid_tool", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		m := script(t, llmtest.ToolCalls(call("c1", "wait", `{"text":"x"}`)), llmtest.Text("never sent"))
		r, armed := graceRig(t, m, rigConfig{tools: registry(t, waitTool(t, &calls))})
		ctx, cancel := context.WithCancel(t.Context())
		s := r.start(ctx, "wait", "")
		s.until(agentapi.TypeToolCall, 1)
		s.abandon(cancel)
		<-armed
		if run := r.getRun(s.runID()); run.Status != agentapi.StatusRunning {
			t.Errorf("run within the grace = %s, want running", run.Status)
		}
		r.clock.Advance(agentapi.DefaultReconnectGrace)
		run := r.waitEnded(s.runID())
		if run.Status != agentapi.StatusInterrupted || run.Error == nil || run.Error.Code != agentapi.CodeRunInterrupted {
			t.Errorf("run = %+v, want interrupted", run)
		}
		if n := len(m.requests()); n != 1 || calls.Load() != 1 {
			t.Errorf("%d model calls and %d tool calls, want 1 each", n, calls.Load())
		}
		c := r.getConversation(s.conversationID())
		if len(c.Messages) != 1 || c.Status != agentapi.StatusInterrupted {
			t.Errorf("conversation = %s, want only the prompt, interrupted", jsonOf(c))
		}
	})
	t.Run("past_the_grace_between_turns", func(t *testing.T) {
		t.Parallel()
		sets := demoTools(t)
		m := script(t, llmtest.ToolCalls(call("c1", "echo", `{"text":"x"}`)), llmtest.Text("never answered"))
		m.gate = make(chan struct{}, 1)
		m.gate <- struct{}{} // the first reply goes through; the second waits for ever
		r, armed := graceRig(t, m, rigConfig{tools: sets["a"]})
		ctx, cancel := context.WithCancel(t.Context())
		s := r.start(ctx, "echo x", "")
		s.until(agentapi.TypeTurnDone, 1)
		s.abandon(cancel)
		<-armed
		r.clock.Advance(agentapi.DefaultReconnectGrace)
		run := r.waitEnded(s.runID())
		if run.Status != agentapi.StatusInterrupted || len(run.Record.Turns) != 1 {
			t.Errorf("run = %+v, want interrupted after 1 completed turn", run)
		}
		if n := len(m.requests()); n > 2 {
			t.Errorf("%d model calls, want at most 2", n)
		}
		c := r.getConversation(s.conversationID())
		if len(c.Messages) != 3 || c.Status != agentapi.StatusInterrupted {
			t.Errorf("conversation = %s, want the prompt and turn 1 (reply, results), interrupted", jsonOf(c))
		}
	})
	t.Run("reconnect_within_the_grace", func(t *testing.T) {
		t.Parallel()
		sets := demoTools(t)
		m := script(t, llmtest.ToolCalls(call("c1", "echo", `{"text":"x"}`)), llmtest.Text("Done."))
		m.gate = make(chan struct{}, 1)
		m.gate <- struct{}{}
		r, armed := graceRig(t, m, rigConfig{tools: sets["a"]})
		ctx, cancel := context.WithCancel(t.Context())
		s := r.start(ctx, "echo x", "")
		last := s.until(agentapi.TypeTurnDone, 1)
		s.abandon(cancel)
		<-armed
		r.clock.Advance(agentapi.DefaultReconnectGrace - time.Second)
		back := r.follow(s.runID(), last.ID) // within the grace
		r.clock.Advance(2 * time.Second)     // past when the grace would have ended
		m.gate <- struct{}{}
		events := back.all()
		if events[0].Seq != last.Seq || events[0].ID == last.ID || back.last().Type != agentapi.TypeRunDone || back.last().Answer != "Done." {
			t.Errorf("reconnected stream %s, want the run from after %s to its answer", jsonOf(events), last.ID)
		}
		if run := r.getRun(s.runID()); run.Status != agentapi.StatusDone {
			t.Errorf("run = %s, want done", run.Status)
		}
	})
}

// TestStop: POST /v1/runs/{id}/stop interrupts a run at once, with its
// stream ending in run_error interrupted; resuming it sends the model the
// conversation as it was.
func TestStop(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}, 1)
	r := newRig(t, m, rigConfig{})
	s := r.start(t.Context(), "hi", "")
	s.until(agentapi.TypeRunStart, 0)
	var run agentapi.Run
	resp, body := r.do(http.MethodPost, "/v1/runs/"+s.runID()+"/stop", "")
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &run) != nil || run.Status != agentapi.StatusInterrupted {
		t.Fatalf("stop = %d %s, want 200 and the run interrupted", resp.StatusCode, body)
	}
	if e := s.all(); e[len(e)-1].Error == nil || e[len(e)-1].Error.Code != agentapi.CodeRunInterrupted {
		t.Errorf("stream ended with %+v, want run_error interrupted", e[len(e)-1])
	}
	if resp, body := r.do(http.MethodPost, "/v1/runs/"+s.runID()+"/stop", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("stopping a stopped run = %d %s, want 200", resp.StatusCode, body)
	}
	m.gate <- struct{}{}
	resumed := r.open(r.postPath(t.Context(), "/v1/runs/"+s.runID()+"/resume", ""))
	resumed.all()
	if e := resumed.last(); e.Type != agentapi.TypeRunDone || e.Answer != "echo: hi" || resumed.events[0].Seq == 0 {
		t.Errorf("resumed stream %s, want the rest of the run, answered", jsonOf(resumed.events))
	}
}

// TestResumeInterruptedConversation: a conversation whose run stopped after
// tool results is interrupted; an empty message resumes the same run, which
// sends the model the conversation as it was, and its stream goes on from
// where the run's events stopped. A new message instead replaces the
// interrupted run (T100), so it is never 409.
func TestResumeInterruptedConversation(t *testing.T) {
	t.Parallel()
	sets := demoTools(t)
	m := script(t, llmtest.ToolCalls(call("c1", "echo", `{"text":"x"}`)), llmtest.Text("Resumed answer."), llmtest.Text("Next."))
	r := newRig(t, m, rigConfig{tools: sets["a"], harness: []harness.Option{harness.WithMaxTurns(1)}})
	s := r.run("echo x", "")
	if e := s.last(); e.Type != agentapi.TypeRunError || e.Error.Code != agentapi.CodeBudgetExceeded {
		t.Fatalf("first run ended with %+v, want budget_exceeded", e)
	}
	conv := s.conversationID()
	if c := r.getConversation(conv); c.Status != agentapi.StatusInterrupted {
		t.Fatalf("conversation status %q", c.Status)
	}
	resumed := r.run("", conv)
	if resumed.runID() != s.runID() || resumed.events[0].Seq != len(s.events)-1 || resumed.last().Answer != "Resumed answer." {
		t.Errorf("resume = %s, want run %s going on from seq %d, answered", jsonOf(resumed.events), s.runID(), len(s.events)-1)
	}
	reqs := m.requests()
	if c := r.getConversation(conv); !jsonEqual(reqs[1].Messages, messagesOf(c)[:3]) || c.Status != agentapi.StatusIdle {
		t.Errorf("resumed request %s, want the interrupted conversation", jsonOf(reqs[1].Messages))
	}
	r.run("next", conv)
}

// TestNewMessageReplacesAnInterruptedRun: a message to a conversation whose
// run was interrupted starts a new run and cancels the old one.
func TestNewMessageReplacesAnInterruptedRun(t *testing.T) {
	t.Parallel()
	sets := demoTools(t)
	m := script(t, llmtest.ToolCalls(call("c1", "echo", `{"text":"x"}`)), llmtest.Text("Fresh."))
	r := newRig(t, m, rigConfig{tools: sets["a"], harness: []harness.Option{harness.WithMaxTurns(1)}})
	s := r.run("echo x", "")
	next := r.run("never mind", s.conversationID())
	if next.runID() == s.runID() || next.last().Answer != "Fresh." {
		t.Errorf("new message = %s, want a new run, answered", jsonOf(next.events))
	}
	if old := r.getRun(s.runID()); old.Status != agentapi.StatusCanceled {
		t.Errorf("replaced run = %s, want canceled", old.Status)
	}
}

func messagesOf(c agentapi.Conversation) []llm.Message {
	out := make([]llm.Message, len(c.Messages))
	for i, m := range c.Messages {
		out[i] = m.Message
	}
	return out
}

func errorOf(t *testing.T, resp *http.Response) (int, agentapi.Error) {
	t.Helper()
	defer resp.Body.Close()
	var body agentapi.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Error == nil || body.V != agentapi.WireVersion {
		t.Fatalf("status %d: not an error response: %v", resp.StatusCode, err)
	}
	return resp.StatusCode, *body.Error
}

// TestConcurrencyLimit: past the limit a run gets 503 with Retry-After and
// reaches no model; once a run ends, its slot takes a new one.
func TestConcurrencyLimit(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{})
	r := newRig(t, m, rigConfig{api: []agentapi.Option{agentapi.WithMaxConcurrentRuns(2)}})
	a := r.start(t.Context(), "a", "")
	b := r.start(t.Context(), "b", "")
	a.until(agentapi.TypeRunStart, 0)
	b.until(agentapi.TypeRunStart, 0)

	resp := r.post(t.Context(), runBody("c", ""), "")
	retry := resp.Header.Get("Retry-After")
	if code, e := errorOf(t, resp); code != http.StatusServiceUnavailable || e.Code != agentapi.CodeOverCapacity || retry == "" {
		t.Errorf("third run = %d %+v, Retry-After %q; want 503 over_capacity with Retry-After", code, e, retry)
	}
	if n := len(m.requests()); n > 2 {
		t.Errorf("%d model calls, want at most 2", n)
	}
	m.gate <- struct{}{} // one run answers, whichever took the value
	m.gate <- struct{}{}
	a.all()
	b.all()
	close(m.gate)
	if c := r.run("c", ""); c.last().Answer != "echo: c" {
		t.Errorf("run after the others ended = %+v", c.last())
	}
}

// TestConcurrentConversationsDoNotInterfere: runs of different conversations
// at the same time each see only their own history (and -race sees no race).
func TestConcurrentConversationsDoNotInterfere(t *testing.T) {
	t.Parallel()
	const convs, rounds = 8, 3
	r := newRig(t, echo(t), rigConfig{api: []agentapi.Option{agentapi.WithMaxConcurrentRuns(convs)}})
	ids := make([]string, convs)
	var wg sync.WaitGroup
	for i := range convs {
		wg.Go(func() {
			for j := range rounds {
				s := r.run(fmt.Sprintf("conv %d round %d", i, j), ids[i])
				ids[i] = s.conversationID()
			}
		})
	}
	wg.Wait()
	for i, id := range ids {
		var want []llm.Message
		for j := range rounds {
			msg := fmt.Sprintf("conv %d round %d", i, j)
			want = append(want, harness.UserText(msg), llm.Assistant(llm.Text{Text: "echo: " + msg}))
		}
		if got := messagesOf(r.getConversation(id)); !jsonEqual(got, want) {
			t.Errorf("conversation %d = %s", i, jsonOf(got))
		}
	}
}

// TestTokenRequiredOnEveryPath: with a token, a missing or wrong one is 401
// on every /v1 path and method, before anything else happens.
func TestTokenRequiredOnEveryPath(t *testing.T) {
	t.Parallel()
	m := echo(t)
	r := newRig(t, m, rigConfig{token: testToken})
	conv := r.run("hello", "").conversationID()
	paths := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/runs", runBody("hi", "")},
		{http.MethodPost, "/v1/runs", runBody("hi", conv)},
		{http.MethodGet, "/v1/runs/run_1", ""},
		{http.MethodGet, "/v1/conversations/" + conv, ""},
		{http.MethodGet, "/v1/nothing", ""},
		{http.MethodDelete, "/v1/runs/run_1", ""},
		{http.MethodGet, "/v1/", ""},
	}
	headers := map[string]string{
		"missing":      "",
		"wrong":        "Bearer " + testToken + "x",
		"prefix":       "Bearer " + testToken[:len(testToken)-1],
		"other scheme": "Basic " + testToken,
		"no scheme":    testToken,
	}
	for _, p := range paths {
		for name, h := range headers {
			req, err := http.NewRequestWithContext(t.Context(), p.method, r.ts.URL+p.path, strings.NewReader(p.body))
			if err != nil {
				t.Fatal(err)
			}
			if h != "" {
				req.Header.Set("Authorization", h)
			}
			resp, err := r.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if code, e := errorOf(t, resp); code != http.StatusUnauthorized || e.Code != agentapi.CodeUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
				t.Errorf("%s %s with %s token = %d %+v, want 401", p.method, p.path, name, code, e)
			}
		}
	}
	if n := len(m.requests()); n != 1 {
		t.Errorf("%d model calls, want only the authorized run's", n)
	}
	if _, err := agentapi.New(newHarness(t), agentapi.WithStore(scoped.Memory(nil)), agentapi.WithToken("short")); !errors.Is(err, agentapi.ErrInvalidConfig) {
		t.Errorf("a short token: %v, want ErrInvalidConfig", err)
	}
	if _, err := agentapi.New(newHarness(t)); !errors.Is(err, agentapi.ErrInvalidConfig) {
		t.Errorf("no store: %v, want ErrInvalidConfig", err)
	}
}

func newHarness(t *testing.T) *harness.Harness {
	t.Helper()
	h, err := harness.New(echo(t), registry(t))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestRequestErrors: requests that cannot start a run get a typed error and
// reach no model.
func TestRequestErrors(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{})
	r := newRig(t, m, rigConfig{api: []agentapi.Option{agentapi.WithMaxBodyBytes(100)}})
	busy := r.start(t.Context(), "busy", "")
	conv := busy.until(agentapi.TypeRunStart, 0).ConversationID
	for name, tc := range map[string]struct {
		body string
		code int
		want string
	}{
		"not JSON":              {`hello`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"unknown field":         {`{"message":"hi","mesage":"x"}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"trailing data":         {`{"message":"hi"} {}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"wrong type":            {`{"message":42}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"empty message":         {`{"message":"  "}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"no message":            {`{}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"too large":             {`{"message":"` + strings.Repeat("x", 200) + `"}`, http.StatusRequestEntityTooLarge, agentapi.CodeTooLarge},
		"unknown conversation":  {runBody("hi", "conv_nope"), http.StatusNotFound, agentapi.CodeNotFound},
		"busy conversation":     {runBody("hi", conv), http.StatusConflict, agentapi.CodeBusy},
		"resume a running conv": {runBody("", conv), http.StatusConflict, agentapi.CodeBusy},
	} {
		resp := r.post(t.Context(), tc.body, "")
		code, e := errorOf(t, resp)
		if code != tc.code || e.Code != tc.want {
			t.Errorf("%s: %d %+v, want %d %s", name, code, e, tc.code, tc.want)
		}
		if code == http.StatusConflict && e.RunID != busy.runID() {
			t.Errorf("%s: run_id %q, want the busy run %s", name, e.RunID, busy.runID())
		}
	}
	for path, want := range map[string]int{"/v1/runs/run_nope": http.StatusNotFound, "/v1/conversations/conv_nope": http.StatusNotFound, "/v1/other": http.StatusNotFound} {
		var body agentapi.ErrorResponse
		if code := r.get(path, &body); code != want || body.Error == nil || body.Error.Code != agentapi.CodeNotFound {
			t.Errorf("GET %s = %d %+v", path, code, body.Error)
		}
	}
	close(m.gate)
	busy.all()
	if n := len(m.requests()); n != 1 {
		t.Errorf("%d model calls, want 1", n)
	}
}

// TestRunErrorsAreTyped: each way a run fails ends its stream with a
// run_error of its own code.
func TestRunErrorsAreTyped(t *testing.T) {
	t.Parallel()
	refusal := llmtest.Text("I can't.")
	refusal.Response.Stop = llm.StopReason{Kind: llm.StopRefusal, Category: "cyber", Explanation: "policy"}
	cut := llmtest.Text("Half an ans")
	cut.Response.Stop = llm.StopReason{Kind: llm.StopMaxTokens}
	for name, tc := range map[string]struct {
		reply     llmtest.Reply
		code      string
		retryable bool
	}{
		"refused":      {refusal, agentapi.CodeRefused, false},
		"max tokens":   {cut, agentapi.CodeMaxTokens, false},
		"rate limited": {llmtest.Fail(fmt.Errorf("%w: slow down", llm.ErrRateLimited)), agentapi.CodeRateLimited, true},
		"overloaded":   {llmtest.Fail(llm.ErrOverloaded), agentapi.CodeOverloaded, true},
		"auth":         {llmtest.Fail(llm.ErrAuth), agentapi.CodeModelAuth, false},
		"too long":     {llmtest.Fail(llm.ErrContextTooLong), agentapi.CodeContextTooLong, false},
		"rejected":     {llmtest.Fail(llm.ErrInvalidRequest), agentapi.CodeModelRejected, false},
		"other":        {llmtest.Fail(errors.New("boom")), agentapi.CodeInternal, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, script(t, tc.reply), rigConfig{})
			e := r.run("hi", "").last()
			if e.Type != agentapi.TypeRunError || e.Error.Code != tc.code || e.Error.Retryable != tc.retryable {
				t.Errorf("last event %+v, want run_error %s (retryable %v)", e.Error, tc.code, tc.retryable)
			}
			if tc.code == agentapi.CodeRefused && (e.Error.Category != "cyber" || e.Error.Explanation != "policy") {
				t.Errorf("refusal %+v", e.Error)
			}
			if tc.code == agentapi.CodeInternal && strings.Contains(e.Error.Message, "boom") {
				t.Errorf("internal error message shows the cause: %q", e.Error.Message)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		m := echo(t)
		m.gate = make(chan struct{})
		// Within the runner's lease (30s), so the run times out with its
		// lease held; the timers are the run timeout and the lease's renewal.
		r := newRig(t, m, rigConfig{api: []agentapi.Option{agentapi.WithRunTimeout(20 * time.Second)}})
		s := r.start(t.Context(), "hi", "")
		s.until(agentapi.TypeRunStart, 0)
		if err := r.clock.WaitForTimers(t.Context(), 2); err != nil {
			t.Fatal(err)
		}
		r.clock.Advance(20 * time.Second)
		s.all()
		if e := s.last(); e.Error == nil || e.Error.Code != agentapi.CodeTimeout {
			t.Errorf("last event %+v, want run_error timeout", e)
		}
	})
}

// TestRetention: past the retention, the server lets go of a finished run's
// events, and the run is replayed from the store: it reads the same, its
// text in one delta per block. Runs and conversations stay in the store,
// whatever the bounds.
func TestRetention(t *testing.T) {
	t.Parallel()
	r := newRig(t, echo(t), rigConfig{api: []agentapi.Option{agentapi.WithRetention(2, 1, time.Hour)}})
	long := "a message long enough to stream in several deltas"
	var runs []agentapi.Run
	for range 3 {
		runs = append(runs, r.runJSON(long, ""))
	}
	deltas := func(id string) int {
		n := 0
		for _, e := range r.events(id, "") {
			if e.Type == agentapi.TypeTextDelta {
				n++
			}
		}
		return n
	}
	if n := deltas(runs[2].ID); n < 2 {
		t.Fatalf("a kept run has %d text deltas, want its live ones", n)
	}
	// The last run joins the finished ones just after its response is
	// written, so the oldest is let go of a moment later.
	n := deltas(runs[0].ID)
	for deadline := time.Now().Add(10 * time.Second); n != 1 && time.Now().Before(deadline); n = deltas(runs[0].ID) {
		time.Sleep(time.Millisecond)
	}
	if n != 1 {
		t.Errorf("the oldest run, past the count, has %d text deltas, want 1 (replayed)", n)
	}
	r.clock.Advance(2 * time.Hour)
	if n := deltas(runs[2].ID); n != 1 {
		t.Errorf("a run past the age has %d text deltas, want 1 (replayed)", n)
	}
	for _, run := range runs {
		if got := r.getRun(run.ID); !jsonEqual(got, run) {
			t.Errorf("replayed run = %s, want %s", jsonOf(got), jsonOf(run))
		}
		r.getConversation(run.ConversationID)
	}
	if n := len(r.listAll()); n != len(runs) {
		t.Errorf("%d conversations listed, want %d", n, len(runs))
	}
}

// TestDrainEndsRunsWithShutdown: once the drain begins no run starts; at the
// drain deadline a run still going ends with a run_error of code shutdown
// that its client receives, the conversation keeps its turns, and Shutdown
// reports the run it cut.
func TestDrainEndsRunsWithShutdown(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{})
	stop := make(chan struct{})
	r := newRig(t, m, rigConfig{api: []agentapi.Option{agentapi.WithDrain(stop, 10*time.Second)}})
	s := r.start(t.Context(), "slow", "")
	s.until(agentapi.TypeRunStart, 0)
	close(stop)
	if err := r.clock.WaitForTimers(t.Context(), 2); err != nil { // the drain's and the lease's renewal
		t.Fatal(err)
	}
	resp := r.post(t.Context(), runBody("late", ""), "")
	if code, e := errorOf(t, resp); code != http.StatusServiceUnavailable || e.Code != agentapi.CodeShuttingDown {
		t.Errorf("run during the drain = %d %+v, want 503 shutting_down", code, e)
	}
	r.clock.Advance(10 * time.Second)
	s.all()
	if e := s.last(); e.Type != agentapi.TypeRunError || e.Error.Code != agentapi.CodeShutdown {
		t.Errorf("last event %+v, want run_error shutdown", e)
	}
	err := r.srv.Shutdown(t.Context())
	if !errors.Is(err, agentapi.ErrRunsCutOff) || !strings.Contains(err.Error(), s.runID()) {
		t.Errorf("Shutdown = %v, want ErrRunsCutOff naming %s", err, s.runID())
	}
}

// TestShutdownAfterCleanDrain: runs that finished during the drain make
// Shutdown return nil.
func TestShutdownAfterCleanDrain(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{})
	stop := make(chan struct{})
	r := newRig(t, m, rigConfig{api: []agentapi.Option{agentapi.WithDrain(stop, 10*time.Second)}})
	s := r.start(t.Context(), "slow", "")
	s.until(agentapi.TypeRunStart, 0)
	close(stop)
	close(m.gate)
	s.all()
	if e := s.last(); e.Type != agentapi.TypeRunDone {
		t.Errorf("last event %+v, want run_done", e)
	}
	if err := r.srv.Shutdown(t.Context()); err != nil {
		t.Errorf("Shutdown = %v", err)
	}
}

// TestShutdownThroughTheRuntime: the agent mounted in a component runtime and
// served on a real listener, stopped while a run streams. The runtime drains
// requests, so the stream goes on: it either finishes (exit clean) or, at the
// drain deadline, ends with run_error shutdown, which Serve reports.
func TestShutdownThroughTheRuntime(t *testing.T) {
	t.Parallel()
	for _, finish := range []bool{true, false} {
		t.Run(map[bool]string{true: "finishes", false: "cut"}[finish], func(t *testing.T) {
			t.Parallel()
			m := echo(t)
			m.gate = make(chan struct{})
			sig, signal := context.WithCancel(context.Background())
			defer signal()
			r := newRig(t, m, rigConfig{api: []agentapi.Option{agentapi.WithDrain(sig.Done(), 5*time.Second)}})
			rt, err := component.New([]component.Mount{{Pattern: "/v1/", Component: r.srv}}, component.WithShutdownTimeout(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			served := make(chan error, 1)
			go func() { served <- rt.Serve(sig, ln) }()
			r.url = "http://" + ln.Addr().String()

			s := r.start(t.Context(), "slow", "")
			s.until(agentapi.TypeRunStart, 0)
			signal()
			testkit.Eventually(t, 30*time.Second, func() bool { // the listener is closed: the drain has begun
				conn, err := net.Dial("tcp", ln.Addr().String())
				if err == nil {
					_ = conn.Close()
				}
				return err != nil
			})
			if finish {
				close(m.gate)
			} else {
				if err := r.clock.WaitForTimers(t.Context(), 2); err != nil { // the drain's and the lease's renewal
					t.Fatal(err)
				}
				r.clock.Advance(5 * time.Second)
			}
			s.all()
			err = <-served
			switch e := s.last(); {
			case finish && (e.Type != agentapi.TypeRunDone || err != nil):
				t.Errorf("stream ended with %s, Serve = %v; want run_done and nil", e.Type, err)
			case !finish && (e.Error == nil || e.Error.Code != agentapi.CodeShutdown || !errors.Is(err, agentapi.ErrRunsCutOff)):
				t.Errorf("stream ended with %+v, Serve = %v; want run_error shutdown and ErrRunsCutOff", e, err)
			}
		})
	}
}
