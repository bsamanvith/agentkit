package agentapi_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// surfaceInput is a ui_render input: a surface "news" with a choice and a
// button, and its data. With dangling set, the column names a component
// that is not defined.
func surfaceInput(dangling bool) json.RawMessage {
	children := `["title", "topic", "go"]`
	if dangling {
		children = `["title", "topic", "go", "missing"]`
	}
	return json.RawMessage(`{"messages": [
	  {"version": "v0.9", "createSurface": {"surfaceId": "news", "catalogId": "` + a2ui.BasicCatalogID + `"}},
	  {"version": "v0.9", "updateComponents": {"surfaceId": "news", "components": [
	    {"id": "root", "component": "Card", "child": "col"},
	    {"id": "col", "component": "Column", "children": ` + children + `},
	    {"id": "title", "component": "Text", "text": "Narrow the news search", "variant": "h3"},
	    {"id": "topic", "component": "ChoicePicker", "options": [{"label": "Go", "value": "go"}, {"label": "Rust", "value": "rust"}], "value": {"path": "/topic"}},
	    {"id": "go_label", "component": "Text", "text": "Search"},
	    {"id": "go", "component": "Button", "child": "go_label", "action": {"event": {"name": "search", "context": {"topic": {"path": "/topic"}}}}}
	  ]}},
	  {"version": "v0.9", "updateDataModel": {"surfaceId": "news", "path": "/topic", "value": ["go"]}}
	]}`)
}

func renderCall(id string, dangling bool) llm.ToolUse {
	return llm.ToolUse{ID: id, Name: a2ui.ToolName, Input: surfaceInput(dangling)}
}

// uiRig serves "painter", an agent with UI and the ui_render tool on the
// model m, and "plain", an agent without UI whose tools include a tool of
// the same name, on plain.
func uiRig(t *testing.T, m, plain *model) *rig {
	t.Helper()
	tools := registry(t, a2ui.Tool())
	r := newRig(t, m, rigConfig{agents: []agentapi.Agent{
		{Name: "painter", UI: a2ui.UIMode, Harness: agentHarness(t, m, tools)},
		{Name: "plain", Harness: agentHarness(t, plain, tools)},
	}})
	r.models = []*model{m, plain}
	return r
}

// TestUIEvents: the fake model calls ui_render with a dangling child, gets a
// tool error that names it, and calls again corrected; the stream has a ui
// event per message of the corrected call, right after its tool_result
// and before its turn's turn_done, and none for the failed call. An agent
// without UI streams none, whatever its tools.
func TestUIEvents(t *testing.T) {
	t.Parallel()
	m := script(t,
		llmtest.ToolCalls(renderCall("u1", true)),
		llmtest.ToolCalls(renderCall("u2", false)),
		llmtest.Text("Pick a topic above."),
	)
	plain := script(t, llmtest.ToolCalls(renderCall("p1", false)), llmtest.Text("done"))
	r := uiRig(t, m, plain)

	s := r.startAgent(t.Context(), "Find news", "", "painter")
	evs := s.all()
	want := []string{
		"run_start", "tool_call", "tool_result", "turn_done",
		"tool_call", "tool_result", "ui", "ui", "ui", "turn_done",
		"turn_done", "run_done",
	}
	if got := types(withoutText(evs)); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if bad := evs[2]; !bad.IsError || !strings.Contains(bad.Content, "/messages/1/updateComponents/components/1/children/3") {
		t.Errorf("the dangling call's result = %+v, want an error naming the child", bad)
	}
	if reqs := m.requests(); len(reqs) != 3 || !slices.ContainsFunc(reqs[1].Messages[len(reqs[1].Messages)-1].Content, func(b llm.Block) bool {
		res, ok := b.(llm.ToolResult)
		return ok && res.IsError && res.ToolUseID == "u1"
	}) {
		t.Errorf("the model was not shown the error before its retry")
	}
	msgs, err := a2ui.ParseInput(surfaceInput(false))
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range evs[6:9] {
		if e.SurfaceID != "news" || e.Turn != 2 || e.ToolUseID != "u2" || !jsonEqual(e.UI, msgs[i]) {
			t.Errorf("ui event %d = %+v, want message %s", i, e, jsonOf(msgs[i]))
		}
	}

	// The ui events are stored (an ID of a live event has an N), and
	// following the finished run replays them.
	for _, e := range evs[6:9] {
		if _, n, ok := wire.ParseID(e.ID); !ok || n != 0 {
			t.Errorf("ui event %s is not stored", e.ID)
		}
	}
	if got := types(withoutText(r.events(s.runID(), ""))); !slices.Equal(got, want) {
		t.Errorf("the stored events %v, want %v", got, want)
	}

	var ag wire.Agent
	if code := r.get("/v1/agents/painter", &ag); code != http.StatusOK || ag.UI != a2ui.UIMode {
		t.Errorf("GET /v1/agents/painter = %d %+v, want ui %q", code, ag, a2ui.UIMode)
	}
	var plainAgent wire.Agent
	if code := r.get("/v1/agents/plain", &plainAgent); code != http.StatusOK || plainAgent.UI != "" {
		t.Errorf("GET /v1/agents/plain = %d %+v, want no ui", code, plainAgent)
	}
	other := r.startAgent(t.Context(), "Find news", "", "plain").all()
	if got := types(withoutText(other)); !slices.Equal(got, []string{"run_start", "tool_call", "tool_result", "turn_done", "turn_done", "run_done"}) {
		t.Errorf("an agent without UI streamed %v", got)
	}
}

