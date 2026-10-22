package agui_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agui"
)

// compact is the events' types with each run of TEXT_MESSAGE_CONTENT as
// one: how many pieces a text comes in is the model's business.
func compact(evs []agui.Event) []agui.EventType {
	var out []agui.EventType
	for _, t := range types(evs) {
		if t == agui.EventTextMessageContent && len(out) > 0 && out[len(out)-1] == t {
			continue
		}
		out = append(out, t)
	}
	return out
}

// sameJSON reports whether two JSON documents are the same value.
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}

// asInput turns reduced messages back into request messages, as a client
// sends its history (without its activity messages).
func asInput(tb testing.TB, msgs []message, more ...map[string]any) []map[string]any {
	tb.Helper()
	var out []map[string]any
	for _, m := range msgs {
		if m.Role == "activity" {
			continue
		}
		b, _ := json.Marshal(m)
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			tb.Fatal(err)
		}
		out = append(out, v)
	}
	return append(out, more...)
}

// TestRunWithToolCallAndSurface: a run of the UI agent streams its text,
// a tool call with its arguments and result, the ui_render call, the
// surface as an A2UI activity, and the answer, then RUN_FINISHED with the
// answer as its result; the run and its conversation are the agent API's.
func TestRunWithToolCallAndSurface(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	in := input("thread-1", user("u1", "show news"))
	evs := r.run("painter", keyA, in)
	want := []agui.EventType{
		agui.EventRunStarted,
		agui.EventTextMessageStart, agui.EventTextMessageContent, agui.EventTextMessageEnd,
		agui.EventToolCallStart, agui.EventToolCallArgs, agui.EventToolCallEnd, agui.EventToolCallResult,
		agui.EventToolCallStart, agui.EventToolCallArgs, agui.EventToolCallEnd, agui.EventToolCallResult,
		agui.EventActivitySnapshot,
		agui.EventTextMessageStart, agui.EventTextMessageContent, agui.EventTextMessageEnd,
		agui.EventRunFinished,
	}
	if got := compact(evs); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	conv, runID := bound(t, evs)

	msgs := reduce(t, in["messages"].([]map[string]any), evs)
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		roles[i] = m.Role
	}
	if want := []string{"user", "assistant", "tool", "assistant", "tool", "activity", "assistant"}; !slices.Equal(roles, want) {
		t.Fatalf("messages %v, want %v", roles, want)
	}
	first, echoed := msgs[1], msgs[2]
	if first.text() != "Let me check." || len(first.ToolCalls) != 1 || first.ToolCalls[0].Function.Name != "echo" || !sameJSON([]byte(first.ToolCalls[0].Function.Arguments), []byte(`{"text":"hi"}`)) {
		t.Errorf("the first turn %+v, want its text and the echo call", first)
	}
	if echoed.text() != `"hi"` || echoed.ToolCallID != first.ToolCalls[0].ID {
		t.Errorf("the echo result %+v", echoed)
	}
	render := msgs[3]
	if len(render.ToolCalls) != 1 || render.ToolCalls[0].Function.Name != a2ui.ToolName || !sameJSON([]byte(render.ToolCalls[0].Function.Arguments), []byte(surface)) {
		t.Errorf("the ui_render turn %+v", render)
	}
	activity := msgs[5]
	var content struct {
		Ops []json.RawMessage `json:"a2ui_operations"`
	}
	var sent struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal([]byte(surface), &sent)
	if err := json.Unmarshal(activity.Content, &content); err != nil || activity.ActivityType != agui.ActivityTypeA2UI ||
		activity.ID != "a2ui-surface-"+render.ToolCalls[0].ID || len(content.Ops) != len(sent.Messages) {
		t.Fatalf("activity %+v (%v), want the call's %d messages", activity, err, len(sent.Messages))
	}
	for i := range sent.Messages {
		if !sameJSON(content.Ops[i], sent.Messages[i]) {
			t.Errorf("A2UI message %d: %s, want %s", i, content.Ops[i], sent.Messages[i])
		}
	}
	if msgs[6].text() != "Pick a topic above." {
		t.Errorf("the answer %q", msgs[6].text())
	}
	for _, m := range msgs[1:] {
		if m.Role != "activity" && !strings.HasPrefix(m.ID, runID+"/") {
			t.Errorf("message ID %q does not name run %s", m.ID, runID)
		}
	}

	end := last(evs)
	var result string
	if err := json.Unmarshal(end.Result, &result); err != nil || result != "Pick a topic above." || end.Outcome.Type != agui.OutcomeSuccess {
		t.Errorf("RUN_FINISHED %+v, want success with the answer", end)
	}
	if len(end.Usage) != 1 || end.Usage[0].TotalTokens != end.Usage[0].InputTokens+end.Usage[0].OutputTokens {
		t.Errorf("usage %+v", end.Usage)
	}

	run := r.apiRun(keyA, runID)
	if run.Status != wire.StatusDone || run.ConversationID != conv || run.Agent != "painter" || run.Answer != "Pick a topic above." {
		t.Errorf("GET /v1/runs/%s = %+v", runID, run)
	}
	var list wire.ConversationList
	r.getJSON("/v1/conversations", keyA, &list)
	if !slices.ContainsFunc(list.Conversations, func(c wire.ConversationSummary) bool { return c.ID == conv && c.Agent == "painter" }) {
		t.Errorf("GET /v1/conversations = %+v, want %s", list, conv)
	}
}

