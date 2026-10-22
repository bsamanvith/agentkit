package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agenttools"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/web"
)

// Environment read by agentkit serve.
const (
	envComponents      = "AGENTKIT_COMPONENTS"       // comma-separated; default mcp-server
	envAddr            = "AGENTKIT_ADDR"             // listen address; default 127.0.0.1:8080
	envLogLevel        = "AGENTKIT_LOG_LEVEL"        // debug, info, warn or error; default info
	envConfig          = "AGENTKIT_CONFIG"           // config file, if -config is not given
	envShutdownTimeout = "AGENTKIT_SHUTDOWN_TIMEOUT" // e.g. 15s; default component.DefaultShutdownTimeout
	// envRunLease is how long a run's lease lasts unrenewed: how soon the run
	// of a process that died shows as interrupted, or is resumed by another
	// process. Default 30s.
	envRunLease = "AGENTKIT_RUN_LEASE"
)

// knownComponents are the names AGENTKIT_COMPONENTS accepts.
var knownComponents = []string{mcpServerName, agentapi.Name, web.Name, runnerName, toolsetsName, a2aName}

// serveConfig is the config file of agentkit serve, as loadServeConfig reads
// it. The file is JSON; YAML (with the same keys) comes with pkg/config.
//
// The file names its agents in "agents", each with its own model, system
// prompt and toolsets, and the API's limits in "agent_api". A file with one
// "agent" section instead (the form before several agents) serves that agent
// as "default", with the limits in the same section.
type serveConfig struct {
	MCP mcpConfig
	// Agents are the agents, in the file's order: one named "default" when
	// the file has an "agent" section or neither section.
	Agents []agentDef
	API    apiConfig
	// Auth holds the API keys of the agent API and the MCP endpoints.
	Auth authConfig
	// Runtime is how admins add MCP servers as toolsets through the API.
	Runtime runtimeConfig
	// Environment is what this deployment imposes on every agent.
	Environment environmentConfig
	// Credentials is where the credential vault keeps its sealed blobs.
	Credentials credentialsConfig
	legacy      bool // Agents come from an "agent" section (or none)
}

// environmentConfig is the "environment" section of the config file.
type environmentConfig struct {
	// ToolPolicy narrows every agent's tool policy: a preset's name (an
	// ephemeral environment might say "read_only") or a policy.
	ToolPolicy *policy.Policy `json:"tool_policy"`
}

// serveFile is the top level of the config file.
type serveFile struct {
	MCP      json.RawMessage   `json:"mcp"`
	Agent    json.RawMessage   `json:"agent"`
	Agents   []json.RawMessage `json:"agents"`
	AgentAPI json.RawMessage   `json:"agent_api"`
	Auth     json.RawMessage   `json:"auth"`
	Runtime  json.RawMessage   `json:"runtime_toolsets"`
	Env      json.RawMessage   `json:"environment"`
	// Credentials is the credential vault's backend (credentials.go).
	Credentials json.RawMessage `json:"credentials"`
}

// serveEnv is what agentkit serve reads from the environment.
type serveEnv struct {
	components      []string
	addr            string
	logLevel        slog.Level
	shutdownTimeout time.Duration
	runLease        time.Duration // 0 for the runner's default
	// The split deployment (split.go): where the agents run and where the
	// toolsets come from, empty for in process; the token between them.
	runnerURL, toolsetsURL, rpcToken string
	dependencyWait                   time.Duration
	// credentialKeys seal the credential vault ($AGENTKIT_CREDENTIALS_KEY);
	// nil when unset. Parsed at startup so a bad ring fails there.
	credentialKeys *credentials.KeyRing
	// vault is the credential vault over the config's credentials backend
	// (credentials.go), opened at startup: nil without a key ring. The
	// toolsets that use it come with T044.
	vault credentials.Vault
}

