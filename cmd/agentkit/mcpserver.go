//go:build !nomcp

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agenttools"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/resourcemeta"
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
	// agentTools builds the agentkit toolsets, nil when there are none: its
	// Close stops following the runs their agent_run left going.
	agentTools *agenttools.Factory
}

// newMCPServer builds the MCP handler over the toolsets serve built: a
// required toolset that failed, or a mistake in the specs, has already failed
// startup; an optional one that failed is skipped and shown in /readyz.
//
// Every request must authenticate with authn, the agent API's own (the API
// keys and $AGENTKIT_API_TOKEN), and sees only the toolsets its principal may
// use: another answers 404, as one that does not exist. authn may be nil only
// on a loopback addr: the toolsets call out with the server's keys and
// tokens.
//
// The agentkit toolsets (agents over MCP) are built here: they call
// agentAPI, the agent API of this process, unless their settings name a URL
// (agentAPI is nil when the agent component runs elsewhere). Each call
// carries its caller's credential to the API, which checks it again.
func newMCPServer(ctx context.Context, cfg mcpConfig, regs map[string]tool.Registry, problems []toolset.Problem, authn auth.Authenticator, agentAPI http.Handler, addr string, log *slog.Logger) (component.Mount, error) {
	switch {
	case authn == nil && !isLoopback(addr):
		return component.Mount{}, needsCredentials(addr)
	case authn != nil && cfg.Stateful:
		// A session would outlive the request that authenticated it, and
		// could be used by another principal: not until sessions are bound
		// to one (T040 Notes).
		return component.Mount{}, errors.New("mcp.stateful cannot be used with authentication yet: sessions are not bound to a principal")
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
	_, specs, err := splitAgentTools(cfg.Toolsets)
	if err != nil {
		return component.Mount{}, err
	}
	var agentTools *agenttools.Factory
	if len(specs) > 0 {
		var aregs map[string]tool.Registry
		var aprobs []toolset.Problem
		agentTools, aregs, aprobs, err = buildAgentTools(ctx, cfg, specs, agentAPI, log)
		if err != nil {
			return component.Mount{}, err
		}
		regs, problems = maps.Clone(regs), slices.Clone(problems)
		maps.Copy(regs, aregs)
		problems = append(problems, aprobs...)
	}
	if authn != nil {
		mw := auth.Middleware(authn, auth.WithLogger(log.With("component", mcpServerName)))
		if cfg.protected != nil {
			// A 401 names the metadata of the endpoint asked for, whose
			// resource is that endpoint's URL, as MCP clients check.
			mw = resourcemeta.Middleware(authn, cfg.protected.Audience, []string{mcpPrefix(cfg)}, auth.WithLogger(log.With("component", mcpServerName)))
		}
		if agentTools != nil {
			// Outside the authentication, which removes the header: the
			// agentkit tools send the caller's credential on to the API.
			authenticate := mw
			mw = func(h http.Handler) http.Handler { return agenttools.ForwardCredentials(authenticate(h)) }
		}
		opts = append(opts,
			server.WithMiddleware(mw),
			server.WithAccess(func(ctx context.Context, name string) bool {
				p, ok := auth.From(ctx)
				return ok && p.AllowsToolset(name)
			}),
		)
	}
	srv, err := server.New(regs, opts...)
	if err != nil {
		if agentTools != nil {
			_ = agentTools.Close()
		}
		return component.Mount{}, err
	}
	// Mounted without http.StripPrefix: the server routes on the full path.
	return component.Mount{Pattern: mcpPrefix(cfg), Component: &mcpServer{handler: srv, problems: problems, agentTools: agentTools}}, nil
}

// buildAgentTools builds the agentkit toolsets of specs: in process over
// agentAPI, or over the URL their settings give. agent_run returns before
// the MCP call timeout cuts it off.
func buildAgentTools(ctx context.Context, cfg mcpConfig, specs []toolset.Spec, agentAPI http.Handler, log *slog.Logger) (*agenttools.Factory, map[string]tool.Registry, []toolset.Problem, error) {
	opts := []agenttools.Option{
		agenttools.WithCallTimeout(time.Duration(cfg.CallTimeout)),
		agenttools.WithLogger(log.With("component", mcpServerName)),
	}
	if agentAPI != nil {
		opts = append(opts, agenttools.WithHandler(agentAPI))
	}
	f, err := agenttools.New(opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	catalog, err := toolset.NewCatalog(f)
	if err != nil {
		_ = f.Close()
		return nil, nil, nil, err
	}
	regs, problems, err := toolset.Build(ctx, catalog, specs)
	if err != nil {
		_ = f.Close()
		if agentAPI == nil && errors.Is(err, toolset.ErrInvalidSettings) {
			err = fmt.Errorf("%w (the %s component does not run in this process: give the toolset the agent API's URL in settings.url, or run both)", err, agentapi.Name)
		}
		return nil, nil, nil, err
	}
	for _, p := range problems {
		log.Warn("optional toolset skipped", "toolset", p.Toolset, "type", p.Type, "err", p.Err)
	}
	return f, regs, problems, nil
}

// mcpPrefix is the path prefix of the toolsets' endpoints: "/mcp/".
func mcpPrefix(cfg mcpConfig) string { return strings.TrimSuffix(cfg.BasePath, "/") + "/" }

// resourceMetadataName names the components that serve the MCP endpoints'
// protected resource metadata.
const resourceMetadataName = "oauth-protected-resource"

// newResourceMetadata serves the protected resource metadata (RFC 9728) of
// the MCP endpoints when auth.jwt is configured: at
// /.well-known/oauth-protected-resource for the server, and at that path
// followed by an endpoint's path (/mcp/crm) for each endpoint. The resource
// is auth.jwt's audience, which must then be the server's origin. With no
// auth.jwt it serves nothing.
func newResourceMetadata(cfg mcpConfig) ([]component.Mount, error) {
	j := cfg.protected
	if j == nil {
		return nil, nil
	}
	h, err := resourcemeta.Handler(resourcemeta.Config{
		Resource:             j.Audience,
		AuthorizationServers: j.authorizationServers(),
		ScopesSupported:      j.ScopesSupported,
		Paths:                []string{mcpPrefix(cfg)},
	})
	if err != nil {
		return nil, fmt.Errorf("auth.jwt: the MCP endpoints' metadata: %w (the audience must be the server's origin, such as https://agents.example.com)", err)
	}
	return []component.Mount{
		{Pattern: resourcemeta.WellKnown, Component: &resourceMetadata{name: resourceMetadataName, h: h}},
		{Pattern: resourcemeta.WellKnown + "/", Component: &resourceMetadata{name: resourceMetadataName + "-endpoints", h: h}},
	}, nil
}

// resourceMetadata is a component that serves the metadata: public, always
// ready, nothing to shut down.
type resourceMetadata struct {
	name string
	h    http.Handler
}

func (m *resourceMetadata) Name() string                   { return m.name }
func (m *resourceMetadata) Handler() http.Handler          { return m.h }
func (m *resourceMetadata) Ready(context.Context) error    { return nil }
func (m *resourceMetadata) Shutdown(context.Context) error { return nil }

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

// Shutdown stops following the runs the agentkit toolsets' agent_run left
// going; nothing else to release: the runtime has already drained the
// requests, and stateless endpoints keep no sessions.
func (m *mcpServer) Shutdown(context.Context) error {
	if m.agentTools != nil {
		return m.agentTools.Close()
	}
	return nil
}

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
