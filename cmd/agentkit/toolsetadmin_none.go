//go:build nomcp && !noagent

package main

import (
	"errors"
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
)

// newToolsetAdmin, in a build without MCP, is nil: the toolsets added at run
// time are MCP servers. Turning them on explicitly is an error.
func (c cli) newToolsetAdmin(cfg serveConfig, _ agentapi.Store, _ *slog.Logger) (toolsetAdmin, error) {
	if cfg.Runtime.Enabled != nil && *cfg.Runtime.Enabled {
		return nil, errors.New("runtime_toolsets: this build has no MCP client (built with -tags nomcp)")
	}
	return nil, nil
}
