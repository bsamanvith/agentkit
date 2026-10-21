package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agenttools"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// envAPIToken is the bearer token of the agent API. Required when serving on
// an address that is not loopback.
const envAPIToken = "AGENTKIT_API_TOKEN"

// agentConfig is the legacy "agent" section of the serve config file: one
// agent, named "default", and the API's limits in the same section.
type agentConfig struct {
	agentDef
	apiConfig
}

// agentDef is one agent: an entry of "agents" in the serve config file.
type agentDef struct {
	// Name names the agent in runs ("agent") and in /v1/agents. It must
	// match ^[a-zA-Z0-9_-]{1,64}$ and be unique.
	Name string `json:"name"`
	// Description says what the agent is for, in GET /v1/agents.
	Description string           `json:"description"`
	Model       agentModelConfig `json:"model"`
	// System is the system prompt.
	System string `json:"system"`
	// Toolsets names entries of mcp.toolsets the agent may use, merged into
	// one registry in process.
	Toolsets []string `json:"toolsets"`
	// PrefixToolNames names every tool "<toolset>__<tool>"; without it a tool
	// name in two toolsets fails startup.
	PrefixToolNames bool `json:"prefix_tool_names"`
	// RuntimeToolsets names toolsets admins add through the API (POST
	// /v1/toolsets) that the agent may use, in the tenant of each run, as
	// "<toolset>__<tool>" whatever PrefixToolNames says. A tenant that has
	// not added one gets none of its tools.
	RuntimeToolsets []string `json:"runtime_toolsets"`
	// Skills are folders of Agent Skills (pkg/skill) the agent may load:
	// their names and descriptions go in the system prompt, and the agent
	// gets skill_load and skill_read. A relative path is relative to the
	// config file's folder.
	Skills []skillSource `json:"skills"`
	// MaxTurns limits model calls per run; MaxTotalTokens tokens per run (0:
	// no limit).
	MaxTurns       int `json:"max_turns"`
	MaxTotalTokens int `json:"max_total_tokens"`
	// ToolPolicy decides the agent's tool calls: a preset's name or a
	// policy (package policy). Absent: "all". The environment's policy and a
	// run's narrow it.
	ToolPolicy *policy.Policy `json:"tool_policy"`
	// Interaction says whether the agent's tool calls may wait for people
	// (harness.WithInteraction): "interactive" or "supervised" (a tool that
	// asks, such as ask_user, and a call the policy wants approved suspend
	// the run until answered through POST /v1/runs/{id}/input), or
	// "autonomous", the default (they never wait: ask_user is not offered,
	// and a call that wants an approval is refused). A run's interaction
	// narrows it.
	Interaction harness.Interaction `json:"interaction"`
	// Memory gives the agent a context store and the "memory" tool over it
	// (memory.go); nil gives it neither.
	Memory *memoryConfig `json:"memory"`
}

// apiConfig is the "agent_api" section of the serve config file: limits of
// the agent API, shared by its agents.
type apiConfig struct {
	// MaxConcurrentRuns caps runs at once (503 with Retry-After past it).
	MaxConcurrentRuns int `json:"max_concurrent_runs"`
	// RunTimeout ends a longer run; "0s" means no limit.
	RunTimeout duration `json:"run_timeout"`
	// MaxRuns, MaxConversations and Retention bound what is kept in memory.
	MaxRuns          int      `json:"max_runs"`
	MaxConversations int      `json:"max_conversations"`
	Retention        duration `json:"retention"`
	// MaxBodyBytes limits the body of POST /v1/runs.
	MaxBodyBytes int64 `json:"max_body_bytes"`
}