// TestThreads: a request continues the conversation of the messages the
// server gave the client, or the one its threadId names; anything else
// starts a conversation. A thread is never another agent's or another
// tenant's conversation.
func TestThreads(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	in := input("t-1", user("u1", "hello"))
	first := r.run("plain", keyA, in)
	conv, _ := bound(t, first)
	history := reduce(t, in["messages"].([]map[string]any), first)

	// The same client thread, with the history: the same conversation.
	second := r.run("plain", keyA, input("t-1", asInput(t, history, user("u2", "again"))...))
	if c, _ := bound(t, second); c != conv {
		t.Errorf("the second run is in %s, want %s", c, conv)
	}
	if got := r.conversation(keyA, conv); len(got.Messages) != 4 {
		t.Errorf("conversation has %d messages, want 4", len(got.Messages))
	}

	// A threadId that is the conversation, with no history.
	third := r.run("plain", keyA, input(conv, user("u3", "and again")))
	if c, _ := bound(t, third); c != conv {
		t.Errorf("the run of thread %s is in %s", conv, c)
	}

	// A new thread with no message of ours: a new conversation.
	fresh := r.run("plain", keyA, input("t-2", user("x", "hi")))
	if c, _ := bound(t, fresh); c == conv {
		t.Error("a new thread continued an old conversation")
	}

	// The conversation of another agent.
	other := r.run("painter", keyA, input(conv, user("y", "hi")))
	if e := last(other); e.Type != agui.EventRunError || e.Code != agui.CodeInvalidInput {
		t.Errorf("another agent's conversation: %+v", e)
	}

	// Another tenant, naming tenant a's conversation and its messages.
	theirs := r.run("plain", keyB, input(conv, asInput(t, history, user("z", "hi"))...))
	if c, _ := bound(t, theirs); c == conv {
		t.Fatal("tenant b continued tenant a's conversation")
	}
	if code := r.getJSON("/v1/conversations/"+conv, keyB, nil); code != http.StatusNotFound {
		t.Errorf("tenant b reads tenant a's conversation: %d", code)
	}
	if got := r.conversation(keyA, conv); len(got.Messages) != 6 {
		t.Errorf("tenant a's conversation has %d messages, want 6: tenant b's went elsewhere", len(got.Messages))
	}
}

