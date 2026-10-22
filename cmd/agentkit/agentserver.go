//go:build !noagent

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/bus/busopen"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/storeopen"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// The agent component (agentapi.Name): the agent API on its store and bus,
// its agents' memory and the toolsets added at run time, and the recovery
// loop that resumes the runs a process left. The noagent tag leaves all of
// it out (agentserver_none.go): an image that serves no agent API (the
// runner and toolsets images) then links none of it. What the runner
// component shares with it (the agents' config, newHarness) is in agent.go.

// agentWiring is what serve keeps of the agent component: the store and
// bus, opened before the components are built, and the API once built, for
// the MCP server's agentkit toolsets and the recovery loop.
type agentWiring struct {
	st       openedStore
	b        bus.Bus
	closeBus func()
	srv      *agentapi.Server
}

// openAgents opens the agent API's store and bus.
func (c cli) openAgents(ctx context.Context, clk clock.Clock, log *slog.Logger) (*agentWiring, error) {
	st, err := c.openStore(ctx, clk)
	if err != nil {
		return nil, err
	}
	b, closeBus, err := c.openBus(ctx, clk, log)
	if err != nil {
		st.close()
		return nil, err
	}
	return &agentWiring{st: st, b: b, closeBus: closeBus}, nil
}

// close closes the bus, then the store.
func (w *agentWiring) close() {
	w.closeBus()
	w.st.close()
}

// buildAgents is newAgents on w's store and bus.
func (c cli) buildAgents(ctx context.Context, w *agentWiring, cfg serveConfig, built map[string]tool.Registry, problems []toolset.Problem, env serveEnv, authn auth.Authenticator, clk clock.Clock, log *slog.Logger) (component.Mount, error) {
	m, srv, err := c.newAgents(ctx, cfg, built, problems, env, authn, w.st.Store, w.b, clk, log)
	w.srv = srv
	return m, err
}

// handler is the agent API's handler, which the agentkit toolsets call in
// process; nil when it does not run here.
func (w *agentWiring) handler() http.Handler {
	if w == nil || w.srv == nil {
		return nil
	}
	return w.srv.Handler()
}

// server is the agent API, which the A2A server calls in process; nil when
// it does not run here.
func (w *agentWiring) server() *agentapi.Server {
	if w == nil {
		return nil
	}
	return w.srv
}

// serve serves rt on ln with the recovery loop over the store, which
// resumes the background runs a process left (chat runs stay interrupted
// for their user). It stops first, then the server drains, then the store
// closes (serve's defers).
func (w *agentWiring) serve(ctx context.Context, rt *component.Runtime, ln net.Listener, log *slog.Logger) error {
	if w == nil || w.srv == nil {
		return rt.Serve(ctx, ln)
	}
	agents, st := w.srv, w.st
	rc, err := runner.NewRecovery(agents.Runner(), st.admin, runner.WithExecute(agents.RunRecovered))
	if err != nil {
		ln.Close()
		return err
	}
	// A recovery loop that cannot read across tenants (a Postgres role under
	// row-level security, with no $AGENTKIT_STORE_ADMIN) fails startup
	// rather than every pass.
	if _, err := st.admin.ListResumableAll(ctx, time.Time{}, 1); err != nil {
		ln.Close()
		return fmt.Errorf("recovery: %w", err)
	}
	rctx, stopRecovery := context.WithCancel(ctx)
	recovered := make(chan struct{})
	go func() {
		defer close(recovered)
		_ = rc.Run(rctx, func(r runner.Report, err error) {
			switch {
			case err != nil:
				log.Error("recovery pass", "component", agentapi.Name, "resumed", r.Resumed, "skipped", r.Skipped, "errors", r.Errors, "err", err)
			case r.Resumed > 0:
				log.Info("recovery pass", "component", agentapi.Name, "resumed", r.Resumed, "skipped", r.Skipped)
			}
		})
	}()
	err = rt.Serve(ctx, ln)
	stopRecovery()
	<-recovered
	return err
}

// openedStore is the store serve runs on: the tenant-scoped view the agent
// API gets, the cross-tenant Admin only the recovery loop gets, and Close.
type openedStore struct {
	agentapi.Store
	admin store.Admin
	close func()
}

// openStore opens the store $AGENTKIT_STORE names (package storeopen); an
// unknown spec, or a backend this build leaves out (backends.go), is an
// error, which never echoes the value (a connection string can carry a
// password).
func (c cli) openStore(ctx context.Context, clk clock.Clock) (openedStore, error) {
	migrate := true
	switch v := c.env(envStoreMigrate); v {
	case "", "on":
	case "off":
		migrate = false
	default:
		return openedStore{}, fmt.Errorf("%s: %q is not on or off", envStoreMigrate, v)
	}
	o, err := storeopen.Open(ctx, c.env(envStore), storeopen.WithClock(clk), storeopen.WithMigrate(migrate), storeopen.WithAdminURL(c.env(envStoreAdmin)))
	if err != nil {
		return openedStore{}, fmt.Errorf("%s: %w", envStore, err)
	}
	return openedStore{Store: o.Store, admin: o.Admin, close: func() { _ = o.Close() }}, nil
}

