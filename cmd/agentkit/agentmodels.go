package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm/modelset"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// knownModels are the model names the server suggests per provider (GET
// /v1/models), for pickers and to show which an agent's patterns allow. A
// provider serves more than these; a pattern may allow them. A new
// provider adds its line with its row of modelProviders.
var knownModels = map[string][]string{
	defaultProvider: {
		"anthropic/claude-fable-5-1",
		"anthropic/claude-opus-5-5",
		"anthropic/claude-sonnet-5-5",
		"anthropic/claude-haiku-5-5",
	},
	providerOpenAI: {},
	// The Gemini API's text models with function calling (2026-10).
	providerGemini: {
		"gemini/gemini-3.1-pro-preview",
		"gemini/gemini-3.8-flash",
		"gemini/gemini-3.5-flash",
		"gemini/gemini-3.5-flash-lite",
	},
}

// modelCatalog is knownModels for the providers this binary builds, for
// agentapi.WithModels.
func modelCatalog() []wire.ModelProvider {
	var out []wire.ModelProvider
	for _, p := range slices.Sorted(maps.Keys(modelProviders())) {
		out = append(out, wire.ModelProvider{Name: p, Models: slices.Clone(knownModels[p])})
	}
	return out
}

// name is the model's name, "<provider>/<id>".
func (m agentModelConfig) name() string {
	return cmp.Or(m.Provider, defaultProvider) + "/" + m.id()
}

// modelSet is the agent's models: its model, the default, and what its
// models section allows. A problem names its field under label.
func (d agentDef) modelSet(label string) (*modelset.Set, error) {
	set, err := modelset.New(d.Model.name(), d.Models, func(p string) bool {
		_, ok := modelProviders()[p]
		return ok
	})
	var errs modelset.Errors
	if errors.As(err, &errs) && len(errs) > 0 {
		f := errs[0]
		if f.Field == "default" {
			return nil, fmt.Errorf("%s.model: %s", label, f.Message)
		}
		return nil, fmt.Errorf("%s.models.%s: %s", label, f.Field, f.Message)
	}
	return set, err
}

// onModel is the agent as it runs on model (canonical): its model config
// for that model, and its variant's changes. A model of another provider
// starts from that provider's defaults: no base_url, key variable or tool
// search of the agent's own.
func (d agentDef) onModel(set *modelset.Set, model string) agentDef {
	p, id, _ := modelset.Parse(model)
	if p != cmp.Or(d.Model.Provider, defaultProvider) {
		d.Model.BaseURL, d.Model.APIKeyEnv, d.Model.ToolSearch = "", "", ""
	}
	d.Model.Provider, d.Model.ID = p, id
	v, ok := set.Variant(model)
	if !ok {
		return d
	}
	switch {
	case v.System != "":
		d.System = v.System
	case v.SystemAppend != "":
		d.System += "\n\n" + v.SystemAppend
	}
	if v.Effort != "" {
		d.Model.Effort = v.Effort
	}
	if v.MaxTokens != 0 {
		d.Model.MaxTokens = v.MaxTokens
	}
	if len(v.ToolDescriptions) > 0 {
		descs := maps.Clone(d.ToolDescriptions)
		if descs == nil {
			descs = map[string]string{}
		}
		maps.Copy(descs, v.ToolDescriptions)
		d.ToolDescriptions = descs
	}
	return d
}

// maxPatternBuilds is how many models an agent keeps built beyond those it
// allows by name: models only its patterns allow, built when a run first
// names one. Past it, the oldest is built again when named again.
const maxPatternBuilds = 16

// modelBuilds builds an agent on each of its models and keeps what it
// built: once per model per agent version, as the registry keeps the
// agent itself.
type modelBuilds struct {
	set   *modelset.Set
	build func(ctx context.Context, model string) (agentapi.Agent, error)

	mu    sync.Mutex
	built map[string]agentapi.Agent
	order []string // models built for a pattern, oldest first
}

// newModelBuilds builds the agent on every model its set allows by name
// but the default, failing on the first that does not build, so that a
// definition is refused as a whole.
func newModelBuilds(ctx context.Context, set *modelset.Set, build func(ctx context.Context, model string) (agentapi.Agent, error)) (*modelBuilds, error) {
	b := &modelBuilds{set: set, build: build, built: map[string]agentapi.Agent{}}
	for _, m := range set.Names()[1:] {
		a, err := build(ctx, m)
		if err != nil {
			return nil, err
		}
		b.built[m] = a
	}
	return b, nil
}

// forModel is agentapi.Agent.ForModel.
func (b *modelBuilds) forModel(ctx context.Context, model string) (agentapi.Agent, error) {
	if !b.set.Allows(model) {
		return agentapi.Agent{}, fmt.Errorf("%w: %q", modelset.ErrNotAllowed, model)
	}
	b.mu.Lock()
	a, ok := b.built[model]
	b.mu.Unlock()
	if ok {
		return a, nil
	}
	a, err := b.build(ctx, model)
	if err != nil {
		return agentapi.Agent{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if prev, ok := b.built[model]; ok {
		return prev, nil // built at the same time by another run
	}
	b.built[model] = a
	b.order = append(b.order, model)
	if len(b.order) > maxPatternBuilds {
		delete(b.built, b.order[0])
		b.order = b.order[1:]
	}
	return a, nil
}

// withModels gives def (the agent on its default model) its models: the
// set, and ForModel over build. An agent with one model gets the set
// alone.
func withModels(ctx context.Context, def agentapi.Agent, set *modelset.Set, build func(ctx context.Context, model string) (agentapi.Agent, error)) (agentapi.Agent, error) {
	def.Models = set
	if set.Single() {
		return def, nil
	}
	b, err := newModelBuilds(ctx, set, build)
	if err != nil {
		return agentapi.Agent{}, err
	}
	def.ForModel = b.forModel
	return def, nil
}

// modelHarness is an agent of the runner component on its default model
// that runs its other models too (runnerrpc.ModelAgent).
type modelHarness struct {
	*harness.Harness
	builds *modelBuilds
}

func (h modelHarness) ForModel(ctx context.Context, model string) (runner.Agent, error) {
	a, err := h.builds.forModel(ctx, model)
	if err != nil {
		return nil, err
	}
	return a.Harness, nil
}

// wireModel is the model config as GET /v1/agents shows it.
func wireModel(m agentModelConfig) wire.Model {
	return wire.Model{Provider: m.Provider, ID: m.id(), Effort: m.Effort}
}

// modelBuild builds agent a, served as def on its default model, on
// another of its models: the harness newHarness builds for a.onModel. An
// error names the model under the agent's models.
func (c cli) modelBuild(cfg serveConfig, a agentDef, set *modelset.Set, def agentapi.Agent, built map[string]tool.Registry, admin toolsetAdmin, mem *agentMemory, msg tool.Registry, clk clock.Clock, log *slog.Logger) func(context.Context, string) (agentapi.Agent, error) {
	return func(ctx context.Context, model string) (agentapi.Agent, error) {
		d := a.onModel(set, model)
		h, _, _, err := c.newHarness(ctx, cfg, d, built, admin, mem, msg, clk, log)
		if err != nil {
			return agentapi.Agent{}, fmt.Errorf("%s.models: on %s: %w", a.label(cfg.legacy), model, err)
		}
		on := def
		on.Model, on.System, on.Harness = wireModel(d.Model), d.System, h
		return on, nil
	}
}
