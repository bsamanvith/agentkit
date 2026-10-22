//go:build !noagent && !nomodels

package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agenttools"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/registry"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// The agent registry (T075): agents admins define through the API, kept as
// versions in the store per tenant, built here as serve builds the agents
// of its file. Only the agent component has it, and only where the models
// are built in (registry_none.go): a build without them runs its agents on
// a runner, which builds only the agents of its file.

// registryBuilt: this build keeps agent definitions (registryConfig.on).
const registryBuilt = true

// newRegistry is the agent registry over st, nil when it is off. The
// agents of the file are read-only in it.
func (c cli) newRegistry(cfg serveConfig, env serveEnv, st agentapi.Store, built map[string]tool.Registry, admin toolsetAdmin, clk clock.Clock, log *slog.Logger) (agentapi.AgentRegistry, error) {
	rc := cfg.Registry
	explicit := rc.Enabled != nil && *rc.Enabled
	switch {
	case rc.CacheSize < 0:
		return nil, errors.New("agent_registry.cache_size: negative")
	case !rc.on(env) && explicit:
		return nil, fmt.Errorf("agent_registry.enabled: the agents run on the runner at $%s, which builds only the agents of its config file", envRunnerURL)
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
	reg, err := registry.New(items, c.storedAgentBuilder(cfg, built, admin, clk, log), opts...)
	if err != nil {
		return nil, err
	}
	return reg, nil
}

// storedAgentBuilder builds a stored agent's definition, an entry of
// "agents" in the config file's shape, with the checks serve makes of
// those, and a few more: a stored agent has no skills or memory yet (both
// name paths on this machine), no UI or webhook of its own yet, and only
// process isolation, the only pools that run. A refusal is an *agentapi.DefinitionError naming the fields.
func (c cli) storedAgentBuilder(cfg serveConfig, built map[string]tool.Registry, admin toolsetAdmin, clk clock.Clock, log *slog.Logger) registry.Builder {
	cfg.legacy = false // errors name "agents.<name>"
	return func(ctx context.Context, name string, raw json.RawMessage) (agentapi.Agent, error) {
		a := defaultAgentDef()
		if err := decodeStrict(raw, &a); err != nil {
			return agentapi.Agent{}, decodeError(err)
		}
		a.Name = name
		var fields []wire.FieldError
		refuse := func(field, format string, args ...any) {
			fields = append(fields, wire.FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
		}
		if len(a.Skills) > 0 {
			refuse("skills", "an agent stored through the API cannot have skills yet; define it in the config file")
		}
		if a.Memory != nil {
			refuse("memory", "an agent stored through the API cannot have memory yet; define it in the config file")
		}
		if a.UI != "" {
			refuse("ui", "an agent stored through the API cannot show UI yet; define it in the config file")
		}
		if a.Notify != nil {
			refuse("notify", "an agent stored through the API cannot have a webhook yet; name one per task (notify in POST /v1/tasks)")
		}
		for _, ts := range a.Toolsets {
			i := slices.IndexFunc(cfg.MCP.Toolsets, func(s toolset.Spec) bool { return s.Name == ts })
			switch {
			case i < 0:
				refuse("toolsets", "%q is not a toolset of this server (GET /v1/toolsets lists them; runtime_toolsets names those added through the API)", ts)
			case cfg.MCP.Toolsets[i].Type == agenttools.Type:
				refuse("toolsets", "%q, of type %s, serves the agents to MCP clients, and agents cannot use it", ts, agenttools.Type)
			}
		}
		if len(a.RuntimeToolsets) > 0 && admin == nil {
			refuse("runtime_toolsets", "toolsets added at run time are off on this server")
		}
		for _, ts := range a.RuntimeToolsets {
			if slices.ContainsFunc(cfg.MCP.Toolsets, func(s toolset.Spec) bool { return s.Name == ts }) {
				refuse("runtime_toolsets", "%q is a toolset of the config file; list it in toolsets", ts)
			}
		}
		// An openai model's server and key variable: the operator's
		// ($OPENAI_BASE_URL, OPENAI_API_KEY), or a pair an agent of the
		// file uses. Any other would send the value of any variable of the
		// server (its keys, the store's URL) to any server the admin names.
		keyEnv := cmp.Or(a.Model.APIKeyEnv, defaultOpenAIKeyEnv)
		if a.Model.BaseURL != "" || keyEnv != defaultOpenAIKeyEnv {
			if !slices.ContainsFunc(cfg.Agents, func(f agentDef) bool {
				return f.Model.Provider == providerOpenAI && f.Model.BaseURL == a.Model.BaseURL &&
					cmp.Or(f.Model.APIKeyEnv, defaultOpenAIKeyEnv) == keyEnv
			}) {
				field := "model.base_url"
				if a.Model.BaseURL == "" {
					field = "model.api_key_env"
				}
				refuse(field, "an agent stored through the API uses $OPENAI_BASE_URL and $%s, or the base_url and api_key_env of an agent of the config file", defaultOpenAIKeyEnv)
			}
		}
		iso, err := pool.ParseIsolation(a.Isolation)
		switch {
		case err != nil:
			refuse("isolation", "%v", err)
		case iso > pool.Process:
			refuse("isolation", "%s: only process pools run yet", iso)
		}
		if err := a.check(cfg.legacy); err != nil {
			fields = append(fields, fieldError(a.label(cfg.legacy), err))
		}
		if len(fields) > 0 {
			return agentapi.Agent{}, &agentapi.DefinitionError{Fields: fields}
		}
		h, _, _, err := c.newHarness(ctx, cfg, a, built, admin, nil, clk, log)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return agentapi.Agent{}, cerr
			}
			return agentapi.Agent{}, &agentapi.DefinitionError{Fields: []wire.FieldError{fieldError(a.label(cfg.legacy), err)}}
		}
		return agentapi.Agent{
			Name: a.Name, Description: a.Description, System: a.System,
			Model:    wire.Model{Provider: a.Model.Provider, ID: a.Model.id(), Effort: a.Model.Effort},
			Toolsets: a.Toolsets, PrefixToolNames: a.PrefixToolNames, RuntimeToolsets: a.RuntimeToolsets,
			Isolation: iso, Harness: h,
		}, nil
	}
}

