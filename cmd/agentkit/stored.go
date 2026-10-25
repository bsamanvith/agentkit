package main

import (
	"cmp"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agenttools"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Stored agents (T075): agents admins define through the API, as entries
// of "agents" in the config file's shape. The agent API keeps their
// versions (registry.go); whoever runs them builds them here: the agent API
// itself, or the runner component it runs its agents on (T132), from the
// definition each run carries (runnerrpc.WithStored).

// checkStored decodes a stored agent's definition and makes the checks
// serve makes of an agent of its file that need nothing built, and a few
// more: a stored agent has no skills or memory yet (both name paths on
// this machine), no UI or webhook of its own yet, and only process
// isolation, the only pools that run. runtime says whether it may use
// toolsets added at run time. A refusal is an *agentapi.DefinitionError
// naming the fields; cfg.legacy must be false, so errors name
// "agents.<name>".
func checkStored(cfg serveConfig, runtime bool, name string, raw json.RawMessage) (agentDef, pool.Isolation, error) {
	a := defaultAgentDef()
	if err := decodeStrict(raw, &a); err != nil {
		return agentDef{}, 0, decodeError(err)
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
	if len(a.CanMessage) > 0 {
		refuse("can_message", "an agent stored through the API cannot message other agents yet; define it in the config file")
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
	if len(a.RuntimeToolsets) > 0 && !runtime {
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
		return agentDef{}, 0, &agentapi.DefinitionError{Fields: fields}
	}
	return a, iso, nil
}

// buildStored builds the stored agent of raw here: checkStored, then its
// harness and its models, as serve builds an agent of its file.
func (c cli) buildStored(ctx context.Context, cfg serveConfig, built map[string]tool.Registry, admin toolsetAdmin, clk clock.Clock, log *slog.Logger, name string, raw json.RawMessage) (agentapi.Agent, error) {
	cfg.legacy = false // errors name "agents.<name>"
	a, iso, err := checkStored(cfg, admin != nil, name, raw)
	if err != nil {
		return agentapi.Agent{}, err
	}
	h, _, _, err := c.newHarness(ctx, cfg, a, built, admin, nil, nil, clk, log)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return agentapi.Agent{}, cerr
		}
		return agentapi.Agent{}, &agentapi.DefinitionError{Fields: []wire.FieldError{fieldError(a.label(cfg.legacy), err)}}
	}
	def := agentapi.Agent{
		Name: a.Name, Description: a.Description, System: a.System,
		Model:    wireModel(a.Model),
		Toolsets: a.Toolsets, PrefixToolNames: a.PrefixToolNames, RuntimeToolsets: a.RuntimeToolsets,
		ToolDescriptions: a.ToolDescriptions,
		Isolation:        iso, Harness: h,
	}
	set, err := a.modelSet(a.label(cfg.legacy)) // checked by check
	if err == nil {
		def, err = withModels(ctx, def, set, c.modelBuild(cfg, a, set, def, built, admin, nil, nil, clk, log))
	}
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return agentapi.Agent{}, cerr
		}
		return agentapi.Agent{}, &agentapi.DefinitionError{Fields: []wire.FieldError{fieldError(a.label(cfg.legacy), err)}}
	}
	return def, nil
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

// maxRunnerStoredBuilds is how many stored agents a runner keeps built,
// across tenants and versions: the agent API's registry keeps as many
// (registry.DefaultCacheSize) by default.
const maxRunnerStoredBuilds = 256

// runnerStored builds the stored agents a runner component runs for an
// agent API (runnerrpc.WithStored), keeping the most recently run: a run
// carries its version's definition, which never changes, so the
// definition's bytes are the key, whatever the tenant.
type runnerStored struct {
	build func(ctx context.Context, name string, raw json.RawMessage) (agentapi.Agent, error)
	size  int

	mu    sync.Mutex
	order *list.List // of *storedBuild, most recently used first
	built map[string]*list.Element
}

type storedBuild struct {
	key   string
	agent storedAgent
}

func newRunnerStored(size int, build func(ctx context.Context, name string, raw json.RawMessage) (agentapi.Agent, error)) *runnerStored {
	return &runnerStored{build: build, size: size, order: list.New(), built: map[string]*list.Element{}}
}

// Build is a runnerrpc.StoredBuilder.
func (s *runnerStored) Build(ctx context.Context, name string, raw json.RawMessage) (runner.Agent, error) {
	key := name + "\x00" + string(raw)
	s.mu.Lock()
	if e, ok := s.built[key]; ok {
		s.order.MoveToFront(e)
		s.mu.Unlock()
		return e.Value.(*storedBuild).agent, nil
	}
	s.mu.Unlock()
	a, err := s.build(ctx, name, raw)
	if err != nil {
		if de, ok := errors.AsType[*agentapi.DefinitionError](err); ok {
			return nil, toRunnerDefinition(de)
		}
		return nil, err
	}
	sa := storedAgent{Harness: a.Harness, forModel: a.ForModel}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.built[key]; ok { // built at the same time by another run
		s.order.MoveToFront(e)
		return e.Value.(*storedBuild).agent, nil
	}
	s.built[key] = s.order.PushFront(&storedBuild{key: key, agent: sa})
	if s.order.Len() > s.size {
		old := s.order.Remove(s.order.Back()).(*storedBuild)
		delete(s.built, old.key)
	}
	return sa, nil
}

// storedAgent is a stored agent a runner built: its harness on its
// default model, and on its others (runnerrpc.ModelAgent).
type storedAgent struct {
	*harness.Harness
	forModel func(ctx context.Context, model string) (agentapi.Agent, error)
}

func (a storedAgent) ForModel(ctx context.Context, model string) (runner.Agent, error) {
	if a.forModel == nil {
		return nil, errors.New("the agent runs only its default model")
	}
	on, err := a.forModel(ctx, model)
	if err != nil {
		return nil, err
	}
	return on.Harness, nil
}

// toRunnerDefinition and fromRunnerDefinition carry a refused definition's
// fields across the runner's wire.
func toRunnerDefinition(de *agentapi.DefinitionError) *runnerrpc.DefinitionError {
	out := &runnerrpc.DefinitionError{}
	for _, f := range de.Fields {
		out.Fields = append(out.Fields, runnerrpc.FieldError{Field: f.Field, Message: f.Message})
	}
	return out
}

func fromRunnerDefinition(de *runnerrpc.DefinitionError) *agentapi.DefinitionError {
	out := &agentapi.DefinitionError{}
	for _, f := range de.Fields {
		out.Fields = append(out.Fields, wire.FieldError{Field: f.Field, Message: f.Message})
	}
	return out
}
