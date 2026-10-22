package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

// UI (T065): an agent whose UI is a2ui has the ui_render tool, and the
// messages of each of its successful calls go out as ui events.

// showsUI reports whether the agent of the conversation shows UI. It reads
// the conversation only when some agent does.
func (s *Server) showsUI(ctx context.Context, convID string) bool {
	if !s.anyUI {
		return false
	}
	c, err := s.store.GetConversation(ctx, convID)
	if err != nil {
		s.log.Warn("reading a run's conversation for its agent; its UI is not streamed", "conversation_id", convID, "err", err)
		return false
	}
	a := s.agents[c.Agent]
	return a != nil && a.UI == a2ui.UIMode
}

// uiEvents are the ui events that follow e, the wire form of the harness
// event ev: for the tool_result of a ui_render call that succeeded, one per
// message of the call, in order. calls holds the ui_render calls under way,
// by ID: ev's tool_call adds one, its tool_result takes it out.
func uiEvents(e Event, ev harness.Event, calls map[string]llm.ToolUse) []Event {
	switch ev.Kind {
	case harness.EventToolStart:
		if ev.ToolUse.Name == a2ui.ToolName {
			calls[ev.ToolUse.ID] = ev.ToolUse
		}
		return nil
	case harness.EventToolDone:
	default:
		return nil
	}
	call, ok := calls[ev.ToolResult.ToolUseID]
	delete(calls, ev.ToolResult.ToolUseID)
	if !ok || ev.ToolResult.IsError {
		return nil
	}
	// The tool validated the input before it succeeded: read it back without
	// the validator, which this package (and the api image) does not link.
	msgs, err := a2ui.ReadInput(call.Input)
	if err != nil {
		return nil
	}
	out := make([]Event, 0, len(msgs))
	for _, m := range msgs {
		raw, err := json.Marshal(m)
		if err != nil {
			return nil
		}
		out = append(out, Event{
			V: WireVersion, Type: TypeUI, RunID: e.RunID, At: e.At, Turn: e.Turn, Call: e.Call,
			ToolUseID: call.ID, SurfaceID: m.SurfaceID(), UI: raw,
		})
	}
	return out
}

// uiActionProblem says what is wrong with a ui_action, in the request's
// field names.
func uiActionProblem(err error) string {
	var es a2ui.Errors
	if !errors.As(err, &es) {
		return err.Error()
	}
	fields := map[string]string{"/name": "action", "/surfaceId": "surface_id", "/sourceComponentId": "component_id", "/context": "context"}
	lines := make([]string, len(es))
	for i, e := range es {
		field := e.Path
		for p, f := range fields {
			if rest, ok := strings.CutPrefix(e.Path, p); ok && (rest == "" || rest[0] == '/') {
				field = f + rest
			}
		}
		lines[i] = field + ": " + e.Detail
	}
	return strings.Join(lines, "; ")
}
