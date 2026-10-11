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
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// Environment read by agentkit serve.
const (
	envComponents      = "AGENTKIT_COMPONENTS"       // comma-separated; default mcp-server
	envAddr            = "AGENTKIT_ADDR"             // listen address; default 127.0.0.1:8080
	envLogLevel        = "AGENTKIT_LOG_LEVEL"        // debug, info, warn or error; default info
	envConfig          = "AGENTKIT_CONFIG"           // config file, if -config is not given
	envShutdownTimeout = "AGENTKIT_SHUTDOWN_TIMEOUT" // e.g. 15s; default component.DefaultShutdownTimeout
)

// knownComponents are the names AGENTKIT_COMPONENTS accepts.
var knownComponents = []string{mcpServerName, agentapi.Name}

// serveConfig is the config file of agentkit serve. It is JSON; YAML (with
// the same keys) comes with pkg/config.
type serveConfig struct {
	MCP   mcpConfig   `json:"mcp"`
	Agent agentConfig `json:"agent"`
}

// serveEnv is what agentkit serve reads from the environment.
type serveEnv struct {
	components      []string
	addr            string
	logLevel        slog.Level
	shutdownTimeout time.Duration
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
	log := slog.New(slog.NewJSONHandler(c.stderr, &slog.HandlerOptions{Level: env.logLevel}))
	built, problems, err := buildToolsets(ctx, env, cfg, *configPath, log)
	if err != nil {
		return err
	}

	// The wiring: one constructor per component, chosen by name.
	var mounts []component.Mount
	for _, name := range env.components {
		var m component.Mount
		switch name {
		case mcpServerName:
			m, err = newMCPServer(ctx, cfg.MCP, built, problems, log)
		case agentapi.Name:
			m, err = c.newAgent(ctx, cfg.Agent, built, env, log)
		default: // in knownComponents but not wired here
			err = errors.New("component not wired in cmd/agentkit")
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		mounts = append(mounts, m)
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
	return rt.Serve(ctx, ln)
}

// buildToolsets builds, once, the toolsets the components use: every one in
// mcp.toolsets when mcp-server runs, else those the agent names. Both
// components then share the same instances. Optional toolsets that fail are
// logged and returned as problems.
func buildToolsets(ctx context.Context, env serveEnv, cfg serveConfig, configPath string, log *slog.Logger) (map[string]tool.Registry, []toolset.Problem, error) {
	var specs []toolset.Spec
	if slices.Contains(env.components, agentapi.Name) {
		s, err := agentToolsetSpecs(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", agentapi.Name, err)
		}
		specs = s
	}
	if slices.Contains(env.components, mcpServerName) {
		if configPath == "" {
			return nil, nil, fmt.Errorf("%s needs a config file with its toolsets: -config or $%s", mcpServerName, envConfig)
		}
		if len(cfg.MCP.Toolsets) == 0 {
			return nil, nil, fmt.Errorf("%s: no toolsets configured (mcp.toolsets in the config file)", mcpServerName)
		}
		specs = cfg.MCP.Toolsets
	}
	if len(specs) == 0 {
		return map[string]tool.Registry{}, nil, nil
	}
	catalog, err := newCatalog()
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
	return e, nil
}

// loadServeConfig reads the config file; an empty path gives the defaults.
// Unknown fields are errors, so a typo cannot silently change what is served.
func loadServeConfig(path string) (serveConfig, error) {
	cfg := serveConfig{MCP: defaultMCPConfig(), Agent: defaultAgentConfig()}
	if path == "" {
		return cfg, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("config %s: trailing data after the JSON object", path)
	}
	return cfg, nil
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
