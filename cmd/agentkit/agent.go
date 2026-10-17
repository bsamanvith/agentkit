package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/harness"
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
	// MaxTurns limits model calls per run; MaxTotalTokens tokens per run (0:
	// no limit).
	MaxTurns       int `json:"max_turns"`
	MaxTotalTokens int `json:"max_total_tokens"`
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
	// Provider names the model adapter (modelProviders): "anthropic".
	Provider string `json:"provider"`
	// ID is the model ID.
	ID string `json:"id"`
	// Effort is low, medium, high, xhigh or max; empty for the adapter's
	// default (medium).
	Effort string `json:"effort"`
	// MaxTokens limits each reply's output; 0 for the adapter's default.
	MaxTokens int `json:"max_tokens"`
}

func defaultAgentDef() agentDef {
	return agentDef{
		Model:    agentModelConfig{Provider: "anthropic", ID: defaultModelID},
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
}

// runnerCheckTimeout bounds the call that checks the runner in /readyz.
const runnerCheckTimeout = 2 * time.Second

// Ready is degraded when optional toolsets the agents name were skipped, or
// when the runner the agents run on cannot be reached or does not run them:
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
	if len(a.skipped) > 0 {
		return fmt.Errorf("%w: toolsets skipped: %s", component.ErrDegraded, strings.Join(a.skipped, ", "))
	}
	return nil
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
	var agents []agentapi.Agent
	var skipped []string
	for _, a := range cfg.Agents {
		def := agentapi.Agent{
			Name: a.Name, Description: a.Description, System: a.System,
			Model:    wire.Model{Provider: a.Model.Provider, ID: a.Model.ID, Effort: a.Model.Effort},
			Toolsets: a.Toolsets, PrefixToolNames: a.PrefixToolNames,
		}
		if remote != nil {
			// The runner builds the model and the tools: this process
			// needs neither (the agentkit-api image has no model adapter).
			if err := a.check(cfg.legacy); err != nil {
				return component.Mount{}, nil, err
			}
			def.Runner = remote.Agent(a.Name)
			for _, name := range a.Toolsets {
				if slices.ContainsFunc(problems, func(p toolset.Problem) bool { return p.Toolset == name }) {
					skipped = appendNew(skipped, name) // the runner skips it too
				}
			}
		} else {
			h, sk, err := c.newHarness(ctx, cfg, a, built, clk)
			if err != nil {
				return component.Mount{}, nil, err
			}
			def.Harness = h
			skipped = appendNew(skipped, sk...)
		}
		agents = append(agents, def)
	}
	if cfg.API.RunTimeout < 0 {
		return component.Mount{}, nil, errors.New("agent_api: negative limit")
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
		return component.Mount{}, nil, err
	}
	return component.Mount{Pattern: "/v1/", Component: &agentComponent{Server: srv, skipped: skipped, runner: remote, runnerURL: env.runnerURL, agents: agentNames(cfg)}}, srv, nil
}

// check validates what of an agent's config does not need its model built.
func (d agentDef) check(legacy bool) error {
	label := d.label(legacy)
	if _, ok := modelProviders()[cmp.Or(d.Model.Provider, defaultProvider)]; !ok {
		return fmt.Errorf("%s.model.provider %q: want %s", label, d.Model.Provider, providerNames())
	}
	if d.MaxTotalTokens < 0 || d.Model.MaxTokens < 0 {
		return fmt.Errorf("%s: negative limit", label)
	}
	return nil
}

// newHarness builds an agent's harness: its model, its toolsets merged into
// one registry, and its limits. It returns the toolsets the agent names that
// were skipped (optional ones that failed to build).
func (c cli) newHarness(ctx context.Context, cfg serveConfig, a agentDef, built map[string]tool.Registry, clk clock.Clock) (*harness.Harness, []string, error) {
	label := a.label(cfg.legacy)
	if err := a.check(cfg.legacy); err != nil {
		return nil, nil, err
	}
	newModel := c.newModel
	if newModel == nil {
		newModel = buildModel
	}
	model, err := newModel(modelFlags{provider: a.Model.Provider, model: a.Model.ID, effort: a.Model.Effort})
	if err != nil {
		return nil, nil, fmt.Errorf("%s.model: %w", label, err)
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
		return nil, nil, fmt.Errorf("%s.toolsets: %w", label, err)
	}
	h, err := harness.New(model, tools,
		harness.WithSystem(a.System),
		harness.WithRequest(harness.RequestOptions{MaxTokens: a.Model.MaxTokens}),
		harness.WithMaxTurns(a.MaxTurns),
		harness.WithMaxTotalTokens(a.MaxTotalTokens),
		harness.WithNow(clk.Now),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", label, err)
	}
	return h, skipped, nil
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
