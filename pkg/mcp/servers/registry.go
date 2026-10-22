package servers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/mcp/client"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// toolNameRE is the rule of tool names (package tool).
var toolNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// offered returns the tools of a toolset of ctx's tenant that agents may
// call now: approved, and listed by the server with the approved
// fingerprint. A disabled toolset offers none.
func (m *Manager) offered(ctx context.Context, name string) ([]offer, error) {
	_, r, err := m.load(ctx, name)
	if err != nil || !r.Enabled {
		return nil, err
	}
	cn, err := m.connFor(ctx, name, r)
	var ce *connectionError
	if errors.As(err, &ce) {
		// Not connected (or broken): the approved tools are offered as
		// approved, and their calls say so.
		return m.approvedOffers(name, r, ce), nil
	} else if err != nil {
		return nil, err
	}
	live := cn.listed()
	var out []offer
	for _, toolName := range slices.Sorted(maps.Keys(live)) {
		t, rv := live[toolName], r.Tools[toolName]
		if rv.Approved == "" || rv.Approved != t.fp {
			continue
		}
		def, err := client.Definition(t.def)
		if err != nil {
			m.log.Warn("mcp server: an approved tool cannot be offered", "toolset", name, "tool", toolName, "err", err)
			continue
		}
		if rv.Kind != "" {
			def.Kind = rv.Kind
		}
		if rv.Risk != "" {
			def.Risk = rv.Risk
		}
		out = append(out, offer{def: def, t: t, cn: cn})
	}
	return out, nil
}

// approvedOffers are the approved tools of an OAuth toolset the caller
// cannot use now, as approved, each failing with ce.
func (m *Manager) approvedOffers(name string, r record, ce *connectionError) []offer {
	var out []offer
	for _, toolName := range slices.Sorted(maps.Keys(r.Tools)) {
		rv := r.Tools[toolName]
		t := approvedTool(rv)
		if rv.Approved == "" || t == nil || t.Name != toolName {
			continue
		}
		def, err := client.Definition(t)
		if err != nil {
			m.log.Warn("mcp server: an approved tool cannot be offered", "toolset", name, "tool", toolName, "err", err)
			continue
		}
		if rv.Kind != "" {
			def.Kind = rv.Kind
		}
		if rv.Risk != "" {
			def.Risk = rv.Risk
		}
		out = append(out, offer{def: def, unusable: ce})
	}
	return out
}

// Registry is the tools of the named toolsets for a harness, as
// "<toolset>__<tool>", resolved in the tenant of each call's context: a
// tenant that has not added a toolset gets none of its tools, and a server
// that cannot be reached offers none (logged) rather than failing the run.
// Each listing reads the toolsets again, so a review, a change on the
// server or a removal takes effect at the next turn, and each call checks
// again that its tool is still offered.
func (m *Manager) Registry(names ...string) tool.Registry {
	return &registry{m: m, names: slices.Clone(names)}
}

type registry struct {
	m     *Manager
	names []string
}

// List implements [tool.Registry].
func (g *registry) List(ctx context.Context) ([]tool.Definition, error) {
	var out []tool.Definition
	for _, ts := range g.names {
		offered, err := g.m.offered(ctx, ts)
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.Is(err, agentapi.ErrToolsetNotFound):
			continue
		case err != nil:
			g.m.log.Warn("mcp server: its tools are left out", "toolset", ts, "err", err)
			continue
		}
		for _, o := range offered {
			d := o.def
			d.Name = ts + agentapi.ToolPrefixSeparator + d.Name
			if !toolNameRE.MatchString(d.Name) {
				g.m.log.Warn("mcp server: a tool's name is too long with its toolset's", "toolset", ts, "tool", o.def.Name)
				continue
			}
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b tool.Definition) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Invoke implements [tool.Registry].
func (g *registry) Invoke(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
	ts, own, ok := g.split(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q", tool.ErrNotFound, name)
	}
	offered, err := g.m.offered(ctx, ts)
	switch {
	case errors.Is(err, agentapi.ErrToolsetNotFound):
		return nil, fmt.Errorf("%w: %q", tool.ErrNotFound, name)
	case err != nil:
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, &tool.Error{Tool: name, Kind: tool.ErrToolFailed, Err: fmt.Errorf("the MCP server of toolset %s cannot be used just now", ts)}
	}
	i := slices.IndexFunc(offered, func(o offer) bool { return o.def.Name == own })
	if i < 0 {
		return nil, &tool.Error{Tool: name, Kind: tool.ErrToolFailed, Err: errors.New(
			"this tool is off: its toolset was disabled, or the MCP server changed or dropped the tool since an admin approved it; it stays off until reviewed")}
	}
	if ce := offered[i].unusable; ce != nil {
		return nil, callError(name, ce)
	}
	w, err := offered[i].cn.wrap(offered[i].t)
	if err != nil {
		return nil, &tool.Error{Tool: name, Kind: tool.ErrToolFailed, Err: err}
	}
	out, err := w.Invoke(ctx, input)
	if err != nil && ctx.Err() == nil {
		// A connection that broke during the call (its token refused and
		// not refreshed): said plainly, never with the server's answer.
		if _, r, lerr := g.m.load(ctx, ts); lerr == nil && r.Auth == wire.AuthOAuth {
			if ce, ok := asConnection(err, ts, r.Scope); ok {
				return nil, callError(name, ce)
			}
			// The transport may not keep the cause: the connection says it.
			if _, _, cerr := g.m.connected(ctx, ts, r); cerr != nil {
				if ce, ok := asConnection(cerr, ts, r.Scope); ok {
					return nil, callError(name, ce)
				}
			}
		}
	}
	return out, err
}

// split cuts "<toolset>__<tool>" for one of the registry's toolsets; a
// name could be cut two ways only if a toolset's name holds "__", so the
// longest toolset name that fits wins.
func (g *registry) split(name string) (ts, own string, ok bool) {
	for _, t := range g.names {
		if rest, found := strings.CutPrefix(name, t+agentapi.ToolPrefixSeparator); found && rest != "" && len(t) > len(ts) {
			ts, own, ok = t, rest, true
		}
	}
	return ts, own, ok
}
