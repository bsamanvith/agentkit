//go:build !noagui

package main

import (
	"fmt"
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agui"
	"github.com/bsamanvith/agentkit/pkg/component"
)

// newAGUI is the agui component (AGENTKIT_COMPONENTS=agent,agui): every
// agent of the agent API served to AG-UI frontends at /agui/{agent}. It
// calls the agent API in process with each request's own credential, so
// the API's keys, tokens, tenants, scopes and agent visibility apply as
// they do on /v1.
//
// Its mount goes before the agent API's, as a2a's does: the runtime shuts
// components down in reverse order, so the agent API drains the runs that
// AG-UI follows before AG-UI stops following them.
func newAGUI(agents *agentapi.Server, log *slog.Logger) ([]component.Mount, error) {
	if agents == nil {
		return nil, fmt.Errorf("needs the %s component in the same process ($%s)", agentapi.Name, envComponents)
	}
	srv, err := agui.New(agents.Handler(), agui.WithLogger(log.With("component", agui.Name)))
	if err != nil {
		return nil, err
	}
	return []component.Mount{{Pattern: agui.PathPrefix, Component: srv}}, nil
}
