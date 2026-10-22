//go:build !nomodels

package main

import (
	"context"
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The ui_render tool runs in an agent's harness, which needs a model, so a
// build without models (-tags nomodels: the api and toolsets images) leaves
// the tool and its validator out (ui_none.go).

// withUI adds ui_render (package a2ui) to an agent's tools. A tool of the
// agent's toolsets with its name is an error.
func withUI(ctx context.Context, tools tool.Registry) (tool.Registry, error) {
	defs, err := tools.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the agent's tools: %w", err)
	}
	if slices.ContainsFunc(defs, func(d tool.Definition) bool { return d.Name == a2ui.ToolName }) {
		return nil, fmt.Errorf("a toolset of the agent has a tool named %q, the UI tool's name", a2ui.ToolName)
	}
	ui, err := tool.NewRegistry([]tool.Tool{a2ui.Tool()})
	if err != nil {
		return nil, err
	}
	return joinRegistries(ui, tools), nil
}
