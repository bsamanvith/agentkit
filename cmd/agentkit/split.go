package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/tool/toolrpc"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// The split deployment (T102): the agent API, the runner and the toolsets
// each in a container of its own, or any of them together. Each pair talks
// through an interface that has a local and a remote implementation:
//
//	agent-api ──runner.Agent──▶ runner ──tool.Registry──▶ toolsets
//
// Without the URLs below, every piece is local: one process, plain calls.
const (
	// runnerName is the runner component: the agents' turn loops (model and
	// tool calls), served at /rpc/runner/ to an agent API elsewhere.
	runnerName = "runner"
	// toolsetsName is the toolsets component: the toolsets of mcp.toolsets,
	// served at /rpc/toolsets/ to runners and agent APIs elsewhere.
	toolsetsName = "toolsets"

	// envRunnerURL makes the agent API run its agents on the runner at
	// this URL (http://runner:8080) instead of in process.
	envRunnerURL = "AGENTKIT_RUNNER_URL"
	// envToolsetsURL makes the runner, the agent API and the MCP server
	// take their toolsets from the toolsets component at this URL instead
	// of building them.
	envToolsetsURL = "AGENTKIT_TOOLSETS_URL"
	// envRPCToken is the bearer token between components: the runner and
	// toolsets components require it, and their callers send it. Required
	// to serve them on an address that is not loopback.
	envRPCToken = "AGENTKIT_RPC_TOKEN"
	// envDependencyWait is how long startup waits for the toolsets at
	// $AGENTKIT_TOOLSETS_URL to answer (default 60s): pods of a split
	// deployment start in any order.
	envDependencyWait = "AGENTKIT_DEPENDENCY_WAIT"
)

const defaultDependencyWait = time.Minute

// rpcComponent serves an rpc.Server: the runner or the toolsets.
type rpcComponent struct {
	name    string
	server  *rpc.Server
	skipped []string // optional toolsets skipped, for /readyz
}

func (r *rpcComponent) Name() string          { return r.name }
func (r *rpcComponent) Handler() http.Handler { return r.server }

// Shutdown cuts the calls in flight, a runner's runs included: their callers
// get rpc.ErrUnavailable, which an agent API retries (runnerrpc), and the
// HTTP server does not wait out its timeout on streams that would not end.
func (r *rpcComponent) Shutdown(context.Context) error {
	r.server.Shutdown()
	return nil
}

// Ready is degraded when optional toolsets were skipped.
func (r *rpcComponent) Ready(context.Context) error {
	if len(r.skipped) > 0 {
		return fmt.Errorf("%w: toolsets skipped: %s", component.ErrDegraded, strings.Join(r.skipped, ", "))
	}
	return nil
}

// newRPCServer is the server of a runner or toolsets component, refusing an
// address that is not loopback without a token.
func newRPCServer(name string, env serveEnv, log *slog.Logger) (*rpc.Server, error) {
	if env.rpcToken == "" && !isLoopback(env.addr) {
		return nil, fmt.Errorf("serving the %s on %s, which is not a loopback address, needs a token in $%s (its callers send the same)", name, env.addr, envRPCToken)
	}
	return rpc.NewServer(rpc.WithToken(env.rpcToken), rpc.WithLogger(log.With("component", name))), nil
}

// newRunner is the runner component: a harness per agent of the config,
// served at /rpc/runner/ (package runnerrpc).
func (c cli) newRunner(ctx context.Context, cfg serveConfig, built map[string]tool.Registry, env serveEnv, clk clock.Clock, log *slog.Logger) (component.Mount, error) {
	s, err := newRPCServer(runnerName, env, log)
	if err != nil {
		return component.Mount{}, err
	}
	agents := map[string]runner.Agent{}
	var skipped []string
	for _, a := range cfg.Agents {
		if len(a.RuntimeToolsets) > 0 {
			// TODO(T096): read them from the store here too.
			return component.Mount{}, fmt.Errorf("%s.runtime_toolsets: the %s component cannot use toolsets added at run time yet; run the agent in the agent API's process", a.label(cfg.legacy), runnerName)
		}
		h, sk, err := c.newHarness(ctx, cfg, a, built, nil, clk)
		if err != nil {
			return component.Mount{}, err
		}
		agents[a.Name] = h
		skipped = appendNew(skipped, sk...)
	}
	if err := runnerrpc.Register(s, agents); err != nil {
		return component.Mount{}, err
	}
	// As the agent API's drain (WithDrain): on SIGTERM, runs going on get
	// most of the shutdown timeout to finish, then are cut so their agent
	// API retries them elsewhere, before the HTTP server's deadline.
	context.AfterFunc(ctx, func() { time.AfterFunc(env.shutdownTimeout*9/10, s.Shutdown) })
	return component.Mount{
		Pattern:   rpc.PathPrefix + runnerrpc.Service + "/",
		Component: &rpcComponent{name: runnerName, server: s, skipped: skipped},
	}, nil
}

