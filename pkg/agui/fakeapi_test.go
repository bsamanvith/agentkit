package agui_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agui"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

// The paths a real agent API only takes on a bad day (a retry, a rewind,
// deltas a follower missed), served by a fake agent API that streams a
// scripted run.

// fakeAPI is an agent API with one agent, "agent", whose every run streams
// events; it knows no conversation and no run.
type fakeAPI struct {
	events []wire.Event
}

const (
	fakeRun  = "run_f"
	fakeConv = "conv_f"
)

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/agents/"):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(wire.Agent{V: wire.Version, Name: strings.TrimPrefix(r.URL.Path, "/v1/agents/"), Model: wire.Model{Provider: "anthropic", ID: "claude-test"}})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Agentkit-Run-Id", fakeRun)
		w.Header().Set("Agentkit-Conversation-Id", fakeConv)
		w.WriteHeader(http.StatusOK)
		at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
		for i, e := range f.events {
			e.V, e.RunID, e.Seq, e.ID, e.At = wire.Version, fakeRun, i+1, fmt.Sprint(i+1), at.Add(time.Duration(i)*time.Millisecond)
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b)
		}
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(wire.ErrorResponse{V: wire.Version, Error: &wire.Error{Code: wire.CodeNotFound, Message: "not found"}})
	}
}

// fakeRunOf serves events and returns the stream of one request, checked
// against the invariants, and the messages the client has after it.
func fakeRunOf(t *testing.T, events ...wire.Event) ([]agui.Event, []message) {
	t.Helper()
	srv, err := agui.New(&fakeAPI{events: events}, agui.WithKeepAlive(0))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	in := input("t-fake", user("u1", "go"))
	body, _ := json.Marshal(in)
	resp, err := ts.Client().Post(ts.URL+agui.PathPrefix+"agent", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw := readSSE(t, resp.Body)
	evs := decode(t, raw)
	checkStream(t, "stream", recorded{in: in, events: evs, raw: raw})
	return evs, reduce(t, in["messages"].([]map[string]any), evs)
}

func delta(turn int, text string) wire.Event {
	return wire.Event{Type: wire.TypeTextDelta, Turn: turn, Text: text}
}

// turnDone is a turn's turn_done with its reply's text in the record.
func turnDone(turn int, reply string) wire.Event {
	return wire.Event{Type: wire.TypeTurnDone, Turn: turn, Record: &wire.TurnRecord{Turn: turn, Messages: []wire.TimedMessage{
		{Message: llm.Assistant(llm.Text{Text: reply})},
	}}}
}

func runDone(answer string) wire.Event {
	return wire.Event{Type: wire.TypeRunDone, Answer: answer, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3}, Turns: 1}
}

// summary is each message as "role id text".
func summary(msgs []message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, strings.TrimSpace(m.Role+" "+m.ID+" "+m.text()))
	}
	return out
}

// TestRetryIsSnapshotted: text of a turn the run retries is dropped by a
// MESSAGES_SNAPSHOT before the run's end; the attempt's text is a message
// of its own.
func TestRetryIsSnapshotted(t *testing.T) {
	t.Parallel()
	evs, msgs := fakeRunOf(t,
		wire.Event{Type: wire.TypeRunStart, ConversationID: fakeConv},
		delta(1, "Hel"),
		wire.Event{Type: wire.TypeRetrying, Attempt: 2, Error: &wire.Error{Code: wire.CodeOverloaded, Message: "busy"}},
		delta(1, "Hello"),
		turnDone(1, "Hello"),
		runDone("Hello"),
	)
	if !slices.Contains(types(evs), agui.EventMessagesSnapshot) {
		t.Fatalf("events %v: no MESSAGES_SNAPSHOT", types(evs))
	}
	if got, want := summary(msgs), []string{"user u1 go", "assistant run_f/t1.1 Hello"}; !slices.Equal(got, want) {
		t.Errorf("messages %q, want %q", got, want)
	}
	end := last(evs)
	if len(end.Usage) != 1 || end.Usage[0] != (agui.TokenUsage{Provider: "anthropic", Model: "claude-test", InputTokens: 13, OutputTokens: 5, TotalTokens: 18, CachedInputTokens: 3}) {
		t.Errorf("usage %+v", end.Usage)
	}
}

// TestRewindIsSnapshotted: a rewind drops the turns after it, which the
// snapshot leaves out; the turns before stand.
func TestRewindIsSnapshotted(t *testing.T) {
	t.Parallel()
	_, msgs := fakeRunOf(t,
		wire.Event{Type: wire.TypeRunStart, ConversationID: fakeConv},
		delta(1, "A"),
		wire.Event{Type: wire.TypeToolCall, Turn: 1, ToolUseID: "c1", Name: "echo", Input: json.RawMessage(`{}`)},
		wire.Event{Type: wire.TypeToolResult, Turn: 1, ToolUseID: "c1", Content: "boom", IsError: true},
		turnDone(1, "A"),
		delta(2, "B1"),
		wire.Event{Type: wire.TypeRewind, Turns: 1},
		delta(2, "B2"),
		turnDone(2, "B2"),
		runDone("B2"),
	)
	want := []string{"user u1 go", "assistant run_f/t1 A", "tool run_f/r/c1 boom", "assistant run_f/t2.1 B2"}
	if got := summary(msgs); !slices.Equal(got, want) {
		t.Errorf("messages %q, want %q", got, want)
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].ID != "c1" {
		t.Errorf("the first turn's calls %+v", msgs[1].ToolCalls)
	}
}