// fieldError turns an error serve gives for an agent of its file
// ("agents.x.model.provider \"y\": want ..."), into the field it names
// ("model.provider") and the rest; one it cannot place is about the
// definition as a whole.
func fieldError(label string, err error) wire.FieldError {
	msg := err.Error()
	rest, ok := strings.CutPrefix(msg, label)
	if !ok {
		return wire.FieldError{Message: msg}
	}
	if r, ok := strings.CutPrefix(rest, "."); ok {
		if i := strings.IndexAny(r, " :"); i > 0 {
			return wire.FieldError{Field: r[:i], Message: strings.TrimLeft(r[i:], ": ")}
		}
	}
	return wire.FieldError{Message: strings.TrimLeft(rest, ": ")}
}

// decodeError is a definition that does not decode, with the field when
// the decoder names one.
func decodeError(err error) *agentapi.DefinitionError {
	f := wire.FieldError{Message: err.Error()}
	var te *json.UnmarshalTypeError
	switch {
	case errors.As(err, &te) && te.Field != "":
		f.Field = te.Field
		f.Message = fmt.Sprintf("want %s, not a JSON %s", te.Type, te.Value)
	case strings.HasPrefix(f.Message, `json: unknown field "`):
		f.Field = strings.TrimSuffix(strings.TrimPrefix(f.Message, `json: unknown field "`), `"`)
		f.Message = "unknown field"
	}
	return &agentapi.DefinitionError{Fields: []wire.FieldError{f}}
}