// agentModelConfig chooses the model.
type agentModelConfig struct {
	// Provider names the model adapter (modelProviders): "anthropic", or
	// "openai" for any OpenAI-compatible server.
	Provider string `json:"provider"`
	// ID is the model ID: defaultModelID when empty for anthropic,
	// required for openai.
	ID string `json:"id"`
	// BaseURL is the server of an openai model, ending before
	// /chat/completions (http://localhost:11434/v1 for Ollama); empty for
	// $OPENAI_BASE_URL, or else OpenAI's API.
	BaseURL string `json:"base_url"`
	// APIKeyEnv names the environment variable holding an openai model's
	// API key (default OPENAI_API_KEY); an unset variable sends no key.
	APIKeyEnv string `json:"api_key_env"`
	// Effort is low, medium, high, xhigh or max; empty for the adapter's
	// default (medium).
	Effort string `json:"effort"`
	// MaxTokens limits each reply's output; 0 for the adapter's default.
	MaxTokens int `json:"max_tokens"`
	// ContextWindow is the model's context window in tokens (0:
	// defaultContextWindow), and CompactAt the share of it a request may
	// fill before the conversation is compacted ahead of its next run (0:
	// harness.DefaultCompactShare).
	ContextWindow int     `json:"context_window"`
	CompactAt     float64 `json:"compact_at"`
}

// providerOpenAI is the provider of OpenAI-compatible servers, and
// defaultOpenAIKeyEnv the variable its key comes from by default.
const (
	providerOpenAI      = "openai"
	defaultOpenAIKeyEnv = "OPENAI_API_KEY"
)

// id is the model ID, with the default for anthropic.
func (m agentModelConfig) id() string {
	if m.ID == "" && cmp.Or(m.Provider, defaultProvider) == defaultProvider {
		return defaultModelID
	}
	return m.ID
}

// flags are the model's settings for buildModel, with its key read through
// getenv.
func (m agentModelConfig) flags(getenv func(string) string) modelFlags {
	f := modelFlags{provider: m.Provider, model: m.id(), effort: m.Effort}
	if m.Provider == providerOpenAI {
		f.baseURL, f.apiKey = m.BaseURL, getenv(cmp.Or(m.APIKeyEnv, defaultOpenAIKeyEnv))
	}
	return f
}

// defaultContextWindow is the context window of Claude models.
const defaultContextWindow = 200_000

// window is the model's context window, for compaction.
func (m agentModelConfig) window() harness.ContextWindow {
	return harness.ContextWindow{Tokens: cmp.Or(m.ContextWindow, defaultContextWindow), Share: m.CompactAt}
}

func defaultAgentDef() agentDef {
	return agentDef{
		Model:    agentModelConfig{Provider: defaultProvider},
		System:   "You are a helpful assistant. Use the tools when they help.",
		MaxTurns: 25,
	}
}

func defaultAPIConfig() apiConfig {
	return apiConfig{
		MaxConcurrentRuns: agentapi.DefaultMaxConcurrentRuns,
		RunTimeout:        duration(agentapi.DefaultRunTimeout),
		MaxRuns:           agentapi.DefaultMaxRuns,
		MaxConversations:  agentapi.DefaultMaxConversations,
		Retention:         duration(agentapi.DefaultRetention),
		MaxBodyBytes:      agentapi.DefaultMaxBodyBytes,
	}
}

func defaultAgentConfig() agentConfig {
	return agentConfig{agentDef: defaultAgentDef(), apiConfig: defaultAPIConfig()}
}

// label names an agent's config section in errors: "agent" for the default
// agent of the legacy section or of no section, "agents.<name>" otherwise.
func (d agentDef) label(legacy bool) string {
	if legacy {
		return "agent"
	}
	return "agents." + d.Name
}

