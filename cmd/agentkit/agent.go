package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// envAPIToken is the bearer token of the agent API. Required when serving on
// an address that is not loopback.
const envAPIToken = "AGENTKIT_API_TOKEN"

// agentConfig is the "agent" section of the serve config file.
type agentConfig struct {
	Model agentModelConfig `json:"model"`
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
	// Provider is "anthropic", the only one so far.
	Provider string `json:"provider"`
	// ID is the model ID.
	ID string `json:"id"`
	// Effort is low, medium, high, xhigh or max; empty for the adapter's
	// default (medium).
	Effort string `json:"effort"`
	// MaxTokens limits each reply's output; 0 for the adapter's default.
	MaxTokens int `json:"max_tokens"`
}

func defaultAgentConfig() agentConfig {
	return agentConfig{
		Model:             agentModelConfig{Provider: "anthropic", ID: anthropic.DefaultModel},
		System:            "You are a helpful assistant. Use the tools when they help.",
		MaxTurns:          25,
		MaxConcurrentRuns: agentapi.DefaultMaxConcurrentRuns,
		RunTimeout:        duration(agentapi.DefaultRunTimeout),
		MaxRuns:           agentapi.DefaultMaxRuns,
		MaxConversations:  agentapi.DefaultMaxConversations,
		Retention:         duration(agentapi.DefaultRetention),
		MaxBodyBytes:      agentapi.DefaultMaxBodyBytes,
	}
}

// agentToolsetSpecs returns the specs of the toolsets the agent uses, from
// the mcp.toolsets entries, failing on a name that has none.
func agentToolsetSpecs(cfg serveConfig) ([]toolset.Spec, error) {
	var specs []toolset.Spec
	for _, name := range cfg.Agent.Toolsets {
		i := slices.IndexFunc(cfg.MCP.Toolsets, func(s toolset.Spec) bool { return s.Name == name })
		if i < 0 {
			return nil, fmt.Errorf("agent.toolsets names %q, which is not in mcp.toolsets", name)
		}
		specs = append(specs, cfg.MCP.Toolsets[i])
	}
	return specs, nil
}

// agentComponent is the agent API with the readiness of its toolsets.
type agentComponent struct {
	*agentapi.Server
	skipped []string
}

// Ready is degraded when optional toolsets the agent names were skipped.
func (a *agentComponent) Ready(ctx context.Context) error {
	if err := a.Server.Ready(ctx); err != nil {
		return err
	}
	if len(a.skipped) > 0 {
		return fmt.Errorf("%w: toolsets skipped: %s", component.ErrDegraded, strings.Join(a.skipped, ", "))
	}
	return nil
}

// newAgent builds the agent component: the model, the agent's toolsets merged
// into one registry, the harness, and the API on top, mounted at /v1/.
// ctx is the signal context: when it is done, the drain begins.
func (c cli) newAgent(ctx context.Context, cfg agentConfig, built map[string]tool.Registry, env serveEnv, log *slog.Logger) (component.Mount, error) {
	token := c.env(envAPIToken)
	if token == "" && !isLoopback(env.addr) {
		return component.Mount{}, fmt.Errorf("serving on %s, which is not a loopback address, needs a bearer token in $%s", env.addr, envAPIToken)
	}
	if cfg.Model.Provider != "anthropic" {
		return component.Mount{}, fmt.Errorf("agent.model.provider %q: the only provider is \"anthropic\"", cfg.Model.Provider)
	}
	if cfg.MaxTotalTokens < 0 || cfg.Model.MaxTokens < 0 || cfg.RunTimeout < 0 {
		return component.Mount{}, errors.New("agent: negative limit")
	}
	newModel := c.newModel
	if newModel == nil {
		newModel = newClaude
	}
	model, err := newModel(modelFlags{model: cfg.Model.ID, effort: cfg.Model.Effort})
	if err != nil {
		return component.Mount{}, fmt.Errorf("agent.model: %w", err)
	}
	sets := map[string]tool.Registry{}
	var skipped []string
	for _, name := range cfg.Toolsets {
		if reg, ok := built[name]; ok {
			sets[name] = reg
		} else {
			skipped = append(skipped, name) // an optional toolset that failed to build
		}
	}
	tools, err := agentapi.MergeTools(ctx, sets, cfg.PrefixToolNames)
	if err != nil {
		return component.Mount{}, fmt.Errorf("agent.toolsets: %w", err)
	}
	clk := clock.Real()
	h, err := harness.New(model, tools,
		harness.WithSystem(cfg.System),
		harness.WithRequest(harness.RequestOptions{MaxTokens: cfg.Model.MaxTokens}),
		harness.WithMaxTurns(cfg.MaxTurns),
		harness.WithMaxTotalTokens(cfg.MaxTotalTokens),
		harness.WithNow(clk.Now),
	)
	if err != nil {
		return component.Mount{}, err
	}
	srv, err := agentapi.New(h,
		agentapi.WithToken(token),
		agentapi.WithMaxConcurrentRuns(cfg.MaxConcurrentRuns),
		agentapi.WithRunTimeout(time.Duration(cfg.RunTimeout)),
		agentapi.WithRetention(cfg.MaxRuns, cfg.MaxConversations, time.Duration(cfg.Retention)),
		agentapi.WithMaxBodyBytes(cfg.MaxBodyBytes),
		agentapi.WithClock(clk),
		agentapi.WithLogger(log.With("component", agentapi.Name)),
		// Runs still going near the end of the shutdown get a run_error
		// "shutdown" instead of a cut connection.
		agentapi.WithDrain(ctx.Done(), env.shutdownTimeout*9/10),
	)
	if err != nil {
		return component.Mount{}, err
	}
	return component.Mount{Pattern: "/v1/", Component: &agentComponent{Server: srv, skipped: skipped}}, nil
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
