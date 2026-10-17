//go:build nomcp

package main

import (
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// mcpClientFactories, in a build without MCP, is empty: config naming type
// "mcp" fails at startup with toolset.ErrUnknownType.
func mcpClientFactories(*slog.Logger) []toolset.Factory { return nil }
