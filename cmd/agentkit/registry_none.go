//go:build !noagent && nomodels

package main

import (
	"errors"
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// registryBuilt: this build keeps no agent definitions.
const registryBuilt = false

// newRegistry, in a build without models, keeps no agent definitions: it
// could not build them. Turning it on explicitly is an error.
func (c cli) newRegistry(cfg serveConfig, _ serveEnv, _ agentapi.Store, _ map[string]tool.Registry, _ toolsetAdmin, _ clock.Clock, _ *slog.Logger) (agentapi.AgentRegistry, error) {
	if e := cfg.Registry.Enabled; e != nil && *e {
		return nil, errors.New("agent_registry.enabled: " + errNoModels.Error())
	}
	return nil, nil
}