// TestUIAction: an action on a surface (forwardedProps.a2uiAction) is the
// agent API's ui_action on the thread's conversation, which the agent reads
// as the action's message.
func TestUIAction(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	in := input("t-ui", user("u1", "show news"))
	first := r.run("painter", keyA, in)
	conv, _ := bound(t, first)
	history := reduce(t, in["messages"].([]map[string]any), first)

	act := input("t-ui", asInput(t, history)...)
	act["forwardedProps"] = map[string]any{"a2uiAction": map[string]any{"userAction": map[string]any{
		"name": "search", "surfaceId": "news", "sourceComponentId": "go", "context": map[string]any{"topic": []string{"go"}},
	}}}
	evs := r.run("painter", keyA, act)
	if c, _ := bound(t, evs); c != conv {
		t.Errorf("the action ran in %s, want %s", c, conv)
	}
	end := last(evs)
	if end.Type != agui.EventRunFinished || string(end.Result) != `"You chose {\"topic\":[\"go\"]}."` {
		t.Fatalf("the action's run ended %+v", end)
	}
	msgs := r.conversation(keyA, conv).Messages
	a, ok := a2ui.ParseActionText(msgs[len(msgs)-2].Message.Text())
	if !ok || a.Name != "search" || a.SurfaceID != "news" || a.SourceComponentID != "go" || !sameJSON(a.Context, []byte(`{"topic":["go"]}`)) {
		t.Errorf("the conversation's action message %q", msgs[len(msgs)-2].Message.Text())
	}

	for name, tc := range map[string]struct {
		agent, thread string
		action        any
		code          string
	}{
		"no thread":       {"painter", "t-none", map[string]any{"name": "search", "surfaceId": "news", "sourceComponentId": "go"}, agui.CodeInvalidInput},
		"no name":         {"painter", conv, map[string]any{"surfaceId": "news"}, agui.CodeInvalidInput},
		"context a list":  {"painter", conv, map[string]any{"name": "search", "surfaceId": "news", "sourceComponentId": "go", "context": []int{1}}, agui.CodeInvalidInput},
		"an agent w/o UI": {"plain", "", map[string]any{"name": "search", "surfaceId": "news", "sourceComponentId": "go"}, wire.CodeInvalidRequest},
	} {
		thread := tc.thread
		if tc.agent == "plain" {
			p := r.run("plain", keyA, input("t-plain", user("p", "hello")))
			thread, _ = bound(t, p)
		}
		in := input(thread, user("u", "ignored"))
		in["forwardedProps"] = map[string]any{"a2uiAction": map[string]any{"userAction": tc.action}}
		if e := last(r.run(tc.agent, keyA, in)); e.Type != agui.EventRunError || e.Code != tc.code {
			t.Errorf("%s: %+v, want RUN_ERROR %s", name, e, tc.code)
		}
	}
}

// interrupts returns the interrupts of a stream that ended on them.
func interrupts(tb testing.TB, evs []agui.Event) []agui.Interrupt {
	tb.Helper()
	end := last(evs)
	if end.Type != agui.EventRunFinished || end.Outcome == nil || end.Outcome.Type != agui.OutcomeInterrupt {
		tb.Fatalf("the run ended %+v, want an interrupt", end)
	}
	return end.Outcome.Interrupts
}

// resume is a request that answers interrupts: entries of interruptId,
// status and payload.
func resume(thread string, history []map[string]any, entries ...map[string]any) map[string]any {
	in := input(thread, history...)
	in["resume"] = entries
	return in
}

func entry(id, status string, payload any) map[string]any {
	e := map[string]any{"interruptId": id, "status": status}
	if payload != nil {
		e["payload"] = payload
	}
	return e
}