// agentToolsetSpecs returns the specs of the toolsets the agents use, each
// once, in the order the agents name them, from the mcp.toolsets entries,
// failing on a name that has none.
func agentToolsetSpecs(cfg serveConfig) ([]toolset.Spec, error) {
	var specs []toolset.Spec
	for _, a := range cfg.Agents {
		for _, name := range a.Toolsets {
			i := slices.IndexFunc(cfg.MCP.Toolsets, func(s toolset.Spec) bool { return s.Name == name })
			if i < 0 {
				return nil, fmt.Errorf("%s.toolsets names %q, which is not in mcp.toolsets", a.label(cfg.legacy), name)
			}
			if cfg.MCP.Toolsets[i].Type == agenttools.Type {
				// Agents calling agents is a flow's job, not this path's.
				return nil, fmt.Errorf("%s.toolsets names %q, of type %s: that toolset serves the agents to MCP clients, and agents cannot use it (to have agents call agents, use a flow)", a.label(cfg.legacy), name, agenttools.Type)
			}
			if !slices.ContainsFunc(specs, func(s toolset.Spec) bool { return s.Name == name }) {
				specs = append(specs, cfg.MCP.Toolsets[i])
			}
		}
	}
	return specs, nil
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

// closeAll calls each close func.
func closeAll(closers []func() error) error {
	var errs []error
	for _, c := range closers {
		errs = append(errs, c())
	}
	return errors.Join(errs...)
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

// degraded is the Ready error of a component whose agents skipped optional
// toolsets or skill folders: nil when none were skipped.
func degraded(toolsets []string, skills skillReport) error {
	var why []string
	if len(toolsets) > 0 {
		why = append(why, "toolsets skipped: "+strings.Join(toolsets, ", "))
	}
	if r := skills.reason(); r != "" {
		why = append(why, r)
	}
	if len(why) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", component.ErrDegraded, strings.Join(why, "; "))
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

// check validates what of an agent's config does not need its model built.
func (d agentDef) check(legacy bool) error {
	label := d.label(legacy)
	if _, ok := modelProviders()[cmp.Or(d.Model.Provider, defaultProvider)]; !ok {
		return fmt.Errorf("%s.model.provider %q: want %s", label, d.Model.Provider, providerNames())
	}
	if d.Model.Provider == providerOpenAI && d.Model.ID == "" {
		return fmt.Errorf("%s.model.id: provider %q needs the model ID the server knows (\"gpt-5-mini\", \"llama3.2\")", label, providerOpenAI)
	}
	if d.Model.Provider == providerOpenAI && d.Memory != nil && d.Memory.NativeTool {
		return fmt.Errorf("%s.memory.native_tool: Claude's native memory tool needs provider %q", label, defaultProvider)
	}
	if d.Model.Provider != providerOpenAI && (d.Model.BaseURL != "" || d.Model.APIKeyEnv != "") {
		return fmt.Errorf("%s.model: base_url and api_key_env are for provider %q", label, providerOpenAI)
	}
	if d.MaxTotalTokens < 0 || d.Model.MaxTokens < 0 || d.Model.ContextWindow < 0 {
		return fmt.Errorf("%s: negative limit", label)
	}
	if d.Model.CompactAt < 0 || d.Model.CompactAt > 1 {
		return fmt.Errorf("%s.model.compact_at %v: want a share above 0, at most 1", label, d.Model.CompactAt)
	}
	if d.Interaction != "" && !d.Interaction.Valid() {
		return fmt.Errorf("%s.interaction %q: want %q", label, d.Interaction, harness.Interactions())
	}
	return nil
}

// newHarness builds an agent's harness: its model, its toolsets merged into
// one registry with its memory and recall tools (mem, nil without memory),
// its skills, auto recall, and its limits. It returns the toolsets the agent names that were skipped
// (optional ones that failed to build) and the skills it loaded.
func (c cli) newHarness(ctx context.Context, cfg serveConfig, a agentDef, built map[string]tool.Registry, admin toolsetAdmin, mem *agentMemory, clk clock.Clock) (*harness.Harness, []string, agentSkills, error) {
	label := a.label(cfg.legacy)
	fail := func(err error) (*harness.Harness, []string, agentSkills, error) { return nil, nil, agentSkills{}, err }
	if err := a.check(cfg.legacy); err != nil {
		return fail(err)
	}
	newModel := c.newModel
	if newModel == nil {
		newModel = buildModel
	}
	f := a.Model.flags(c.env)
	f.nativeMemory = mem != nil && mem.native
	model, err := newModel(f)
	if err != nil {
		return fail(fmt.Errorf("%s.model: %w", label, err))
	}
	sets := map[string]tool.Registry{}
	var skipped []string
	for _, name := range a.Toolsets {
		if reg, ok := built[name]; ok {
			sets[name] = reg
		} else {
			skipped = appendNew(skipped, name) // an optional toolset that failed to build
		}
	}
	tools, err := agentapi.MergeTools(ctx, sets, a.PrefixToolNames)
	if err != nil {
		return fail(fmt.Errorf("%s.toolsets: %w", label, err))
	}
	system, tools, skills, err := addSkills(ctx, label, a.System, tools, a.Skills)
	if err != nil {
		return fail(err)
	}
	if mem != nil {
		defs, err := tools.List(ctx)
		if err != nil {
			return fail(fmt.Errorf("%s.toolsets: %w", label, err))
		}
		for _, name := range mem.names {
			if slices.ContainsFunc(defs, func(d tool.Definition) bool { return d.Name == name }) {
				return fail(fmt.Errorf("%s.memory: a toolset of the agent has a tool named %q, the name of a memory tool", label, name))
			}
		}
		tools = joinRegistries(mem.tools, tools)
	}
	if len(a.RuntimeToolsets) > 0 {
		tools = joinRegistries(tools, admin.Registry(a.RuntimeToolsets...))
	}
	toolsetOf, err := toolsetsOf(ctx, sets, a)
	if err != nil {
		return fail(fmt.Errorf("%s.toolsets: %w", label, err))
	}
	opts := []harness.Option{
		harness.WithToolPolicy(a.toolPolicy(cfg.Environment.ToolPolicy), toolsetOf),
		harness.WithInteraction(cmp.Or(a.Interaction, harness.Autonomous)),
		harness.WithSystem(system),
		harness.WithRequest(harness.RequestOptions{MaxTokens: a.Model.MaxTokens}),
		harness.WithMaxTurns(a.MaxTurns),
		harness.WithMaxTotalTokens(a.MaxTotalTokens),
		harness.WithContextWindow(a.Model.window()),
		harness.WithClock(clk),
	}
	if mem != nil && mem.prepare != nil {
		opts = append(opts, harness.WithPrepare(mem.prepare))
	}
	h, err := harness.New(model, tools, opts...)
	if err != nil {
		return fail(fmt.Errorf("%s: %w", label, err))
	}
	return h, skipped, skills, nil
}

// toolPolicy is the agent's tool policy narrowed by the environment's (nil
// for none). An agent without one gets "all": a tool that declares it
// requires approval still needs it.
func (d agentDef) toolPolicy(env *policy.Policy) policy.Policy {
	p := policy.MustPreset(policy.All)
	if d.ToolPolicy != nil {
		p = d.ToolPolicy.Clone()
		if p.Name == "" {
			p.Name = "agent"
		}
	}
	if env != nil {
		e := env.Clone()
		if e.Name == "" {
			e.Name = "environment"
		}
		p = policy.Narrow(p, e)
	}
	return p
}

// toolsetsOf names the toolset of each of an agent's tools, by the name the
// model calls it, for the rules of tool policies on toolsets. Prefixed
// names ("<toolset>__<tool>", which runtime toolsets always have) carry it;
// the others are looked up in the toolsets' lists.
func toolsetsOf(ctx context.Context, sets map[string]tool.Registry, a agentDef) (func(string) string, error) {
	owner := map[string]string{}
	if !a.PrefixToolNames {
		for name, reg := range sets {
			defs, err := reg.List(ctx)
			if err != nil {
				return nil, fmt.Errorf("toolset %q: list tools: %w", name, err)
			}
			for _, d := range defs {
				owner[d.Name] = name
			}
		}
	}
	// A toolset's name may itself contain the separator ("my__tools"): the
	// longest name that prefixes the tool's is its toolset.
	prefixed := slices.Concat(slices.Collect(maps.Keys(sets)), a.RuntimeToolsets)
	slices.SortFunc(prefixed, func(x, y string) int { return cmp.Compare(len(y), len(x)) })
	return func(tool string) string {
		if set, ok := owner[tool]; ok {
			return set
		}
		for _, set := range prefixed {
			if strings.HasPrefix(tool, set+agentapi.ToolPrefixSeparator) {
				return set
			}
		}
		return ""
	}, nil
}

// appendNew appends the names not already in list.
func appendNew(list []string, names ...string) []string {
	for _, n := range names {
		if !slices.Contains(list, n) {
			list = append(list, n)
		}
	}
	return list
}

// agentNames are the names of the config's agents, in order.
func agentNames(cfg serveConfig) []string {
	names := make([]string, len(cfg.Agents))
	for i, a := range cfg.Agents {
		names[i] = a.Name
	}
	return names
}

// toolsetCatalog describes the toolsets served, for GET /v1/toolsets: each
// built one with its tools, each skipped one with the reason, where the MCP
// server serves it (when it runs), and its credential's status. A reason
// never holds a credential's value: any is cut out.
func (c cli) toolsetCatalog(cfg serveConfig, specs []toolset.Spec, built map[string]tool.Registry, problems []toolset.Problem, env serveEnv) []agentapi.Toolset {
	var secrets []string
	out := make([]agentapi.Toolset, 0, len(specs))
	for _, s := range specs {
		cred := c.credentialOfServed(s, built, problems)
		if v := c.env(cred.Env); cred.Env != "" && v != "" {
			secrets = append(secrets, v)
		}
		ts := agentapi.Toolset{Name: s.Name, Type: s.Type, Tools: built[s.Name], Credential: cred}
		if ts.Tools == nil {
			ts.Skipped = "toolset " + s.Name + " was not built"
			if i := slices.IndexFunc(problems, func(p toolset.Problem) bool { return p.Toolset == s.Name }); i >= 0 {
				ts.Skipped = problems[i].Err.Error()
			}
		}
		if slices.Contains(env.components, mcpServerName) {
			ts.MCPPath = strings.TrimSuffix(cfg.MCP.BasePath, "/") + "/" + s.Name
		}
		out = append(out, ts)
	}
	for i := range out {
		for _, v := range secrets {
			out[i].Skipped = strings.ReplaceAll(out[i].Skipped, v, "[redacted]")
		}
	}
	return out
}

// credentialEnv is the environment variable each connector reads its key
// from by default; "api_key_env" in a toolset's settings names another. A
// connector not listed needs no credential.
// TODO: connectors should report their credential themselves (a factory
// method), so this table cannot fall behind them.
var credentialEnv = map[string]string{
	"tavily":   "TAVILY_API_KEY",
	"exa":      "EXA_API_KEY",
	"parallel": "PARALLEL_API_KEY",
	"calendar": "GOOGLE_ACCESS_TOKEN",
	"drive":    "GOOGLE_ACCESS_TOKEN",
}

// credentialOf says where a toolset's key comes from and whether it is set:
// the variable's name, never its value.
func credentialOf(s toolset.Spec, getenv func(string) string) agentapi.Credential {
	var settings struct {
		APIKeyEnv string `json:"api_key_env"`
		// The Google connectors name their variable access_token_env.
		AccessTokenEnv string `json:"access_token_env"`
		Auth           string `json:"auth"` // tavily: auto, key or keyless
	}
	_ = json.Unmarshal(s.Settings, &settings) // the factory validated them
	env := credentialEnv[s.Type]
	if settings.APIKeyEnv != "" {
		env = settings.APIKeyEnv
	}
	if settings.AccessTokenEnv != "" {
		env = settings.AccessTokenEnv
	}
	set := env != "" && getenv(env) != ""
	switch {
	case env == "":
		return agentapi.Credential{Status: wire.CredentialNotNeeded}
	case s.Type == "tavily" && settings.Auth == "keyless":
		return agentapi.Credential{Status: wire.CredentialKeyless}
	case set:
		return agentapi.Credential{Status: wire.CredentialConfigured, Env: env}
	case s.Type == "tavily" && settings.Auth != "key":
		return agentapi.Credential{Status: wire.CredentialKeyless, Env: env}
	default:
		return agentapi.Credential{Status: wire.CredentialMissing, Env: env}
	}
}

// isLoopback reports whether a listen address only takes connections from
// this machine. An empty host (":8080") listens on every interface.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