// newToolsets is the toolsets component: every toolset of mcp.toolsets,
// built or skipped, served at /rpc/toolsets/ (package toolrpc).
func (c cli) newToolsets(cfg serveConfig, built map[string]tool.Registry, problems []toolset.Problem, env serveEnv, log *slog.Logger) (component.Mount, error) {
	s, err := newRPCServer(toolsetsName, env, log)
	if err != nil {
		return component.Mount{}, err
	}
	specs, err := servedSpecs(env, cfg)
	if err != nil {
		return component.Mount{}, err
	}
	var sets []toolrpc.Toolset
	regs := map[string]tool.Registry{}
	var skipped []string
	for _, ts := range c.toolsetCatalog(cfg, specs, built, problems, env) {
		sets = append(sets, toolrpc.Toolset{
			Name: ts.Name, Type: ts.Type, Skipped: ts.Skipped,
			Credential: toolrpc.Credential{Status: ts.Credential.Status, Env: ts.Credential.Env},
		})
		if ts.Tools != nil {
			regs[ts.Name] = ts.Tools
		} else {
			skipped = append(skipped, ts.Name)
		}
	}
	if err := toolrpc.Register(s, sets, regs); err != nil {
		return component.Mount{}, err
	}
	return component.Mount{
		Pattern:   rpc.PathPrefix + toolrpc.Service + "/",
		Component: &rpcComponent{name: toolsetsName, server: s, skipped: skipped},
	}, nil
}

// remoteToolset is a toolset of the toolsets component: its registry, and
// what the component says about it (its credential lives there).
type remoteToolset struct {
	tool.Registry
	info toolrpc.Toolset
}

// remoteSkipped is why the toolsets component skipped a toolset.
type remoteSkipped struct{ info toolrpc.Toolset }

func (e remoteSkipped) Error() string {
	return fmt.Sprintf("toolset %s (%s): skipped by the toolsets component: %s", e.info.Name, e.info.Type, e.info.Skipped)
}

// remoteToolsets returns the registries of specs from the toolsets
// component at $AGENTKIT_TOOLSETS_URL, waiting for it to answer: the ones it
// skipped are problems, as for toolsets built here, and one it does not
// serve at all fails startup (its config differs from this one).
func (c cli) remoteToolsets(ctx context.Context, env serveEnv, specs []toolset.Spec, log *slog.Logger) (map[string]tool.Registry, []toolset.Problem, error) {
	conn, err := rpc.Dial(env.toolsetsURL, rpc.WithBearer(env.rpcToken))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", envToolsetsURL, err)
	}
	client := toolrpc.NewClient(conn)
	served, err := waitFor(ctx, env.dependencyWait, log, "toolsets at "+env.toolsetsURL, client.Toolsets)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", envToolsetsURL, err)
	}
	regs := map[string]tool.Registry{}
	var problems []toolset.Problem
	for _, s := range specs {
		i := slices.IndexFunc(served, func(t toolrpc.Toolset) bool { return t.Name == s.Name })
		if i < 0 {
			return nil, nil, fmt.Errorf("%s: the toolsets component serves no toolset %q; give it the same mcp.toolsets as this config", envToolsetsURL, s.Name)
		}
		if info := served[i]; info.Skipped != "" {
			problems = append(problems, toolset.Problem{Toolset: s.Name, Type: info.Type, Err: remoteSkipped{info}})
			log.Warn("optional toolset skipped", "toolset", s.Name, "type", info.Type, "err", info.Skipped, "by", envToolsetsURL)
			continue
		}
		regs[s.Name] = remoteToolset{Registry: client.Registry(s.Name), info: served[i]}
	}
	return regs, problems, nil
}

// waitFor calls f until it succeeds, an error other than rpc.ErrUnavailable
// (a wrong token, say) ends it, or wait has passed.
func waitFor[T any](ctx context.Context, wait time.Duration, log *slog.Logger, what string, f func(context.Context) (T, error)) (T, error) {
	deadline := time.Now().Add(wait)
	delay := 250 * time.Millisecond
	for logged := false; ; {
		// An attempt that hangs (a peer that takes the connection and never
		// answers) must not outlast the wait.
		actx, cancel := context.WithDeadline(ctx, deadline)
		v, err := f(actx)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			err = fmt.Errorf("%w: no answer within %s: %w", rpc.ErrUnavailable, wait, err)
		}
		if err == nil || !errors.Is(err, rpc.ErrUnavailable) || time.Now().Add(delay).After(deadline) {
			return v, err
		}
		if !logged {
			log.Info("waiting for a dependency", "dependency", what, "err", err)
			logged = true
		}
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-time.After(delay):
		}
		delay = min(2*delay, 2*time.Second)
	}
}

// credentialOfServed is a toolset's credential: as the toolsets component
// reports it for one of its toolsets, else from this process's environment.
func (c cli) credentialOfServed(s toolset.Spec, built map[string]tool.Registry, problems []toolset.Problem) agentapi.Credential {
	info, ok := remoteInfo(s.Name, built, problems)
	if !ok {
		return credentialOf(s, c.env)
	}
	return agentapi.Credential{Status: info.Credential.Status, Env: info.Credential.Env}
}

// remoteInfo is what the toolsets component said about a toolset, if it
// came from there.
func remoteInfo(name string, built map[string]tool.Registry, problems []toolset.Problem) (toolrpc.Toolset, bool) {
	if r, ok := built[name].(remoteToolset); ok {
		return r.info, true
	}
	for _, p := range problems {
		if rs, ok := errors.AsType[remoteSkipped](p.Err); ok && p.Toolset == name {
			return rs.info, true
		}
	}
	return toolrpc.Toolset{}, false
}
