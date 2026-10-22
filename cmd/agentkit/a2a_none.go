//go:build noa2a

package main

import (
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/component"
)

func (c cli) newA2A(*agentapi.Server, serveConfig, auth.Authenticator, *slog.Logger) ([]component.Mount, error) {
	return nil, notBuilt(envComponents, "the A2A server", "noa2a")
}
