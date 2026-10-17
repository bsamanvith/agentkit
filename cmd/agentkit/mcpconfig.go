package main

import (
	"time"

	"github.com/bsamanvith/agentkit/pkg/toolset"
)

const mcpServerName = "mcp-server"

// mcpConfig is the "mcp" section of the serve config file.
type mcpConfig struct {
	// BasePath is where the toolsets are served: {base_path}/{name}.
	BasePath string `json:"base_path"`
	// Stateful turns MCP sessions on (default stateless). Replicas then need
	// session affinity, and open session streams hold a shutdown until its
	// deadline.
	Stateful bool `json:"stateful"`
	// CallTimeout limits each tool call; "0s" means no limit.
	CallTimeout duration `json:"call_timeout"`
	// MaxInFlight caps concurrent toolset requests (503 past it); 0 means no cap.
	MaxInFlight int `json:"max_in_flight"`
	// SessionTimeout closes idle sessions in stateful mode; "0s" means never.
	SessionTimeout duration `json:"session_timeout"`
	// MaxBodyBytes limits a request body; 0 means the SDK default (4 MiB).
	MaxBodyBytes int64 `json:"max_body_bytes"`
	// Toolsets are built by toolset.Build from the compiled-in catalog.
	Toolsets []toolset.Spec `json:"toolsets"`
}

// defaultMCPConfig is what an omitted field means.
func defaultMCPConfig() mcpConfig {
	return mcpConfig{
		BasePath:    "/mcp",
		CallTimeout: duration(30 * time.Second),
		MaxInFlight: 512,
	}
}
