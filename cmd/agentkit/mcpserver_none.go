//go:build nomcp

package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

func newMCPServer(context.Context, mcpConfig, map[string]tool.Registry, []toolset.Problem, auth.Authenticator, http.Handler, string, *slog.Logger) (component.Mount, error) {
	return component.Mount{}, notBuilt(envComponents, "the MCP server", "nomcp")
}

// newResourceMetadata serves nothing: there are no MCP endpoints to describe.
func newResourceMetadata(mcpConfig) ([]component.Mount, error) { return nil, nil }