// TestInterruptsAndResume: a run whose calls wait for people ends with an
// interrupt outcome, one interrupt per call (a question, an approval); the
// thread takes no new input until a resume answers every one; then the
// same run goes on, the answered calls' results first, without their start
// again.
func TestInterruptsAndResume(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	in := input("t-ask", user("u1", "both"))
	first := r.run("asker", keyA, in)
	conv, runID := bound(t, first)
	asked := interrupts(t, first)
	if len(asked) != 2 {
		t.Fatalf("interrupts %+v, want two", asked)
	}
	var question, approval agui.Interrupt
	for _, i := range asked {
		switch i.Reason {
		case agui.ReasonInputRequired:
			question = i
		case agui.ReasonToolCall:
			approval = i
		}
	}
	if question.Message != "which color?" || !strings.Contains(string(question.ResponseSchema), `"enum":["red","blue"]`) || question.ToolCallID == "" {
		t.Errorf("the question %+v", question)
	}
	if approval.Message != "notes need a yes" || !strings.Contains(string(approval.ResponseSchema), `"approved"`) || approval.ToolCallID == "" {
		t.Errorf("the approval %+v", approval)
	}
	for _, i := range asked {
		if !strings.HasPrefix(i.ID, runID+"/") {
			t.Errorf("interrupt ID %q does not name run %s", i.ID, runID)
		}
	}
	history := asInput(t, reduce(t, in["messages"].([]map[string]any), first))

	// Not yet: new input, half the answers, answers to what nobody asked,
	// answers of the wrong shape.
	for name, tc := range map[string]struct {
		in   map[string]any
		code string
	}{
		"new input":         {input("t-ask", append(slices.Clone(history), user("u2", "hello"))...), agui.CodePendingInterrupts},
		"half the answers":  {resume("t-ask", history, entry(question.ID, agui.ResumeResolved, map[string]any{"text": "red"})), agui.CodeInvalidInput},
		"not an interrupt":  {resume("t-ask", history, entry("nope", agui.ResumeResolved, true)), agui.CodeUnknownInterrupt},
		"another call":      {resume("t-ask", history, entry(runID+"/i/call_99", agui.ResumeResolved, true)), agui.CodeUnknownInterrupt},
		"no such run":       {resume("t-ask", history, entry("run_99/i/call_1", agui.ResumeResolved, true)), agui.CodeUnknownInterrupt},
		"a bad payload":     {resume("t-ask", history, entry(question.ID, agui.ResumeResolved, 3), entry(approval.ID, agui.ResumeResolved, true)), agui.CodeInvalidInput},
		"edited arguments":  {resume("t-ask", history, entry(question.ID, agui.ResumeResolved, "red"), entry(approval.ID, agui.ResumeResolved, map[string]any{"approved": true, "editedArgs": map[string]any{"s": "x"}})), agui.CodeUnsupported},
		"a bad status":      {resume("t-ask", history, entry(question.ID, "maybe", "red"), entry(approval.ID, agui.ResumeResolved, true)), agui.CodeInvalidInput},
		"answered twice":    {resume("t-ask", history, entry(question.ID, agui.ResumeResolved, "red"), entry(question.ID, agui.ResumeResolved, "red")), agui.CodeInvalidInput},
		"not one of choice": {resume("t-ask", history, entry(question.ID, agui.ResumeResolved, "green"), entry(approval.ID, agui.ResumeResolved, true)), wire.CodeInvalidRequest},
	} {
		if e := last(r.run("asker", keyA, tc.in)); e.Type != agui.EventRunError || e.Code != tc.code {
			t.Errorf("%s: %+v, want RUN_ERROR %s", name, e, tc.code)
		}
	}
	if run := r.apiRun(keyA, runID); run.Status != wire.StatusSuspended || r.notes.Load() != 0 {
		t.Fatalf("after refused resumes the run is %s and note ran %d times", run.Status, r.notes.Load())
	}

	answer := resume("t-ask", history,
		entry(question.ID, agui.ResumeResolved, map[string]any{"text": "red"}),
		entry(approval.ID, agui.ResumeResolved, map[string]any{"approved": true}))
	evs := r.run("asker", keyA, answer)
	if c, run := bound(t, evs); c != conv || run != runID {
		t.Errorf("the resume ran %s in %s, want the run %s in %s", run, c, runID, conv)
	}
	want := []agui.EventType{
		agui.EventRunStarted, agui.EventToolCallResult, agui.EventToolCallResult,
		agui.EventTextMessageStart, agui.EventTextMessageContent, agui.EventTextMessageEnd, agui.EventRunFinished,
	}
	if got := compact(evs); !slices.Equal(got, want) {
		t.Fatalf("the resumed run's events %v, want %v", got, want)
	}
	results := map[string]string{}
	for _, e := range evs {
		if e.Type == agui.EventToolCallResult {
			var s string
			_ = json.Unmarshal(e.Content, &s)
			results[e.ToolCallID] = s
		}
	}
	if results[question.ToolCallID] != "red" || results[approval.ToolCallID] != `"noted hi"` || r.notes.Load() != 1 {
		t.Errorf("results %v, note ran %d times", results, r.notes.Load())
	}
	if e := last(evs); e.Outcome.Type != agui.OutcomeSuccess || string(e.Result) != `"They said red and \"noted hi\"."` {
		t.Errorf("the resumed run ended %+v", e)
	}
	// The same resume again does nothing again.
	if e := last(r.run("asker", keyA, answer)); e.Type != agui.EventRunError || e.Code != agui.CodeUnknownInterrupt || r.notes.Load() != 1 {
		t.Errorf("the resume again: %+v, note ran %d times", e, r.notes.Load())
	}
}

// TestCancelledInterrupts: a cancelled approval is a denial, and the run
// goes on; a cancelled question cancels the run.
func TestCancelledInterrupts(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	note := r.run("asker", keyA, input("t-note", user("u", "note")))
	asked := interrupts(t, note)
	evs := r.run("asker", keyA, resume("t-note", nil, entry(asked[0].ID, agui.ResumeCancelled, nil)))
	end := last(evs)
	if end.Type != agui.EventRunFinished || end.Outcome.Type != agui.OutcomeSuccess || r.notes.Load() != 0 {
		t.Fatalf("a cancelled approval: %+v, note ran %d times", end, r.notes.Load())
	}
	for _, e := range evs {
		if e.Type == agui.EventToolCallResult && !strings.Contains(string(e.Content), "cancelled by the user") {
			t.Errorf("the denied call's result %s", e.Content)
		}
	}

	ask := r.run("asker", keyA, input("t-q", user("u", "ask")))
	_, runID := bound(t, ask)
	asked = interrupts(t, ask)
	end = last(r.run("asker", keyA, resume("t-q", nil, entry(asked[0].ID, agui.ResumeCancelled, nil))))
	if end.Type != agui.EventRunFinished || end.Outcome.Type != agui.OutcomeCancelled {
		t.Errorf("a cancelled question: %+v", end)
	}
	if run := r.apiRun(keyA, runID); run.Status != wire.StatusCanceled {
		t.Errorf("the run is %s, want canceled", run.Status)
	}
}