// withoutText is evs without their text_delta events.
func withoutText(evs []agentapi.Event) []agentapi.Event {
	return slices.DeleteFunc(slices.Clone(evs), func(e agentapi.Event) bool { return e.Type == agentapi.TypeTextDelta })
}

// TestUIAction: a ui_action continues the conversation that showed the
// surface: the model gets it as the next user message, the action as
// structured text, and answers; the conversation holds every message, in
// order, and stays valid (the rig checks both).
func TestUIAction(t *testing.T) {
	t.Parallel()
	m := script(t,
		llmtest.ToolCalls(renderCall("u1", false)),
		llmtest.Text("Pick a topic above."),
		llmtest.Text("Searching Go news."),
	)
	r := uiRig(t, m, script(t))
	first := r.startAgent(t.Context(), "Find news", "", "painter")
	first.all()
	conv := first.conversationID()
	beforeConv := r.getConversation(conv)
	before := beforeConv.Messages

	body, _ := json.Marshal(agentapi.RunRequest{ConversationID: conv, UIAction: &agentapi.UIAction{
		SurfaceID: "news", ComponentID: "go", Action: "search", Context: json.RawMessage(`{"topic": ["go"]}`),
	}})
	s := r.open(r.post(t.Context(), string(body), ""))
	evs := s.all()
	if end := s.last(); end.Type != agentapi.TypeRunDone || end.Answer != "Searching Go news." {
		t.Fatalf("the action's run ended with %+v", end)
	}
	prompt := evs[0].Prompt
	if prompt == nil || prompt.Message.Role != llm.RoleUser {
		t.Fatalf("run_start's prompt = %+v, want the action as a user message", prompt)
	}
	a, ok := a2ui.ParseActionText(prompt.Message.Text())
	if !ok || a.Name != "search" || a.SurfaceID != "news" || a.SourceComponentID != "go" ||
		!a.Timestamp.Equal(r.clock.Now()) || string(a.Context) != `{"topic":["go"]}` {
		t.Errorf("the action's message %q reads as %+v, %v", prompt.Message.Text(), a, ok)
	}
	reqs := m.requests()
	last := reqs[len(reqs)-1].Messages
	if !jsonEqual(last[:len(before)], messagesOf(beforeConv)) || !jsonEqual(last[len(last)-1], prompt.Message) {
		t.Errorf("the model was not sent the conversation and then the action")
	}
	after := r.getConversation(conv).Messages
	if len(after) != len(before)+2 || !jsonEqual(after[:len(before)], before) {
		t.Errorf("the conversation went from %d to %d messages, or changed its earlier ones", len(before), len(after))
	}
}

// TestUIActionRefused: a ui_action that cannot be one is 400 and starts no
// run: with a message too, without a conversation, to an agent without UI,
// or with fields the action check refuses.
func TestUIActionRefused(t *testing.T) {
	t.Parallel()
	m := script(t, llmtest.Text("hello"))
	plain := script(t, llmtest.Text("hi"))
	r := uiRig(t, m, plain)
	painted := r.startAgent(t.Context(), "hi", "", "painter")
	painted.all()
	unpainted := r.startAgent(t.Context(), "hi", "", "plain")
	unpainted.all()
	ok := agentapi.UIAction{SurfaceID: "news", ComponentID: "go", Action: "search"}
	for _, tc := range []struct {
		name string
		req  agentapi.RunRequest
	}{
		{"with a message", agentapi.RunRequest{Message: "also this", ConversationID: painted.conversationID(), UIAction: &ok}},
		{"no conversation", agentapi.RunRequest{Agent: "painter", UIAction: &ok}},
		{"agent without UI", agentapi.RunRequest{ConversationID: unpainted.conversationID(), UIAction: &ok}},
		{"no action name", agentapi.RunRequest{ConversationID: painted.conversationID(), UIAction: &agentapi.UIAction{SurfaceID: "news", ComponentID: "go"}}},
		{"no surface", agentapi.RunRequest{ConversationID: painted.conversationID(), UIAction: &agentapi.UIAction{ComponentID: "go", Action: "search"}}},
		{"context not an object", agentapi.RunRequest{ConversationID: painted.conversationID(), UIAction: &agentapi.UIAction{
			SurfaceID: "news", ComponentID: "go", Action: "search", Context: json.RawMessage(`["go"]`),
		}}},
	} {
		body, _ := json.Marshal(tc.req)
		resp, b := r.do(http.MethodPost, "/v1/runs", string(body), "Accept", "application/json")
		var e agentapi.ErrorResponse
		if resp.StatusCode != http.StatusBadRequest || json.Unmarshal(b, &e) != nil || e.Error == nil || e.Error.Code != agentapi.CodeInvalidRequest {
			t.Errorf("%s: POST /v1/runs = %d %s, want 400 invalid_request", tc.name, resp.StatusCode, b)
		}
	}
	for _, conv := range []string{painted.conversationID(), unpainted.conversationID()} {
		if n := len(r.getConversation(conv).Messages); n != 2 {
			t.Errorf("conversation %s has %d messages after refused actions, want 2", conv, n)
		}
	}
}
