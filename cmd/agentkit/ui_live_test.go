//go:build live

package main

import (
	"testing"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// TestLiveUIRender: real Claude, with ui_render and a request to let the
// user pick between options, produces a surface the validator takes on its
// first or second call.
func TestLiveUIRender(t *testing.T) {
	t.Parallel()
	requireCredentials(t)
	reg, err := tool.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := withUI(t.Context(), reg)
	if err != nil {
		t.Fatal(err)
	}
	model, err := newClaude(modelFlags{model: defaultModelID, effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := harness.New(model, tools, harness.WithMaxTurns(4),
		harness.WithSystem("You are a travel assistant. When the user has to choose, show them a UI to choose with."))
	if err != nil {
		t.Fatal(err)
	}
	calls, valid := 0, 0
	for ev, err := range h.Run(t.Context(), harness.Input{Message: harness.UserText(
		"I want to fly to Lisbon next month. Let me pick between economy, premium economy and business, then I'll tell you.")}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if ev.Kind == harness.EventToolDone && calls < 2 {
			calls++
			if ev.ToolResult.IsError {
				t.Logf("ui_render call %d refused: %s", calls, ev.ToolResult.Content)
				continue
			}
			valid = calls
		}
		if ev.Kind == harness.EventToolStart && ev.ToolUse.Name == a2ui.ToolName {
			t.Logf("ui_render: %s", ev.ToolUse.Input)
		}
	}
	if valid == 0 {
		t.Errorf("Claude made no valid surface in %d ui_render calls", calls)
	}
}
