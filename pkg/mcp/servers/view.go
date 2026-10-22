package servers

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/mcp/client"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// show is view, with the caller's connection to a toolset with auth
// "oauth".
func (m *Manager) show(ctx context.Context, rec store.ToolsetRecord, r record, cn *conn, connErr error) wire.ServerToolset {
	v := m.view(rec, r, cn, connErr)
	v.Issuer, v.OAuthClientID = r.Issuer, r.OAuthClientID
	v.Connection = m.connectionStatus(ctx, rec.Name, r)
	return v
}

// view is a toolset as the API shows it: its connection cn, or why there
// is none (connErr), decides the status of its tools.
func (m *Manager) view(rec store.ToolsetRecord, r record, cn *conn, connErr error) wire.ServerToolset {
	v := wire.ServerToolset{
		V: agentapi.WireVersion, Name: rec.Name, Type: Type, URL: r.URL, Transport: r.Transport, Auth: r.Auth,
		HeaderNames: slices.Sorted(maps.Keys(r.Headers)), Scope: r.Scope, Enabled: r.Enabled, Server: r.Server,
		Tools: []wire.ReviewTool{}, Version: rec.Version, CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
	}
	var live map[string]*listedTool
	if connErr == nil && cn != nil {
		live = cn.listed()
		if cn.server.Name != "" {
			v.Server = cn.server
		}
	}
	names := map[string]bool{}
	for name := range live {
		names[name] = true
	}
	for name, rv := range r.Tools {
		if rv.Approved != "" || connErr != nil {
			names[name] = true
		}
	}
	review := false
	for _, name := range slices.Sorted(maps.Keys(names)) {
		rt := reviewTool(name, live[name], r.Tools[name], connErr != nil)
		if rt.Status == wire.ToolNew || rt.Status == wire.ToolChanged {
			review = true
		}
		v.Tools = append(v.Tools, rt)
	}
	switch {
	case !r.Enabled:
		v.Status = wire.ToolsetDisabled
	case connErr != nil:
		v.Status, v.Reason = wire.ToolsetUnavailable, connErr.Error()
	case review:
		v.Status = wire.ToolsetReviewNeeded
	default:
		v.Status = wire.ToolsetReady
	}
	return v
}

// reviewTool is one tool of a view: as the server lists it (t, nil when it
// does not) against its review rv. When the server cannot be reached
// (unknown), a tool is shown as reviewed.
func reviewTool(name string, t *listedTool, rv review, unknown bool) wire.ReviewTool {
	rt := wire.ReviewTool{Name: name, Kind: string(rv.Kind), Risk: string(rv.Risk), ReviewedBy: rv.By}
	if !rv.At.IsZero() {
		at := rv.At
		rt.ReviewedAt = &at
	}
	approved := approvedTool(rv)
	switch {
	case t == nil && unknown:
		rt.Status = wire.ToolApproved
		if rv.Approved == "" {
			rt.Status = wire.ToolRejected
		}
	case t == nil:
		rt.Status, rt.Approved = wire.ToolRemoved, definition(approved)
	case t.fp == rv.Approved:
		rt.Status = wire.ToolApproved
	case t.fp == rv.Rejected:
		rt.Status = wire.ToolRejected
	case rv.Approved != "":
		rt.Status, rt.Approved = wire.ToolChanged, definition(approved)
		if approved != nil {
			rt.Changes, _ = client.Diff(approved, t.def)
		}
	default:
		rt.Status = wire.ToolNew
	}
	shown := approved
	if t != nil {
		rt.Fingerprint, rt.Current, shown = t.fp, definition(t.def), t.def
		rt.Description = client.SanitizeDescription(t.def.Description)
	}
	if shown != nil && (rt.Kind == "" || rt.Risk == "") {
		if def, err := client.Definition(shown); err == nil {
			kind, risk := def.Class()
			rt.Kind, rt.Risk = string(cmpOr(rv.Kind, kind)), string(cmpOr(rv.Risk, risk))
		}
	}
	return rt
}

func cmpOr[T ~string](a, b T) T {
	if a != "" {
		return a
	}
	return b
}

// approvedTool decodes the approved definition, numbers as written, so its
// fields compare byte for byte with the server's; nil when there is none.
func approvedTool(rv review) *mcp.Tool {
	if len(rv.Definition) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(rv.Definition))
	dec.UseNumber()
	var t mcp.Tool
	if err := dec.Decode(&t); err != nil {
		return nil
	}
	return &t
}

// definition is a server's definition as the API shows it, raw.
func definition(t *mcp.Tool) *wire.ToolDefinition {
	if t == nil {
		return nil
	}
	raw := func(v any) json.RawMessage {
		if v == nil {
			return nil
		}
		b, err := json.Marshal(v)
		if err != nil || string(b) == "null" {
			return nil
		}
		return b
	}
	d := &wire.ToolDefinition{Title: t.Title, Description: t.Description, InputSchema: raw(t.InputSchema), OutputSchema: raw(t.OutputSchema)}
	if t.Annotations != nil {
		d.Annotations = raw(t.Annotations)
	}
	return d
}

// offer is a tool agents are offered: its definition under the server's
// name, classed as reviewed, and what calls it.
type offer struct {
	def tool.Definition
	t   *listedTool
	cn  *conn
	// unusable, when set, is why the caller cannot call it now: their
	// connection to an OAuth toolset is missing or broken.
	unusable *connectionError
}
