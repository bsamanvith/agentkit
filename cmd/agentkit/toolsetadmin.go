//go:build !nomcp && !noagent

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/mcp/oauth"
	"github.com/bsamanvith/agentkit/pkg/mcp/registry"
	"github.com/bsamanvith/agentkit/pkg/mcp/servers"
	"github.com/bsamanvith/agentkit/pkg/netguard"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// The toolsets admins add at run time (T096) are the agent API's: a build
// without MCP (toolsetadmin_none.go) or without the agent component has
// none.

// errNoRuntimeStore means the store cannot keep toolsets.
var errNoRuntimeStore = errors.New("runtime_toolsets: the store cannot keep toolsets")

// newToolsetAdmin is the keeper of the toolsets added at run time, on st;
// nil when they are off.
func (c cli) newToolsetAdmin(cfg serveConfig, st agentapi.Store, env serveEnv, log *slog.Logger) (toolsetAdmin, error) {
	rc := cfg.Runtime
	if !rc.on() {
		return nil, nil
	}
	ts, ok := st.(store.ToolsetStore)
	if !ok {
		return nil, errNoRuntimeStore
	}
	guard, err := netguard.New(rc.AllowHosts)
	if err != nil {
		return nil, fmt.Errorf("runtime_toolsets.allow_hosts: %w", err)
	}
	opts := []servers.Option{
		servers.WithGuard(guard),
		servers.WithHeaderEnv(rc.HeaderEnv, func(name string) (string, bool) { v := c.env(name); return v, v != "" }),
		servers.WithLogger(log.With("component", agentapi.Name)),
	}
	tenants := map[string][]string{}
	for id, t := range rc.Tenants {
		tenants[id] = t.MCPServers.Allow
	}
	allow, err := servers.NewAllowlist(rc.MCPServers.Allow, tenants)
	if err != nil {
		return nil, fmt.Errorf("runtime_toolsets: %w", err)
	}
	opts = append(opts, servers.WithAllowlist(allow))
	if rc.Registry.on() {
		url := rc.Registry.URL
		if url == "" {
			url = registry.DefaultURL
		}
		reg, err := registry.New(url)
		if err != nil {
			return nil, fmt.Errorf("runtime_toolsets.registry.url: %w", err)
		}
		opts = append(opts, servers.WithRegistry(reg))
	}
	if rc.MaxToolsets < 0 || rc.ConnectTimeout < 0 {
		return nil, errors.New("runtime_toolsets: negative limit")
	}
	if rc.MaxToolsets > 0 {
		opts = append(opts, servers.WithMaxToolsets(rc.MaxToolsets))
	}
	if rc.ConnectTimeout > 0 {
		opts = append(opts, servers.WithConnectTimeout(time.Duration(rc.ConnectTimeout)))
	}
	var conn *oauth.Connector
	if env.vaultParts != nil && env.publicURL != "" {
		// MCP OAuth (T095): tokens and registered clients in the vault,
		// every request through the same guard as the servers'.
		vs := oauth.NewVaultStore(env.vaultParts.vault)
		conn, err = oauth.New(oauth.Config{
			PublicURL: env.publicURL, Tokens: vs, Clients: vs,
			HTTPClient: guard.Client(nil),
			Logger:     log.With("component", agentapi.Name),
		})
		if err != nil {
			return nil, fmt.Errorf("$%s: %w", envPublicURL, err)
		}
		opts = append(opts, servers.WithOAuth(conn))
	}
	m, err := servers.New(ts, opts...)
	if err != nil || conn == nil {
		return m, err
	}
	return oauthAdmin{m, conn.Handler()}, nil
}

// oauthAdmin is the toolset admin with MCP OAuth on: the connect flows'
// slots (Dynamic) and the client's metadata document.
type oauthAdmin struct {
	*servers.Manager
	meta http.Handler
}

// ClientMetadata serves the client's metadata document (its CIMD client
// ID), at the root of the public URL.
func (a oauthAdmin) ClientMetadata() http.Handler { return a.meta }
