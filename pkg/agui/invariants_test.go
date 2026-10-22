package agui_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
	"github.com/bsamanvith/agentkit/pkg/agui"
)

// The invariants every stream a test reads must keep (checkStream, run on
// each in the rig's cleanup), which are what the reference client's
// verifier and schemas enforce (@ag-ui/client 1.0.2, verify.ts and
// @ag-ui/core's schemas), and what our mapping promises:
//
//   - the first event is RUN_STARTED, echoing the request's threadId and
//     runId; exactly one RUN_FINISHED or RUN_ERROR, and it is last;
//   - every event has its type's required fields and no field its type does
//     not have;
//   - a text message's content and end come after its start, and nothing
//     starts twice while open; a tool call's args and end likewise; nothing
//     is open at RUN_FINISHED;
//   - a TOOL_CALL_RESULT answers a call started in the stream, or, in a
//     resume, one the resume answers;
//   - a run that succeeds leaves no call unanswered; one that ends on an
//     interrupt leaves unanswered only the calls its interrupts name;
//   - message IDs are unique across text, tool and activity messages;
//   - an A2UI activity holds A2UI messages that package a2ui accepts.

// fields are the fields of each event type, true when required.
var fields = map[agui.EventType]map[string]bool{
	agui.EventRunStarted:         {"threadId": true, "runId": true, "protocolVersion": false, "parentRunId": false, "input": false},
	agui.EventRunFinished:        {"threadId": true, "runId": true, "result": false, "outcome": false, "usage": false},
	agui.EventRunError:           {"message": true, "code": false, "usage": false},
	agui.EventTextMessageStart:   {"messageId": true, "role": false, "name": false},
	agui.EventTextMessageContent: {"messageId": true, "delta": true},
	agui.EventTextMessageEnd:     {"messageId": true},
	agui.EventToolCallStart:      {"toolCallId": true, "toolCallName": true, "parentMessageId": false},
	agui.EventToolCallArgs:       {"toolCallId": true, "delta": true},
	agui.EventToolCallEnd:        {"toolCallId": true},
	agui.EventToolCallResult:     {"messageId": true, "toolCallId": true, "content": true, "role": false},
	agui.EventActivitySnapshot:   {"messageId": true, "activityType": true, "content": true, "replace": false},
	agui.EventMessagesSnapshot:   {"messages": true},
}

var common = []string{"type", "timestamp", "rawEvent", "metadata"}

