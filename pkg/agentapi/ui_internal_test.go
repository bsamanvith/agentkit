package agentapi

import (
	"encoding/json"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

// TestUIEventsOnlyForSuccess: a ui_render call whose result is an error
// shows nothing, even when its input is valid (a call cancelled, denied or
// failed for another reason), and neither does a result of a call the
// server did not see start.
func TestUIEventsOnlyForSuccess(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"messages": [{"version": "v0.9", "createSurface": {"surfaceId": "s", "catalogId": "` + a2ui.BasicCatalogID + `"}}]}`)
	start := func(id string) harness.Event {
		return harness.Event{Kind: harness.EventToolStart, ToolUse: llm.ToolUse{ID: id, Name: a2ui.ToolName, Input: input}}
	}
	done := func(id string, isError bool) harness.Event {
		return harness.Event{Kind: harness.EventToolDone, ToolResult: llm.ToolResult{ToolUseID: id, IsError: isError}}
	}
	calls := map[string]llm.ToolUse{}
	e := Event{V: WireVersion, Type: TypeToolResult, RunID: "run_1"}
	uiEvents(e, start("a"), calls)
	uiEvents(e, start("b"), calls)
	if got := uiEvents(e, done("a", true), calls); len(got) != 0 {
		t.Errorf("a failed call showed %d ui events", len(got))
	}
	if got := uiEvents(e, done("x", false), calls); len(got) != 0 {
		t.Errorf("an unknown call showed %d ui events", len(got))
	}
	if got := uiEvents(e, done("b", false), calls); len(got) != 1 || got[0].SurfaceID != "s" || got[0].ToolUseID != "b" {
		t.Errorf("the successful call showed %+v, want one ui event of surface s", got)
	}
	if len(calls) != 0 {
		t.Errorf("calls left under way: %v", calls)
	}
}
