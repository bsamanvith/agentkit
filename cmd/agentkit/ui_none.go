//go:build nomodels

package main

import (
	"context"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// withUI fails: a build without models runs no harness, so it leaves the
// ui_render tool and its validator out (ui_tool.go). Agents with UI run on a
// runner, whose harness has the tool.
func withUI(context.Context, tool.Registry) (tool.Registry, error) {
	return nil, fmt.Errorf("ui: this agentkit is built without model adapters (-tags nomodels), so it has no ui_render tool: run agents on a runner ($%s), or use an image that has them", envRunnerURL)
}