// TestRequestErrors: a request the server cannot take at all is an HTTP
// error with the agent API's error body; one it cannot run is a run that
// fails.
func TestRequestErrors(t *testing.T) {
	t.Parallel()
	r := newRig(t, agui.WithMaxBodyBytes(2048))
	good := input("t", user("u", "hello"))
	for name, tc := range map[string]struct {
		agent, key, method string
		body               any
		status             int
		code               string
	}{
		"no credential":    {"plain", "", http.MethodPost, good, http.StatusUnauthorized, wire.CodeUnauthorized},
		"another key":      {"plain", "agui-test-key-unknown-0000000000", http.MethodPost, good, http.StatusUnauthorized, wire.CodeUnauthorized},
		"no such agent":    {"nobody", keyA, http.MethodPost, good, http.StatusNotFound, wire.CodeNotFound},
		"a path under":     {"plain/x", keyA, http.MethodPost, good, http.StatusNotFound, wire.CodeNotFound},
		"not JSON":         {"plain", keyA, http.MethodPost, "{", http.StatusBadRequest, wire.CodeInvalidRequest},
		"no threadId":      {"plain", keyA, http.MethodPost, `{"runId":"r","messages":[]}`, http.StatusBadRequest, wire.CodeInvalidRequest},
		"no messages":      {"plain", keyA, http.MethodPost, `{"threadId":"t","runId":"r"}`, http.StatusBadRequest, wire.CodeInvalidRequest},
		"version 2":        {"plain", keyA, http.MethodPost, `{"threadId":"t","runId":"r","messages":[],"protocolVersion":"2.0"}`, http.StatusBadRequest, wire.CodeInvalidRequest},
		"too large":        {"plain", keyA, http.MethodPost, input("t", user("u", strings.Repeat("x", 4096))), http.StatusBadRequest, wire.CodeInvalidRequest},
		"GET":              {"plain", keyA, http.MethodGet, nil, http.StatusMethodNotAllowed, wire.CodeInvalidRequest},
		"no agent in path": {"", keyA, http.MethodPost, good, http.StatusNotFound, wire.CodeNotFound},
	} {
		var body []byte
		switch b := tc.body.(type) {
		case string:
			body = []byte(b)
		case nil:
		default:
			body, _ = json.Marshal(b)
		}
		req, _ := http.NewRequest(tc.method, r.url(agui.PathPrefix+tc.agent), strings.NewReader(string(body)))
		if tc.key != "" {
			req.Header.Set("Authorization", "Bearer "+tc.key)
		}
		resp, err := r.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var e wire.ErrorResponse
		err = json.NewDecoder(resp.Body).Decode(&e)
		resp.Body.Close()
		if resp.StatusCode != tc.status || err != nil || e.Error == nil || e.Error.Code != tc.code {
			t.Errorf("%s: %d %+v (%v), want %d %s", name, resp.StatusCode, e.Error, err, tc.status, tc.code)
		}
		if tc.status == http.StatusUnauthorized && !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("%s: WWW-Authenticate %q", name, resp.Header.Get("WWW-Authenticate"))
		}
	}

	for name, tc := range map[string]struct {
		key  string
		msgs []map[string]any
		code string
	}{
		"no message":         {keyA, nil, agui.CodeInvalidInput},
		"an assistant's":     {keyA, []map[string]any{user("u", "hi"), {"id": "a", "role": "assistant", "content": "hello"}}, agui.CodeInvalidInput},
		"a frontend tool's":  {keyA, []map[string]any{{"id": "t", "role": "tool", "toolCallId": "c", "content": "42"}}, agui.CodeInvalidInput},
		"an image":           {keyA, []map[string]any{{"id": "u", "role": "user", "content": []any{map[string]any{"type": "image", "source": map[string]any{"type": "url", "value": "https://example.com/a.png"}}}}}, agui.CodeUnsupported},
		"empty text":         {keyA, []map[string]any{user("u", "  ")}, agui.CodeInvalidInput},
		"no agent:run scope": {keyRead, []map[string]any{user("u", "hi")}, wire.CodeForbidden},
	} {
		if e := last(r.run("plain", tc.key, input("t-"+name, tc.msgs...))); e.Type != agui.EventRunError || e.Code != tc.code {
			t.Errorf("%s: %+v, want RUN_ERROR %s", name, e, tc.code)
		}
	}
	// Text parts are the prompt, joined by new lines.
	parts := map[string]any{"id": "u", "role": "user", "content": []any{map[string]any{"type": "text", "text": "a"}, map[string]any{"type": "text", "text": "b"}}}
	if e := last(r.run("plain", keyA, input("t-parts", parts))); string(e.Result) != `"echo: a\nb"` {
		t.Errorf("text parts: %+v", e)
	}
}

