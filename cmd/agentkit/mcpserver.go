package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

const mcpServerName = "mcp-server"

// mcpConfig is the "mcp" section of the serve config file.
type mcpConfig struct {
	// BasePath is where the toolsets are served: {base_path}/{name}.
	BasePath string `json:"base_path"`
	// Stateful turns MCP sessions on (default stateless). Replicas then need
	// session affinity, and open session streams hold a shutdown until its
	// deadline.
	Stateful bool `json:"stateful"`
	// CallTimeout limits each tool call; "0s" means no limit.
	CallTimeout duration `json:"call_timeout"`
	// MaxInFlight caps concurrent toolset requests (503 past it); 0 means no cap.
	MaxInFlight int `json:"max_in_flight"`
	// SessionTimeout closes idle sessions in stateful mode; "0s" means never.
	SessionTimeout duration `json:"session_timeout"`
	// MaxBodyBytes limits a request body; 0 means the SDK default (4 MiB).
	MaxBodyBytes int64 `json:"max_body_bytes"`
	// Toolsets are built by toolset.Build from the compiled-in catalog.
	Toolsets []toolset.Spec `json:"toolsets"`
}

// defaultMCPConfig is what an omitted field means.
func defaultMCPConfig() mcpConfig {
	return mcpConfig{
		BasePath:    "/mcp",
		CallTimeout: duration(30 * time.Second),
		MaxInFlight: 512,
	}
}

// mcpServer is the mcp-server component: the configured toolsets, one MCP
// endpoint each, under the base path.
type mcpServer struct {
	handler  http.Handler
	problems []toolset.Problem
}

// newMCPServer builds the MCP handler over the toolsets serve built: a
// required toolset that failed, or a mistake in the specs, has already failed
// startup; an optional one that failed is skipped and shown in /readyz.
func newMCPServer(ctx context.Context, cfg mcpConfig, regs map[string]tool.Registry, problems []toolset.Problem, log *slog.Logger) (component.Mount, error) {
	// The MCP SDK logs every stateless request at info; keep that for debug.
	srvLog := log
	if !log.Enabled(ctx, slog.LevelDebug) {
		srvLog = slog.New(levelFloor{log.Handler(), slog.LevelWarn})
	}
	opts := []server.Option{
		server.WithBasePath(cfg.BasePath),
		server.WithLogger(srvLog.With("component", mcpServerName)),
		server.WithCallTimeout(time.Duration(cfg.CallTimeout)),
		server.WithMaxInFlight(cfg.MaxInFlight),
		server.WithSessionTimeout(time.Duration(cfg.SessionTimeout)),
		server.WithMaxBodyBytes(cfg.MaxBodyBytes),
	}
	if cfg.Stateful {
		opts = append(opts, server.WithStateful())
	}
	srv, err := server.New(regs, opts...)
	if err != nil {
		return component.Mount{}, err
	}
	// Mounted without http.StripPrefix: the server routes on the full path.
	pattern := strings.TrimSuffix(cfg.BasePath, "/") + "/"
	return component.Mount{Pattern: pattern, Component: &mcpServer{handler: srv, problems: problems}}, nil
}

func (m *mcpServer) Name() string          { return mcpServerName }
func (m *mcpServer) Handler() http.Handler { return m.handler }

// Ready is nil once built, or degraded if optional toolsets were skipped:
// the others are served, so the instance still takes traffic.
func (m *mcpServer) Ready(context.Context) error {
	if len(m.problems) == 0 {
		return nil
	}
	reasons := make([]string, len(m.problems))
	for i, p := range m.problems {
		reasons[i] = p.Error()
	}
	return fmt.Errorf("%w: %s", component.ErrDegraded, strings.Join(reasons, "; "))
}

// Shutdown has nothing to release: the runtime has already drained the
// requests, and stateless endpoints keep no sessions.
func (m *mcpServer) Shutdown(context.Context) error { return nil }

// levelFloor drops records below min.
type levelFloor struct {
	slog.Handler
	min slog.Level
}

func (h levelFloor) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.min && h.Handler.Enabled(ctx, l)
}

func (h levelFloor) WithAttrs(attrs []slog.Attr) slog.Handler {
	return levelFloor{h.Handler.WithAttrs(attrs), h.min}
}

func (h levelFloor) WithGroup(name string) slog.Handler {
	return levelFloor{h.Handler.WithGroup(name), h.min}
}
