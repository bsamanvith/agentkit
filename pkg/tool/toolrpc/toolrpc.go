// Package toolrpc puts toolsets on the wire: [Register] serves named
// [tool.Registry] values on an [rpc.Server], and a [Client]'s registries
// implement tool.Registry by calling them. A remote registry runs the same
// conformance suite as the in-process one (tooltest.RunRegistry), so a
// component that takes a tool.Registry cannot tell them apart.
//
// This is agentkit's own protocol between its components; MCP
// (pkg/mcp/server) is the one for everyone else. It keeps what MCP has no
// place for: tool metadata whole, and the identity of tool errors
// ([tool.ErrNotFound], [tool.ErrInvalidInput], [tool.ErrToolFailed]), and
// who a call acts for: the context's tenant and user (pkg/tenancy) reach the
// server's tool, so a user's own credential works on a toolsets component.
package toolrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Service is the service's name: a component serving it mounts its server at
// rpc.PathPrefix + Service + "/".
const Service = "toolsets"

// ErrNoToolset means the server serves no toolset of that name (or serves
// it skipped).
var ErrNoToolset = errors.New("toolset not served")

// Toolset describes a toolset a server serves.
type Toolset struct {
	// Name and Type are the toolset's config name and its connector type.
	Name string `json:"name"`
	Type string `json:"type"`
	// Skipped says why an optional toolset was not built; it then has no
	// tools. Empty for a built toolset.
	Skipped string `json:"skipped,omitempty"`
	// Credential says where the toolset's credential comes from on the
	// server, never its value.
	Credential Credential `json:"credential,omitzero"`
}

// Credential is where a toolset's credential comes from: its status
// ("not_needed", "configured", "missing", "keyless") and the environment
// variable that holds it on the server.
type Credential struct {
	Status string `json:"status,omitempty"`
	Env    string `json:"env,omitempty"`
}

// The service's methods. Each names its toolset; Invoke names the tool by
// its own name in the toolset.
var (
	toolsetsMethod = rpc.Unary[struct{}, toolsetsResp]{Name: Service + "/Toolsets"}
	listMethod     = rpc.Unary[listReq, listResp]{Name: Service + "/List", Errors: codes}
	invokeMethod   = rpc.Unary[invokeReq, invokeResp]{Name: Service + "/Invoke", Errors: codes}
)

// codes are the errors that keep their identity across the wire.
var codes = []rpc.Code{
	{Name: "toolset.not_served", Err: ErrNoToolset},
	{Name: "tool.not_found", Err: tool.ErrNotFound},
	{Name: "tool.invalid_input", Err: tool.ErrInvalidInput},
	{Name: "tool.failed", Err: tool.ErrToolFailed},
	{Name: "tool.panic", Err: tool.ErrPanic},
	{Name: "tool.invalid_definition", Err: tool.ErrInvalidDefinition},
}

type toolsetsResp struct {
	Toolsets []Toolset `json:"toolsets"`
}

type listReq struct {
	Toolset string `json:"toolset"`
}

type listResp struct {
	Tools []tool.Definition `json:"tools"`
}

type invokeReq struct {
	Toolset string          `json:"toolset"`
	Tool    string          `json:"tool"`
	Input   json.RawMessage `json:"input,omitempty"`
	// Caller is the tenant and user the call acts for: a toolset whose
	// credential slot is the tenant's or the user's reads it from the
	// context, on the server as in process.
	Caller tenancy.Carried `json:"caller,omitzero"`
}

type invokeResp struct {
	// Output is absent for a nil output, so the caller gets nil back, not
	// the JSON null.
	Output json.RawMessage `json:"output,omitempty"`
}