// TestRunError: a run that fails is RUN_ERROR with the agent API's code.
func TestRunError(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	e := last(r.run("plain", keyA, input("t", user("u", "fail"))))
	if e.Type != agui.EventRunError || e.Code != wire.CodeModelRejected || e.Message == "" {
		t.Errorf("a failed run ended %+v", e)
	}
}

// TestCancelledRun: a run canceled through the agent API while an AG-UI
// client follows it ends with outcome cancelled.
func TestCancelledRun(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	in := input("t-wait", user("u", "wait for it"))
	body, _ := json.Marshal(in)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, r.url(agui.PathPrefix+"plain"), strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+keyA)
	resp, err := r.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n') // RUN_STARTED, sent once the run is
	if err != nil {
		t.Fatal(err)
	}
	var started agui.Event
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(line), "data: ")), &started); err != nil {
		t.Fatal(err)
	}
	_, runID := bound(t, []agui.Event{started})
	<-r.model.waiting // the model is called: the run is under way
	creq, _ := http.NewRequest(http.MethodPost, r.url("/v1/runs/"+runID+"/cancel"), nil)
	creq.Header.Set("Authorization", "Bearer "+keyA)
	cresp, err := r.ts.Client().Do(creq)
	if err != nil {
		t.Fatal(err)
	}
	if cresp.StatusCode != http.StatusOK {
		t.Errorf("cancel = %d", cresp.StatusCode)
	}
	cresp.Body.Close()
	raw := append([]json.RawMessage{json.RawMessage(strings.TrimPrefix(strings.TrimSpace(line), "data: "))}, readSSE(t, br)...)
	evs := decode(t, raw)
	r.mu.Lock()
	r.streams = append(r.streams, recorded{in: in, events: evs, raw: raw})
	r.mu.Unlock()
	if e := last(evs); e.Type != agui.EventRunFinished || e.Outcome.Type != agui.OutcomeCancelled {
		t.Errorf("the canceled run ended %+v", e)
	}
}

// TestShutdownRefuses: after Shutdown, requests are refused.
func TestShutdownRefuses(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if err := r.srv.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	status, evs, _ := r.post("plain", keyA, input("t", user("u", "hi")))
	if status != http.StatusServiceUnavailable || evs != nil {
		t.Errorf("after Shutdown: %d", status)
	}
}

// TestNew refuses what it cannot use.
func TestNew(t *testing.T) {
	t.Parallel()
	h := http.NotFoundHandler()
	for name, tc := range map[string]struct {
		api  http.Handler
		opts []agui.Option
	}{
		"no API":        {nil, nil},
		"a zero limit":  {h, []agui.Option{agui.WithMaxBodyBytes(0)}},
		"a negative KA": {h, []agui.Option{agui.WithKeepAlive(-time.Second)}},
		"no clock":      {h, []agui.Option{agui.WithClock(nil)}},
		"no logger":     {h, []agui.Option{agui.WithLogger(nil)}},
	} {
		if _, err := agui.New(tc.api, tc.opts...); !errors.Is(err, agui.ErrInvalidConfig) {
			t.Errorf("%s: %v, want ErrInvalidConfig", name, err)
		}
	}
}

// TestServerUsesNotTheAgentAPIServer: the package reaches runs through the
// agent API's client only, never its server or the harness.
func TestServerUsesNotTheAgentAPIServer(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		switch dep {
		case "github.com/bsamanvith/agentkit/pkg/agentapi", "github.com/bsamanvith/agentkit/pkg/harness", "github.com/bsamanvith/agentkit/pkg/a2ui":
			t.Errorf("pkg/agui depends on %s", dep)
		}
	}
}