func checkStream(t testing.TB, name string, s recorded) {
	t.Helper()
	evs := s.events
	if len(evs) == 0 {
		t.Errorf("%s: no events", name)
		return
	}
	for i, raw := range s.raw {
		checkFields(t, name, i, raw)
	}
	if f := evs[0]; f.Type != agui.EventRunStarted || f.ThreadID != s.in["threadId"] || f.RunID != s.in["runId"] || f.ProtocolVersion != agui.ProtocolVersion {
		t.Errorf("%s: first event %+v, want RUN_STARTED of thread %v run %v", name, f, s.in["threadId"], s.in["runId"])
	}
	_, resuming := s.in["resume"]
	openText, openCalls := map[string]bool{}, map[string]bool{}
	started, results := map[string]bool{}, map[string]bool{}
	textIDs, toolIDs, activityIDs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, e := range evs {
		terminal := e.Type == agui.EventRunFinished || e.Type == agui.EventRunError
		if terminal != (i == len(evs)-1) {
			t.Errorf("%s event %d: %s; the last event is %d", name, i, e.Type, len(evs)-1)
		}
		if i > 0 && e.Type == agui.EventRunStarted {
			t.Errorf("%s event %d: a second RUN_STARTED", name, i)
		}
		switch e.Type {
		case agui.EventTextMessageStart:
			if openText[e.MessageID] {
				t.Errorf("%s event %d: message %s started while open", name, i, e.MessageID)
			}
			openText[e.MessageID], textIDs[e.MessageID] = true, true
		case agui.EventTextMessageContent, agui.EventTextMessageEnd:
			if !openText[e.MessageID] {
				t.Errorf("%s event %d: %s of message %s, which is not open", name, i, e.Type, e.MessageID)
			}
			if e.Type == agui.EventTextMessageEnd {
				delete(openText, e.MessageID)
			}
		case agui.EventToolCallStart:
			if openCalls[e.ToolCallID] || started[e.ToolCallID] {
				t.Errorf("%s event %d: call %s started twice", name, i, e.ToolCallID)
			}
			openCalls[e.ToolCallID], started[e.ToolCallID] = true, true
		case agui.EventToolCallArgs, agui.EventToolCallEnd:
			if !openCalls[e.ToolCallID] {
				t.Errorf("%s event %d: %s of call %s, which is not open", name, i, e.Type, e.ToolCallID)
			}
			if e.Type == agui.EventToolCallEnd {
				delete(openCalls, e.ToolCallID)
			}
		case agui.EventToolCallResult:
			if !started[e.ToolCallID] && !resuming {
				t.Errorf("%s event %d: the result of call %s, never started", name, i, e.ToolCallID)
			}
			if toolIDs[e.MessageID] || results[e.ToolCallID] {
				t.Errorf("%s event %d: a second result %s of call %s", name, i, e.MessageID, e.ToolCallID)
			}
			toolIDs[e.MessageID], results[e.ToolCallID] = true, true
		case agui.EventActivitySnapshot:
			activityIDs[e.MessageID] = true
			if e.ActivityType != agui.ActivityTypeA2UI {
				t.Errorf("%s event %d: activity type %q", name, i, e.ActivityType)
				continue
			}
			if e.Replace == nil || !*e.Replace {
				// Each snapshot holds every message of its surface and call:
				// one that merged into the last would repeat them.
				t.Errorf("%s event %d: an A2UI activity that does not replace", name, i)
			}
			var c map[string][]json.RawMessage
			if err := json.Unmarshal(e.Content, &c); err != nil || len(c[agui.A2UIOperationsKey]) == 0 || len(c) != 1 {
				t.Errorf("%s event %d: A2UI activity content %s", name, i, e.Content)
			}
			for _, op := range c[agui.A2UIOperationsKey] {
				if _, err := a2ui.Parse(op); err != nil {
					t.Errorf("%s event %d: A2UI message %s: %v", name, i, op, err)
				}
			}
		case agui.EventRunFinished:
			if len(openText) > 0 || len(openCalls) > 0 {
				t.Errorf("%s: RUN_FINISHED with messages %v and calls %v open", name, openText, openCalls)
			}
			if e.ThreadID != s.in["threadId"] || e.RunID != s.in["runId"] {
				t.Errorf("%s: RUN_FINISHED of thread %s run %s", name, e.ThreadID, e.RunID)
			}
			var unanswered []string
			for id := range started {
				if !results[id] {
					unanswered = append(unanswered, id)
				}
			}
			switch o := e.Outcome; {
			case o == nil:
				t.Errorf("%s: RUN_FINISHED without an outcome", name)
			case o.Type == agui.OutcomeInterrupt:
				if len(o.Interrupts) == 0 {
					t.Errorf("%s: an interrupt outcome with no interrupts", name)
				}
				var named []string
				for _, in := range o.Interrupts {
					if in.ID == "" || in.Reason == "" || in.ToolCallID == "" || len(in.ResponseSchema) == 0 {
						t.Errorf("%s: interrupt %+v", name, in)
					}
					named = append(named, in.ToolCallID)
				}
				for _, id := range unanswered {
					if !slices.Contains(named, id) {
						t.Errorf("%s: call %s left unanswered by a run that waits for %v", name, id, named)
					}
				}
			case o.Type == agui.OutcomeSuccess:
				if len(unanswered) > 0 {
					t.Errorf("%s: a run that succeeded left calls %v unanswered", name, unanswered)
				}
			case o.Type != agui.OutcomeCancelled:
				t.Errorf("%s: outcome %q", name, o.Type)
			}
		}
	}
	for id := range textIDs {
		if toolIDs[id] || activityIDs[id] {
			t.Errorf("%s: message ID %s names two kinds of message", name, id)
		}
	}
	for id := range toolIDs {
		if activityIDs[id] {
			t.Errorf("%s: message ID %s names two kinds of message", name, id)
		}
	}
}

