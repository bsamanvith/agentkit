//go:build !nomcp && !noagent

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
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
func (c cli) newToolsetAdmin(cfg serveConfig, st agentapi.Store, log *slog.Logger) (toolsetAdmin, error) {
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
	if rc.MaxToolsets < 0 || rc.ConnectTimeout < 0 {
		return nil, errors.New("runtime_toolsets: negative limit")
	}
	if rc.MaxToolsets > 0 {
		opts = append(opts, servers.WithMaxToolsets(rc.MaxToolsets))
	}
	if rc.ConnectTimeout > 0 {
		opts = append(opts, servers.WithConnectTimeout(time.Duration(rc.ConnectTimeout)))
	}
	return servers.New(ts, opts...)
}
