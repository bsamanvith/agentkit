//go:build nomcp

package main

import (
	"context"
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

func newMCPServer(context.Context, mcpConfig, map[string]tool.Registry, []toolset.Problem, auth.Authenticator, string, *slog.Logger) (component.Mount, error) {
	return component.Mount{}, notBuilt(envComponents, "the MCP server", "nomcp")
}
