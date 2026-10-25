//go:build !noagent

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/registry"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The agent registry (T075): agents admins define through the API, kept as
// versions in the store per tenant (stored.go). Only the agent component has
// it. It builds them here when the models are built in, or has the runner
// at $AGENTKIT_RUNNER_URL build and run them (T132): a build without models
// (the agentkit-api image) checks a definition, then asks the runner.

// registryBuilt: this build keeps agent definitions (registryConfig.on).
const registryBuilt = true

// newRegistry is the agent registry over st, nil when it is off. The
// agents of the file are read-only in it. With remote, the agents run on
// that runner, which builds the stored ones too.
func (c cli) newRegistry(cfg serveConfig, env serveEnv, st agentapi.Store, built map[string]tool.Registry, admin toolsetAdmin, remote *runnerrpc.Client, clk clock.Clock, log *slog.Logger) (agentapi.AgentRegistry, error) {
	rc := cfg.Registry
	explicit := rc.Enabled != nil && *rc.Enabled
	switch {
	case rc.CacheSize < 0:
		return nil, errors.New("agent_registry.cache_size: negative")
	case !rc.on(env) && explicit:
		return nil, fmt.Errorf("agent_registry.enabled: this agentkit is built without model adapters (-tags nomodels), so it builds stored agents only on a runner ($%s)", envRunnerURL)
	case !rc.on(env):
		return nil, nil
	}
	items, ok := st.(store.ItemStore)
	if !ok {
		if explicit {
			return nil, errors.New("agent_registry: the store cannot keep agent definitions")
		}
		return nil, nil
	}
	opts := []registry.Option{
		registry.WithReadOnly(agentNames(cfg)...),
		registry.WithLogger(log.With("component", agentapi.Name)),
	}
	if rc.CacheSize > 0 {
		opts = append(opts, registry.WithCacheSize(rc.CacheSize))
	}
	build := func(ctx context.Context, name string, raw json.RawMessage) (agentapi.Agent, error) {
		return c.buildStored(ctx, cfg, built, admin, clk, log, name, raw)
	}
	if remote != nil {
		build = remoteStored(cfg, remote)
	}
	return registry.New(items, build, opts...)
}

// remoteStored builds stored agents that run on the runner remote: it
// checks a definition as buildStored does, has the runner build it (the
// runner refuses what does not build, by field), and returns the agent as
// the runner runs it, each run carrying the definition.
func remoteStored(cfg serveConfig, remote *runnerrpc.Client) registry.Builder {
	cfg.legacy = false // errors name "agents.<name>"
	return func(ctx context.Context, name string, raw json.RawMessage) (agentapi.Agent, error) {
		// The runner reads no toolsets added at run time yet (T096).
		a, iso, err := checkStored(cfg, false, name, raw)
		if err != nil {
			return agentapi.Agent{}, err
		}
		raw = slices.Clone(raw) // the runs keep it
		if err := remote.CheckStored(ctx, name, raw); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return agentapi.Agent{}, cerr
			}
			if de, ok := errors.AsType[*runnerrpc.DefinitionError](err); ok {
				return agentapi.Agent{}, fromRunnerDefinition(de)
			}
			return agentapi.Agent{}, fmt.Errorf("the runner at $%s: %w", envRunnerURL, err)
		}
		def := agentapi.Agent{
			Name: a.Name, Description: a.Description, System: a.System,
			Model:    wireModel(a.Model),
			Toolsets: a.Toolsets, PrefixToolNames: a.PrefixToolNames,
			ToolDescriptions: a.ToolDescriptions,
			Isolation:        iso,
			Runner:           remote.StoredAgent(name, raw, a.Model.window()),
		}
		set, err := a.modelSet(a.label(cfg.legacy)) // checked by checkStored
		if err != nil {
			return agentapi.Agent{}, &agentapi.DefinitionError{Fields: []wire.FieldError{fieldError(a.label(cfg.legacy), err)}}
		}
		return withModels(ctx, def, set, func(_ context.Context, m string) (agentapi.Agent, error) {
			d := a.onModel(set, m)
			on := def
			on.Model, on.System, on.Runner = wireModel(d.Model), d.System, remote.StoredModelAgent(name, m, raw, d.Model.window())
			return on, nil
		})
	}
}
