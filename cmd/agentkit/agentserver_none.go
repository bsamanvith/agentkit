//go:build noagent

package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// agentWiring, in a build without the agent component, is never made:
// openAgents refuses the component, so serve's ag stays nil.
type agentWiring struct{}

func (c cli) openAgents(context.Context, clock.Clock, *slog.Logger) (*agentWiring, error) {
	return nil, notBuilt(envComponents, "the agent API", "noagent")
}

func (*agentWiring) close() {}

func (c cli) buildAgents(context.Context, *agentWiring, serveConfig, map[string]tool.Registry, []toolset.Problem, serveEnv, auth.Authenticator, clock.Clock, *slog.Logger) (component.Mount, error) {
	return component.Mount{}, notBuilt(envComponents, "the agent API", "noagent")
}

// handler is nil: the agentkit toolsets call the agent API at their
// settings' URL.
func (*agentWiring) handler() http.Handler { return nil }

// server is nil: the A2A server, which needs the agent API in process,
// refuses to start.
func (*agentWiring) server() *agentapi.Server { return nil }

// serve serves rt on ln: there is no store to recover runs from.
func (*agentWiring) serve(ctx context.Context, rt *component.Runtime, ln net.Listener, _ runnersConfig, _ *slog.Logger) error {
	return rt.Serve(ctx, ln)
}