// Register serves toolsets on s: sets describes them, in the order
// [Client.Toolsets] lists them, and regs holds the registry of each built
// one (every toolset of sets that is not Skipped). It fails for a toolset
// listed twice, a built one without a registry, or a registry not in sets.
func Register(s *rpc.Server, sets []Toolset, regs map[string]tool.Registry) error {
	sets = slices.Clone(sets)
	seen := map[string]bool{}
	for _, ts := range sets {
		switch {
		case seen[ts.Name]:
			return fmt.Errorf("toolrpc: toolset %q listed twice", ts.Name)
		case ts.Skipped == "" && regs[ts.Name] == nil:
			return fmt.Errorf("toolrpc: toolset %q has no registry and was not skipped", ts.Name)
		case ts.Skipped != "" && regs[ts.Name] != nil:
			return fmt.Errorf("toolrpc: toolset %q has a registry and was skipped", ts.Name)
		}
		seen[ts.Name] = true
	}
	for name := range regs {
		if !seen[name] {
			return fmt.Errorf("toolrpc: registry %q is not one of the toolsets", name)
		}
	}
	regs = maps.Clone(regs)
	registry := func(name string) (tool.Registry, error) {
		if reg := regs[name]; reg != nil {
			return reg, nil
		}
		return nil, fmt.Errorf("%w: %q", ErrNoToolset, name)
	}
	toolsetsMethod.Handle(s, func(context.Context, struct{}) (toolsetsResp, error) {
		return toolsetsResp{Toolsets: slices.Clone(sets)}, nil
	})
	listMethod.Handle(s, func(ctx context.Context, r listReq) (listResp, error) {
		reg, err := registry(r.Toolset)
		if err != nil {
			return listResp{}, err
		}
		defs, err := reg.List(ctx)
		return listResp{Tools: defs}, err
	})
	invokeMethod.Handle(s, func(ctx context.Context, r invokeReq) (invokeResp, error) {
		reg, err := registry(r.Toolset)
		if err != nil {
			return invokeResp{}, err
		}
		ctx, err = r.Caller.Into(ctx)
		if err != nil {
			return invokeResp{}, fmt.Errorf("%w: caller: %w", rpc.ErrBadRequest, err)
		}
		out, err := reg.Invoke(ctx, r.Tool, r.Input)
		return invokeResp{Output: out}, err
	})
	return nil
}

// Client calls a server's toolsets. It is safe for concurrent use.
type Client struct{ conn rpc.Conn }

// NewClient returns a client that calls through conn: rpc.Dial for a server
// in another container, rpc.Pipe for one in process.
func NewClient(conn rpc.Conn) *Client { return &Client{conn: conn} }

// Toolsets lists the server's toolsets, built and skipped.
func (c *Client) Toolsets(ctx context.Context) ([]Toolset, error) {
	resp, err := toolsetsMethod.Call(ctx, c.conn, struct{}{})
	return resp.Toolsets, err
}

// Registry returns the registry of the server's toolset name. It does not
// call the server: a toolset it does not serve fails each call with
// [ErrNoToolset].
func (c *Client) Registry(name string) tool.Registry { return &registry{c: c, name: name} }

type registry struct {
	c    *Client
	name string
}

var _ tool.Registry = (*registry)(nil)

func (r *registry) List(ctx context.Context) ([]tool.Definition, error) {
	resp, err := listMethod.Call(ctx, r.c.conn, listReq{Toolset: r.name})
	if err != nil {
		return nil, fmt.Errorf("toolset %q: %w", r.name, err)
	}
	return resp.Tools, nil
}

func (r *registry) Invoke(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
	resp, err := invokeMethod.Call(ctx, r.c.conn, invokeReq{Toolset: r.name, Tool: name, Input: input, Caller: tenancy.Carry(ctx)})
	if err != nil {
		// A toolsets component that went away, or no longer serves this
		// toolset, is a failed call, as a local connector's dropped
		// connection is: the model sees the error, the run goes on.
		if (errors.Is(err, rpc.ErrUnavailable) || errors.Is(err, ErrNoToolset)) && !errors.Is(err, tool.ErrToolFailed) {
			err = fmt.Errorf("%w: %w", tool.ErrToolFailed, err)
		}
		return nil, err
	}
	return resp.Output, nil
}