func (c cli) serve(ctx context.Context, args []string) error {
	fs := c.newFlagSet("agentkit serve", "")
	configPath := fs.String("config", c.env(envConfig), "config file (JSON) with the toolsets to serve (default $"+envConfig+")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errUsage
	}
	env, err := c.serveEnv()
	if err != nil {
		return err
	}
	cfg, err := loadServeConfig(*configPath)
	if err != nil {
		return err
	}
	vault, closeVault, err := c.openVault(ctx, cfg.Credentials, env.credentialKeys)
	if err != nil {
		return err
	}
	defer func() { _ = closeVault() }()
	env.vault = vault
	log := slog.New(slog.NewJSONHandler(c.stderr, &slog.HandlerOptions{Level: env.logLevel}))
	built, problems, err := c.buildToolsets(ctx, env, cfg, *configPath, log)
	if err != nil {
		return err
	}

	// The agent API's store, and the recovery loop over it, which resumes
	// the background runs a process left (chat runs stay interrupted for
	// their user). It stops first, then the server drains, then the store
	// closes.
	clk := clock.Real()
	var (
		agents *agentapi.Server
		st     openedStore
		b      bus.Bus
	)
	if slices.Contains(env.components, agentapi.Name) {
		if st, err = c.openStore(ctx, clk); err != nil {
			return err
		}
		defer st.close()
		var closeBus func()
		if b, closeBus, err = c.openBus(ctx, clk, log); err != nil {
			return err
		}
		defer closeBus()
	}

	// One authenticator for the agent API and the MCP endpoints, so both
	// accept the same keys and see the same principals.
	var authn auth.Authenticator
	if slices.Contains(env.components, agentapi.Name) || slices.Contains(env.components, mcpServerName) {
		if authn, err = c.authenticator(ctx, cfg, log); err != nil {
			return err
		}
		if authn == nil {
			if !isLoopback(env.addr) {
				return needsCredentials(env.addr)
			}
			log.Warn("serving with no authentication: anyone who can reach " + env.addr + " can use the API and the toolsets")
		}
	}

	// The wiring: one constructor per component, chosen by name. The MCP
	// server comes last, at its place in the list: its agentkit toolsets
	// call the agent API built here.
	var mounts []component.Mount
	mcpAt := -1
	for _, name := range env.components {
		var m component.Mount
		switch name {
		case mcpServerName:
			mcpAt = len(mounts)
		case agentapi.Name:
			m, agents, err = c.newAgents(ctx, cfg, built, problems, env, authn, st.Store, b, clk, log)
		case web.Name:
			m, err = newWeb()
		case runnerName:
			m, err = c.newRunner(ctx, cfg, built, env, clk, log)
		case toolsetsName:
			m, err = c.newToolsets(cfg, built, problems, env, log)
		case a2aName:
			continue // over the agent API, below
		default: // in knownComponents but not wired here
			err = errors.New("component not wired in cmd/agentkit")
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		mounts = append(mounts, m)
	}
	if mcpAt >= 0 {
		mcp := cfg.MCP
		mcp.protected = cfg.Auth.JWT
		var agentAPI http.Handler // the agentkit toolsets call it in process
		if agents != nil {
			agentAPI = agents.Handler()
		}
		m, err := newMCPServer(ctx, mcp, built, problems, authn, agentAPI, env.addr, log)
		if err != nil {
			return fmt.Errorf("%s: %w", mcpServerName, err)
		}
		mounts[mcpAt] = m
		meta, err := newResourceMetadata(mcp)
		if err != nil {
			_ = m.Component.Shutdown(ctx)
			return fmt.Errorf("%s: %w", mcpServerName, err)
		}
		mounts = append(mounts, meta...)
	}
	if slices.Contains(env.components, a2aName) {
		m, err := c.newA2A(agents, cfg, authn, log)
		if err != nil {
			return fmt.Errorf("%s: %w", a2aName, err)
		}
		mounts = append(m, mounts...) // shut down after the agent API (newA2A)
	}
	rt, err := component.New(mounts,
		component.WithLogger(log),
		component.WithBuildInfo(component.BuildInfo{Version: version, Commit: commit, Date: date}),
		component.WithShutdownTimeout(env.shutdownTimeout),
	)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", env.addr)
	if err != nil {
		return err
	}
	if agents == nil {
		return rt.Serve(ctx, ln)
	}
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

// buildToolsets is cli.buildToolsets with the compiled-in catalog.
func buildToolsets(ctx context.Context, env serveEnv, cfg serveConfig, configPath string, log *slog.Logger) (map[string]tool.Registry, []toolset.Problem, error) {
	return cli{}.buildToolsets(ctx, env, cfg, configPath, log)
}

// servedSpecs returns the toolsets the components use: every one in
// mcp.toolsets when mcp-server or toolsets runs, else those the agents name
// (agent-api, runner).
func servedSpecs(env serveEnv, cfg serveConfig) ([]toolset.Spec, error) {
	var specs []toolset.Spec
	for _, name := range []string{agentapi.Name, runnerName} {
		if slices.Contains(env.components, name) {
			s, err := agentToolsetSpecs(cfg)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			specs = s
		}
	}
	for _, name := range []string{mcpServerName, toolsetsName} {
		if slices.Contains(env.components, name) {
			if len(cfg.MCP.Toolsets) == 0 {
				return nil, fmt.Errorf("%s: no toolsets configured (mcp.toolsets in the config file)", name)
			}
			rest, _, err := splitAgentTools(cfg.MCP.Toolsets)
			if err != nil {
				return nil, err
			}
			specs = rest
		}
	}
	return specs, nil
}

// splitAgentTools splits the agentkit toolsets (the agents over MCP) from
// the others. Only the mcp-server component builds them, once the agent API
// they call exists (newMCPServer); no agent uses them. A name may not be
// used twice across both.
func splitAgentTools(specs []toolset.Spec) (rest, agentTools []toolset.Spec, err error) {
	for _, s := range specs {
		if s.Type == agenttools.Type {
			agentTools = append(agentTools, s)
		} else {
			rest = append(rest, s)
		}
	}
	for _, a := range agentTools {
		n := 0
		for _, s := range specs {
			if s.Name == a.Name {
				n++
			}
		}
		if n > 1 {
			return nil, nil, &toolset.Error{Toolset: a.Name, Type: a.Type, Err: toolset.ErrDuplicateName}
		}
	}
	return rest, agentTools, nil
}

// needsToolsetConfig names a component in env that serves every toolset of
// the config file, so cannot run without one; empty if none does.
func needsToolsetConfig(env serveEnv) string {
	for _, name := range []string{mcpServerName, toolsetsName} {
		if slices.Contains(env.components, name) {
			return name
		}
	}
	return ""
}

// buildToolsets builds, once, the toolsets the components use
// (servedSpecs): the components then share the same instances. Optional
// toolsets that fail are logged and returned as problems. With
// $AGENTKIT_TOOLSETS_URL they come from the toolsets component there
// instead (remoteToolsets).
func (c cli) buildToolsets(ctx context.Context, env serveEnv, cfg serveConfig, configPath string, log *slog.Logger) (map[string]tool.Registry, []toolset.Problem, error) {
	if name := needsToolsetConfig(env); configPath == "" && name != "" {
		return nil, nil, fmt.Errorf("%s needs a config file with its toolsets: -config or $%s", name, envConfig)
	}
	specs, err := servedSpecs(env, cfg)
	if err != nil {
		return nil, nil, err
	}
	if len(specs) == 0 {
		return map[string]tool.Registry{}, nil, nil
	}
	if env.toolsetsURL != "" {
		return c.remoteToolsets(ctx, env, specs, log)
	}
	catalog, err := c.toolsets()
	if err != nil {
		return nil, nil, err
	}
	regs, problems, err := toolset.Build(ctx, catalog, specs)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range problems {
		log.Warn("optional toolset skipped", "toolset", p.Toolset, "type", p.Type, "err", p.Err)
	}
	return regs, problems, nil
}

func (c cli) serveEnv() (serveEnv, error) {
	e := serveEnv{addr: "127.0.0.1:8080", shutdownTimeout: component.DefaultShutdownTimeout}
	list := c.env(envComponents)
	if strings.TrimSpace(list) == "" {
		list = mcpServerName
	}
	for name := range strings.SplitSeq(list, ",") {
		name = strings.TrimSpace(name)
		switch {
		case name == "":
		case !slices.Contains(knownComponents, name):
			return e, fmt.Errorf("unknown component %q in %s (known: %s)", name, envComponents, strings.Join(knownComponents, ", "))
		case slices.Contains(e.components, name):
			return e, fmt.Errorf("component %q listed twice in %s", name, envComponents)
		default:
			e.components = append(e.components, name)
		}
	}
	if v := c.env(envAddr); v != "" {
		e.addr = v
	}
	if v := c.env(envLogLevel); v != "" {
		if err := e.logLevel.UnmarshalText([]byte(v)); err != nil {
			return e, fmt.Errorf("%s: %w", envLogLevel, err)
		}
	}
	if v := c.env(envShutdownTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return e, fmt.Errorf("%s: %q is not a positive duration", envShutdownTimeout, v)
		}
		e.shutdownTimeout = d
	}
	e.runnerURL, e.toolsetsURL, e.rpcToken = c.env(envRunnerURL), c.env(envToolsetsURL), c.env(envRPCToken)
	switch {
	case e.rpcToken != "" && len(e.rpcToken) < 16:
		return e, fmt.Errorf("%s: the token must be at least 16 bytes", envRPCToken)
	case e.runnerURL != "" && slices.Contains(e.components, runnerName):
		return e, fmt.Errorf("%s runs the %s component here and names one at $%s; set one or the other", envComponents, runnerName, envRunnerURL)
	case e.toolsetsURL != "" && slices.Contains(e.components, toolsetsName):
		return e, fmt.Errorf("%s runs the %s component here and names one at $%s; set one or the other", envComponents, toolsetsName, envToolsetsURL)
	}
	e.dependencyWait = defaultDependencyWait
	if v := c.env(envDependencyWait); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return e, fmt.Errorf("%s: %q is not a duration", envDependencyWait, v)
		}
		e.dependencyWait = d
	}
	if v := c.env(envRunLease); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			return e, fmt.Errorf("%s: %q is not a duration of 1s or more", envRunLease, v)
		}
		e.runLease = d
	}
	if v := c.env(envCredentialsKey); v != "" {
		r, err := credentials.ParseKeyRing(v)
		if err != nil {
			return e, fmt.Errorf("$%s: %w", envCredentialsKey, err)
		}
		e.credentialKeys = r
	}
	return e, nil
}

