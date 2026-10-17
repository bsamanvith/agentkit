//go:build !nomcp

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// mcpServer is the mcp-server component: the configured toolsets, one MCP
// endpoint each, under the base path.
type mcpServer struct {
	handler  http.Handler
	problems []toolset.Problem
}

// newMCPServer builds the MCP handler over the toolsets serve built: a
// required toolset that failed, or a mistake in the specs, has already failed
// startup; an optional one that failed is skipped and shown in /readyz.
//
// Every request must carry token as a bearer token, the agent API's own
// ($AGENTKIT_API_TOKEN). Like the agent API, it may be empty only on a
// loopback addr: the toolsets call out with the server's keys and tokens.
func newMCPServer(ctx context.Context, cfg mcpConfig, regs map[string]tool.Registry, problems []toolset.Problem, token, addr string, log *slog.Logger) (component.Mount, error) {
	switch {
	case token == "" && !isLoopback(addr):
		return component.Mount{}, fmt.Errorf("serving on %s, which is not a loopback address, needs a bearer token in $%s", addr, envAPIToken)
	case token != "" && len(token) < agentapi.MinTokenLength:
		return component.Mount{}, fmt.Errorf("$%s must be at least %d bytes", envAPIToken, agentapi.MinTokenLength)
	}
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
	if token != "" {
		opts = append(opts, server.WithMiddleware(agentapi.RequireToken(token)))
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