// TestMissedTextIsCaughtUp: the text of a turn whose deltas the stream did
// not carry comes from the turn's record, before it ends.
func TestMissedTextIsCaughtUp(t *testing.T) {
	t.Parallel()
	evs, msgs := fakeRunOf(t,
		wire.Event{Type: wire.TypeRunStart, ConversationID: fakeConv},
		turnDone(1, "Whole"),
		delta(2, "Part"),
		turnDone(2, "Partial"),
		runDone("Partial"),
	)
	if got, want := summary(msgs), []string{"user u1 go", "assistant run_f/t1 Whole", "assistant run_f/t2 Partial"}; !slices.Equal(got, want) {
		t.Errorf("messages %q, want %q", got, want)
	}
	if slices.Contains(types(evs), agui.EventMessagesSnapshot) {
		t.Error("a snapshot, with nothing dropped")
	}
}

// TestSurfacesOfOneCall: a call that shows two surfaces is two A2UI
// activities, each with its own messages in order; a failed call's result
// says it failed.
func TestSurfacesOfOneCall(t *testing.T) {
	t.Parallel()
	create := func(id string) json.RawMessage {
		return json.RawMessage(`{"version":"v0.9","createSurface":{"surfaceId":"` + id + `","catalogId":"` + a2ui.BasicCatalogID + `"}}`)
	}
	del := json.RawMessage(`{"version":"v0.9","deleteSurface":{"surfaceId":"a"}}`)
	evs, msgs := fakeRunOf(t,
		wire.Event{Type: wire.TypeRunStart, ConversationID: fakeConv},
		wire.Event{Type: wire.TypeToolCall, Turn: 1, ToolUseID: "c1", Name: a2ui.ToolName, Input: json.RawMessage(`{}`)},
		wire.Event{Type: wire.TypeToolResult, Turn: 1, ToolUseID: "c1", Content: "shown"},
		wire.Event{Type: wire.TypeUI, Turn: 1, ToolUseID: "c1", SurfaceID: "a", UI: create("a")},
		wire.Event{Type: wire.TypeUI, Turn: 1, ToolUseID: "c1", SurfaceID: "b", UI: create("b")},
		wire.Event{Type: wire.TypeUI, Turn: 1, ToolUseID: "c1", SurfaceID: "a", UI: del},
		turnDone(1, ""),
		runDone(""),
	)
	var acts []message
	for _, m := range msgs {
		if m.Role == "activity" {
			acts = append(acts, m)
		}
	}
	if len(acts) != 2 || acts[0].ID != "a2ui-surface-a-c1" || acts[1].ID != "a2ui-surface-b-c1" {
		t.Fatalf("activities %+v", acts)
	}
	var a struct {
		Ops []json.RawMessage `json:"a2ui_operations"`
	}
	if err := json.Unmarshal(acts[0].Content, &a); err != nil || len(a.Ops) != 2 || !sameJSON(a.Ops[1], del) {
		t.Errorf("surface a's messages %s", acts[0].Content)
	}
	if e := last(evs); e.Result != nil {
		t.Errorf("a run with no answer has result %s", e.Result)
	}
	for _, e := range evs {
		if e.Type == agui.EventToolCallResult && e.Metadata != nil {
			t.Errorf("a call that succeeded is marked: %+v", e.Metadata)
		}
	}
}

// TestFailedCallIsMarked: a call that failed has its result marked.
func TestFailedCallIsMarked(t *testing.T) {
	t.Parallel()
	evs, _ := fakeRunOf(t,
		wire.Event{Type: wire.TypeRunStart, ConversationID: fakeConv},
		wire.Event{Type: wire.TypeToolCall, Turn: 1, ToolUseID: "c1", Name: "echo"},
		wire.Event{Type: wire.TypeToolResult, Turn: 1, ToolUseID: "c1", Content: "boom", IsError: true},
		turnDone(1, ""),
		runDone("ok"),
	)
	for _, e := range evs {
		switch e.Type {
		case agui.EventToolCallArgs:
			if *e.Delta != "{}" {
				t.Errorf("a call with no input has arguments %q", *e.Delta)
			}
		case agui.EventToolCallResult:
			meta, _ := e.Metadata[agui.MetadataKey].(map[string]any)
			if meta["isError"] != true {
				t.Errorf("the failed call's result %+v", e)
			}
		}
	}
}

// TestRunsThatEndBadly: a flow run that waits for a decision, which AG-UI
// cannot give, and a stream that breaks before the run's end are RUN_ERROR.
func TestRunsThatEndBadly(t *testing.T) {
	t.Parallel()
	evs, _ := fakeRunOf(t,
		wire.Event{Type: wire.TypeRunStart, ConversationID: fakeConv},
		wire.Event{Type: wire.TypeRunError, Error: &wire.Error{Code: wire.CodeSuspended, Message: "waits for a decision"}},
	)
	if e := last(evs); e.Type != agui.EventRunError || e.Code != agui.CodeUnsupported {
		t.Errorf("a suspended flow run: %+v", e)
	}
	evs, _ = fakeRunOf(t, wire.Event{Type: wire.TypeRunStart, ConversationID: fakeConv}, delta(1, "half"))
	if e := last(evs); e.Type != agui.EventRunError || e.Code != wire.CodeInternal {
		t.Errorf("a broken stream: %+v", e)
	}
	evs, _ = fakeRunOf(t, wire.Event{Type: wire.TypeRunStart, ConversationID: fakeConv}, wire.Event{Type: wire.TypeRunError})
	if e := last(evs); e.Type != agui.EventRunError || e.Code != wire.CodeInternal || e.Message == "" {
		t.Errorf("a run_error without an error: %+v", e)
	}
}