// envCredentialsKey is the credential vault's key ring:
// "<id>:<base64 of 32 bytes>[,<id>:<base64>...]", the first key sealing.
const envCredentialsKey = "AGENTKIT_CREDENTIALS_KEY"

// loadServeConfig reads the config file; an empty path gives the defaults.
// Unknown fields are errors, so a typo cannot silently change what is served.
func loadServeConfig(path string) (serveConfig, error) {
	cfg := serveConfig{MCP: defaultMCPConfig(), API: defaultAPIConfig(), Runtime: defaultRuntimeConfig(), legacy: true}
	def := defaultAgentDef()
	def.Name = agentapi.DefaultAgent
	cfg.Agents = []agentDef{def}
	if path == "" {
		return cfg, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	var file serveFile
	if err := decodeStrict(raw, &file); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	if err := decodeSection(file.MCP, &cfg.MCP); err != nil {
		return cfg, fmt.Errorf("config %s: mcp: %w", path, err)
	}
	if err := decodeSection(file.Runtime, &cfg.Runtime); err != nil {
		return cfg, fmt.Errorf("config %s: runtime_toolsets: %w", path, err)
	}
	if err := decodeSection(file.Auth, &cfg.Auth); err != nil {
		return cfg, fmt.Errorf("config %s: auth: %w", path, err)
	}
	if err := decodeSection(file.Env, &cfg.Environment); err != nil {
		return cfg, fmt.Errorf("config %s: environment: %w", path, err)
	}
	if err := decodeSection(file.Credentials, &cfg.Credentials); err != nil {
		return cfg, fmt.Errorf("config %s: credentials: %w", path, err)
	}
	switch {
	case file.Agent != nil && (file.Agents != nil || file.AgentAPI != nil):
		return cfg, fmt.Errorf("config %s: \"agent\" is the one-agent form; with \"agents\" (and \"agent_api\"), leave it out", path)
	case file.Agent != nil:
		legacy := defaultAgentConfig()
		if err := decodeSection(file.Agent, &legacy); err != nil {
			return cfg, fmt.Errorf("config %s: agent: %w", path, err)
		}
		if legacy.Name == "" {
			legacy.Name = agentapi.DefaultAgent
		}
		cfg.Agents, cfg.API = []agentDef{legacy.agentDef}, legacy.apiConfig
	case file.Agents != nil:
		cfg.legacy = false
		if err := decodeSection(file.AgentAPI, &cfg.API); err != nil {
			return cfg, fmt.Errorf("config %s: agent_api: %w", path, err)
		}
		if len(file.Agents) == 0 {
			return cfg, fmt.Errorf("config %s: agents is empty", path)
		}
		cfg.Agents = nil
		for i, raw := range file.Agents {
			a := defaultAgentDef()
			if err := decodeSection(raw, &a); err != nil {
				return cfg, fmt.Errorf("config %s: agents[%d]: %w", path, i, err)
			}
			if !validAgentName.MatchString(a.Name) {
				return cfg, fmt.Errorf("config %s: agents[%d].name %q: want ^[a-zA-Z0-9_-]{1,64}$", path, i, a.Name)
			}
			if slices.ContainsFunc(cfg.Agents, func(b agentDef) bool { return b.Name == a.Name }) {
				return cfg, fmt.Errorf("config %s: agents[%d].name %q: two agents have that name", path, i, a.Name)
			}
			cfg.Agents = append(cfg.Agents, a)
		}
	case file.AgentAPI != nil:
		if err := decodeSection(file.AgentAPI, &cfg.API); err != nil {
			return cfg, fmt.Errorf("config %s: agent_api: %w", path, err)
		}
	}
	resolveSkillPaths(cfg.Agents, path)
	return cfg, nil
}

// validAgentName is what agents[].name must match: it goes in URL paths.
var validAgentName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// decodeSection decodes a section of the config file over the defaults in v;
// an absent section leaves them.
func decodeSection(raw json.RawMessage, v any) error {
	if raw == nil {
		return nil
	}
	return decodeStrict(raw, v)
}

// decodeStrict decodes one JSON value, refusing unknown fields and trailing
// data.
func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON object")
	}
	return nil
}

// duration is a time.Duration written as a string in config ("30s").
type duration time.Duration

func (d *duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"30s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = duration(v)
	return nil
}