// checkFields checks one event's JSON against its type's fields.
func checkFields(t testing.TB, name string, i int, raw json.RawMessage) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Errorf("%s event %d: not a JSON object: %s", name, i, raw)
		return
	}
	var typ agui.EventType
	_ = json.Unmarshal(m["type"], &typ)
	want, ok := fields[typ]
	if !ok {
		t.Errorf("%s event %d: type %q is not one the server sends", name, i, typ)
		return
	}
	for k := range m {
		if _, known := want[k]; !known && !slices.Contains(common, k) {
			t.Errorf("%s event %d (%s): field %q is not the type's", name, i, typ, k)
		}
		if string(m[k]) == "null" {
			t.Errorf("%s event %d (%s): %q is null; optional fields are left out", name, i, typ, k)
		}
	}
	for k, req := range want {
		if _, has := m[k]; req && !has {
			t.Errorf("%s event %d (%s): no %q", name, i, typ, k)
		}
	}
	if ts, has := m["timestamp"]; has {
		var n int64
		if json.Unmarshal(ts, &n) != nil || n <= 0 {
			t.Errorf("%s event %d (%s): timestamp %s", name, i, typ, ts)
		}
	}
}

// message is a message as an AG-UI client's reducer builds it from the
// request and the events, with the fields the tests read.
type message struct {
	ID           string          `json:"id"`
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content,omitempty"`
	ToolCalls    []agui.ToolCall `json:"toolCalls,omitempty"`
	ToolCallID   string          `json:"toolCallId,omitempty"`
	ActivityType string          `json:"activityType,omitempty"`
}

// text is a message's content as a string ("" if it is not one).
func (m message) text() string {
	var s string
	_ = json.Unmarshal(m.Content, &s)
	return s
}

// reduce applies a stream's events to the messages of its request, as the
// reference client's default reducer does for the events the server sends
// (a MESSAGES_SNAPSHOT keeps the activity messages it does not list).
func reduce(tb testing.TB, msgs []map[string]any, evs []agui.Event) []message {
	tb.Helper()
	var out []message
	b, _ := json.Marshal(msgs)
	if err := json.Unmarshal(b, &out); err != nil {
		tb.Fatal(err)
	}
	find := func(id string) int { return slices.IndexFunc(out, func(m message) bool { return m.ID == id }) }
	for _, e := range evs {
		switch e.Type {
		case agui.EventTextMessageStart:
			if find(e.MessageID) < 0 {
				out = append(out, message{ID: e.MessageID, Role: "assistant", Content: json.RawMessage(`""`)})
			}
		case agui.EventTextMessageContent:
			i := find(e.MessageID)
			s := out[i].text() + *e.Delta
			out[i].Content, _ = json.Marshal(s)
		case agui.EventToolCallStart:
			i := find(e.ParentMessageID)
			if i < 0 {
				out = append(out, message{ID: e.ParentMessageID, Role: "assistant"})
				i = len(out) - 1
			}
			out[i].ToolCalls = append(out[i].ToolCalls, agui.ToolCall{ID: e.ToolCallID, Type: "function", Function: agui.FunctionCall{Name: e.ToolCallName}})
		case agui.EventToolCallArgs:
			for i := range out {
				for j := range out[i].ToolCalls {
					if out[i].ToolCalls[j].ID == e.ToolCallID {
						out[i].ToolCalls[j].Function.Arguments += *e.Delta
					}
				}
			}
		case agui.EventToolCallResult:
			out = append(out, message{ID: e.MessageID, Role: "tool", Content: e.Content, ToolCallID: e.ToolCallID})
		case agui.EventActivitySnapshot:
			m := message{ID: e.MessageID, Role: "activity", ActivityType: e.ActivityType, Content: e.Content}
			if i := find(e.MessageID); i >= 0 {
				out[i] = m
			} else {
				out = append(out, m)
			}
		case agui.EventMessagesSnapshot:
			var snap []message
			if err := json.Unmarshal(e.Messages, &snap); err != nil {
				tb.Fatal(err)
			}
			kept := slices.DeleteFunc(slices.Clone(out), func(m message) bool { return m.Role != "activity" })
			out = slices.Concat(snap, kept)
		}
	}
	return out
}
