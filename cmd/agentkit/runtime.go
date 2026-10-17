package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// runtimeConfig is the "runtime_toolsets" section of the serve config file:
// MCP servers that admins (scope agent:admin) add as toolsets through the
// agent API, by URL, and review before agents use them (T096).
type runtimeConfig struct {
	// Enabled turns the API on; it is on unless false (and the build has
	// the MCP client).
	Enabled *bool `json:"enabled"`
	// AllowHosts are internal hosts a server's URL may name, which may then
	// resolve to a private address and use http: "mcp.internal", or
	// "*.corp.example.com" for its subdomains. Every other URL must be
	// https on a public address.
	AllowHosts []string `json:"allow_hosts"`
	// HeaderEnv names the variables of this process a header may use as
	// "${NAME}"; a header carrying a secret must use one.
	HeaderEnv []string `json:"header_env"`
	// MaxToolsets caps the toolsets each tenant may add (0: 50).
	MaxToolsets int `json:"max_toolsets"`
	// ConnectTimeout bounds connecting to a server and listing its tools
	// ("0s": 10s).
	ConnectTimeout duration `json:"connect_timeout"`
}

func defaultRuntimeConfig() runtimeConfig { return runtimeConfig{} }

// on reports whether the API is on.
func (r runtimeConfig) on() bool { return r.Enabled == nil || *r.Enabled }

// toolsetAdmin keeps the toolsets added at run time (package mcp/servers):
// the API, and the registry a harness offers their tools from.
type toolsetAdmin interface {
	agentapi.ToolsetAdmin
	Registry(names ...string) tool.Registry
	Close() error
}

// errNoRuntimeStore means the store cannot keep toolsets.
var errNoRuntimeStore = errors.New("runtime_toolsets: the store cannot keep toolsets")

// joinRegistries offers the tools of a, then those of b whose names a does
// not have.
func joinRegistries(a, b tool.Registry) tool.Registry { return joined{a, b} }

type joined struct{ a, b tool.Registry }

func (j joined) List(ctx context.Context) ([]tool.Definition, error) {
	first, err := j.a.List(ctx)
	if err != nil {
		return nil, err
	}
	second, err := j.b.List(ctx)
	if err != nil {
		return nil, err
	}
	out := first
	for _, d := range second {
		if !slices.ContainsFunc(first, func(f tool.Definition) bool { return f.Name == d.Name }) {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(x, y tool.Definition) int { return strings.Compare(x.Name, y.Name) })
	return out, nil
}

func (j joined) Invoke(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
	out, err := j.a.Invoke(ctx, name, input)
	if errors.Is(err, tool.ErrNotFound) {
		return j.b.Invoke(ctx, name, input)
	}
	return out, err
}