// openBus opens the bus $AGENTKIT_BUS names (package busopen): nil for one
// replica, and the function that closes it. An unknown value is an error, which never echoes
// it (a URL can carry a password).
func (c cli) openBus(ctx context.Context, clk clock.Clock, log *slog.Logger) (bus.Bus, func(), error) {
	spec := c.env(envBus)
	if spec == "" || spec == "memory" {
		// nil: the agent API makes its own, and knows that no other
		// replica shares it (a held run is then this process's or nobody's).
		return nil, func() {}, nil
	}
	b, closeBus, err := busopen.Open(ctx, spec, busopen.WithClock(clk), busopen.WithLogger(log.With("component", "bus")))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", envBus, err)
	}
	return b, func() { _ = closeBus() }, nil
}

// agentComponent is the agent API with the readiness of its toolsets, or
// of the runner its agents run on.
type agentComponent struct {
	*agentapi.Server
	skipped []string
	// runner runs the agents when they run elsewhere ($AGENTKIT_RUNNER_URL);
	// agents are the names it must run.
	runner    *runnerrpc.Client
	runnerURL string
	agents    []string
	// toolsets is one of the toolsets served by the toolsets component
	// ($AGENTKIT_TOOLSETS_URL), nil when they are built here: listing its
	// tools checks the component.
	toolsets    tool.Registry
	toolsetsURL string
	// admin keeps the toolsets added at run time; nil when off.
	admin toolsetAdmin
	// closers close the agents' memory stores.
	closers []func() error
	// skillReport lists the agents' skills in /readyz (Report).
	skillReport
}

// Shutdown drains the API, then closes the connections to the MCP servers
// added at run time and the agents' memory stores.
func (a *agentComponent) Shutdown(ctx context.Context) error {
	err := a.Server.Shutdown(ctx)
	if a.admin != nil {
		_ = a.admin.Close()
	}
	return errors.Join(err, closeAll(a.closers))
}

// runnerCheckTimeout bounds the call that checks the runner in /readyz.
const runnerCheckTimeout = 2 * time.Second

