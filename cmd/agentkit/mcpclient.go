//go:build !nomcp

package main

import (
	"log/slog"
	"os"

	"github.com/bsamanvith/agentkit/pkg/mcp/client"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// mcpClientFactories is the toolset type "mcp": other MCP servers' tools
// (T094).
func mcpClientFactories(logger *slog.Logger) []toolset.Factory {
	return []toolset.Factory{client.Factory(client.WithLookupEnv(os.LookupEnv), client.WithLogger(logger))}
}