// Ready is degraded when optional toolsets the agents name were skipped,
// when the toolsets component cannot be reached, or when the runner the
// agents run on cannot be reached or does not run them:
// the API still serves conversations and runs that are stored.
func (a *agentComponent) Ready(ctx context.Context) error {
	if err := a.Server.Ready(ctx); err != nil {
		return err
	}
	if a.runner != nil {
		ctx, cancel := context.WithTimeout(ctx, runnerCheckTimeout)
		defer cancel()
		served, err := a.runner.Agents(ctx)
		if err != nil {
			return fmt.Errorf("%w: runner at %s: %w", component.ErrDegraded, a.runnerURL, err)
		}
		var missing []string
		for _, n := range a.agents {
			if !slices.Contains(served, n) {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("%w: the runner at %s does not run agents %s", component.ErrDegraded, a.runnerURL, strings.Join(missing, ", "))
		}
	}
	if a.toolsets != nil {
		ctx, cancel := context.WithTimeout(ctx, runnerCheckTimeout)
		defer cancel()
		if _, err := a.toolsets.List(ctx); err != nil {
			return fmt.Errorf("%w: toolsets at %s: %w", component.ErrDegraded, a.toolsetsURL, err)
		}
	}
	return degraded(a.skipped, a.skillReport)
}

// newAgents builds the agent component: per agent the model, its toolsets
// merged into one registry and the harness; the catalog of the toolsets
// served (specs, as built and skipped); and the API on top, mounted at /v1/,
// on st and the clock clk. ctx is the signal context: when it is done, the
// drain begins.
func (c cli) newAgents(ctx context.Context, cfg serveConfig, built map[string]tool.Registry, problems []toolset.Problem, env serveEnv, authn auth.Authenticator, st agentapi.Store, b bus.Bus, clk clock.Clock, log *slog.Logger) (component.Mount, *agentapi.Server, error) {
	specs, err := servedSpecs(env, cfg)
	if err != nil {
		return component.Mount{}, nil, err
	}
	if authn == nil && !isLoopback(env.addr) {
		return component.Mount{}, nil, needsCredentials(env.addr)
	}
	var remote *runnerrpc.Client
	if env.runnerURL != "" {
		conn, err := rpc.Dial(env.runnerURL, rpc.WithBearer(env.rpcToken))
		if err != nil {
			return component.Mount{}, nil, fmt.Errorf("%s: %w", envRunnerURL, err)
		}
		remote = runnerrpc.NewClient(conn)
	}
	admin, err := c.newToolsetAdmin(cfg, st, log)
	if err != nil {
		return component.Mount{}, nil, err
	}
	var agents []agentapi.Agent
	var (
		skipped []string
		skills  skillReport
		closers []func() error
	)
	fail := func(err error) (component.Mount, *agentapi.Server, error) {
		if admin != nil {
			_ = admin.Close()
		}
		_ = closeAll(closers)
		return component.Mount{}, nil, err
	}
	for _, a := range cfg.Agents {
		def := agentapi.Agent{
			Name: a.Name, Description: a.Description, System: a.System,
			Model:    wire.Model{Provider: a.Model.Provider, ID: a.Model.id(), Effort: a.Model.Effort},
			Toolsets: a.Toolsets, PrefixToolNames: a.PrefixToolNames, RuntimeToolsets: a.RuntimeToolsets,
		}
		if len(a.RuntimeToolsets) > 0 && admin == nil {
			return fail(fmt.Errorf("%s.runtime_toolsets: toolsets added at run time are off (runtime_toolsets.enabled)", a.label(cfg.legacy)))
		}
		for _, name := range a.RuntimeToolsets {
			if slices.ContainsFunc(cfg.MCP.Toolsets, func(s toolset.Spec) bool { return s.Name == name }) {
				return fail(fmt.Errorf("%s.runtime_toolsets: %q is a toolset of the config file; list it in toolsets", a.label(cfg.legacy), name))
			}
		}
		if remote != nil {
			if len(a.RuntimeToolsets) > 0 {
				// TODO(T096): the runner component reads them too.
				return fail(fmt.Errorf("%s.runtime_toolsets: an agent that runs on the runner at $%s cannot use toolsets added at run time yet", a.label(cfg.legacy), envRunnerURL))
			}
			// The runner builds the model and the tools: this process
			// needs neither (the agentkit-api image has no model adapter).
			if a.Memory != nil {
				// TODO(T058 follow-up): the tenant does not cross to the
				// runner yet, so its memory calls would all be refused.
				return fail(fmt.Errorf("%s.memory: an agent that runs on the runner at $%s cannot have memory yet", a.label(cfg.legacy), envRunnerURL))
			}
			if err := a.check(cfg.legacy); err != nil {
				return fail(err)
			}
			def.Runner = remote.Agent(a.Name, a.Model.window())
			for _, name := range a.Toolsets {
				if slices.ContainsFunc(problems, func(p toolset.Problem) bool { return p.Toolset == name }) {
					skipped = appendNew(skipped, name) // the runner skips it too
				}
			}
		} else {
			mem, err := openMemory(ctx, a, cfg.legacy, clk)
			if err != nil {
				return fail(err)
			}
			if mem != nil {
				closers = append(closers, mem.close)
			}
			h, sk, ks, err := c.newHarness(ctx, cfg, a, built, admin, mem, clk)
			if err != nil {
				return fail(err)
			}
			def.Harness = h
			skipped = appendNew(skipped, sk...)
			skills.add(a, ks)
		}
		agents = append(agents, def)
	}
	if cfg.API.RunTimeout < 0 {
		return fail(errors.New("agent_api: negative limit"))
	}
	host, _ := os.Hostname()
	// The lease owner names this process in the store, for operators.
	runnerOpts := []runner.Option{runner.WithOwner(fmt.Sprintf("%s-%d", host, os.Getpid()))}
	if env.runLease > 0 {
		runnerOpts = append(runnerOpts, runner.WithLease(env.runLease))
	}
	var authOpts []agentapi.Option
	if authn != nil {
		authOpts = append(authOpts, agentapi.WithAuthenticator(authn))
	}
	if admin != nil {
		authOpts = append(authOpts, agentapi.WithToolsetAdmin(admin))
	}
	srv, err := agentapi.NewAgents(agents, append(authOpts,
		agentapi.WithStore(st),
		agentapi.WithBus(b),
		agentapi.WithRunnerOptions(runnerOpts...),
		agentapi.WithToolsets(c.toolsetCatalog(cfg, specs, built, problems, env)...),
		agentapi.WithMaxConcurrentRuns(cfg.API.MaxConcurrentRuns),
		agentapi.WithRunTimeout(time.Duration(cfg.API.RunTimeout)),
		agentapi.WithRetention(cfg.API.MaxRuns, cfg.API.MaxConversations, time.Duration(cfg.API.Retention)),
		agentapi.WithMaxBodyBytes(cfg.API.MaxBodyBytes),
		agentapi.WithClock(clk),
		agentapi.WithLogger(log.With("component", agentapi.Name)),
		// Runs still going near the end of the shutdown get a run_error
		// "shutdown" instead of a cut connection.
		agentapi.WithDrain(ctx.Done(), env.shutdownTimeout*9/10),
	)...)
	if err != nil {
		return fail(err)
	}
	ac := &agentComponent{Server: srv, skipped: skipped, skillReport: skills, runner: remote, runnerURL: env.runnerURL, agents: agentNames(cfg), toolsetsURL: env.toolsetsURL, admin: admin, closers: closers}
	for _, name := range slices.Sorted(maps.Keys(built)) {
		if r, ok := built[name].(remoteToolset); ok {
			ac.toolsets = r
			break
		}
	}
	return component.Mount{Pattern: "/v1/", Component: ac}, srv, nil
}
