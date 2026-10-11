package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// newMCPServer builds the MCP server for one toolset and reports how many
// tools it has. Both the HTTP handler and ServeStdio use it.
func newMCPServer(ctx context.Context, name string, reg tool.Registry, log *slog.Logger, timeout time.Duration) (*mcp.Server, int, error) {
	defs, err := reg.List(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("toolset %q: listing tools: %w", name, err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "agentkit-" + name, Version: "0.1.0"}, &mcp.ServerOptions{Logger: log})
	for _, def := range defs {
		if err := addTool(srv, reg, def, log, timeout); err != nil {
			return nil, 0, fmt.Errorf("toolset %q: %w", name, err)
		}
	}
	return srv, len(defs), nil
}

// addTool registers one tool with its schemas passed through as raw JSON, so
// clients see exactly what the registry declared.
func addTool(srv *mcp.Server, reg tool.Registry, def tool.Definition, log *slog.Logger, timeout time.Duration) (err error) {
	// The SDK panics on a definition it cannot use; a remote registry could
	// send one, so turn that into an error.
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("tool %q: %v", def.Name, v)
		}
	}()
	t := &mcp.Tool{Name: def.Name, Description: def.Description, InputSchema: def.InputSchema}
	if len(def.OutputSchema) > 0 {
		t.OutputSchema = def.OutputSchema
	}
	srv.AddTool(t, callHandler(reg, def, log, timeout))
	return nil
}

func callHandler(reg tool.Registry, def tool.Definition, log *slog.Logger, timeout time.Duration) mcp.ToolHandler {
	structured := len(def.OutputSchema) > 0
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		callCtx := ctx
		if timeout > 0 {
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		out, err := reg.Invoke(callCtx, def.Name, req.Params.Arguments)
		switch {
		case err == nil:
		case errors.Is(err, tool.ErrNotFound):
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("unknown tool %q", def.Name)}
		case ctx.Err() == nil && callCtx.Err() != nil:
			// Our own call timeout, not the client leaving: the model should
			// see that the tool was too slow.
			return errorResult(fmt.Sprintf("tool %q timed out after %v", def.Name, timeout)), nil
		case ctx.Err() != nil:
			// The client went away or the deadline passed; no one is reading
			// a tool result, and it must not look like the tool failed.
			return nil, ctx.Err()
		case errors.Is(err, tool.ErrInvalidInput), errors.Is(err, tool.ErrToolFailed):
			// Error() holds the message only: a *tool.PanicError keeps its
			// stack out of it.
			return errorResult(err.Error()), nil
		default:
			log.Error("tool call failed unexpectedly", "tool", def.Name, "err", err)
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "internal error"}
		}

		res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(out)}}}
		if structured {
			if !json.Valid(out) {
				log.Error("tool returned invalid JSON for its output schema", "tool", def.Name)
				return errorResult(fmt.Sprintf("tool %q returned invalid output", def.Name)), nil
			}
			res.StructuredContent = out
		}
		return res, nil
	}
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// ServeStdio serves reg as a single MCP server over stdin/stdout until ctx is
// done or the client disconnects, for desktop clients that launch a local
// process. It uses the same server construction as [New]. Only [WithLogger] and
// [WithCallTimeout] apply.
func ServeStdio(ctx context.Context, reg tool.Registry, opts ...Option) error {
	return ServeTransport(ctx, reg, &mcp.StdioTransport{}, opts...)
}

// ServeTransport is [ServeStdio] over any MCP transport, for example an
// in-memory one in tests.
func ServeTransport(ctx context.Context, reg tool.Registry, t mcp.Transport, opts ...Option) error {
	if reg == nil {
		return fmt.Errorf("%w: registry is nil", ErrInvalidConfig)
	}
	c := newConfig(opts)
	srv, _, err := newMCPServer(ctx, "stdio", reg, c.logger, c.callTimeout)
	if err != nil {
		return err
	}
	return srv.Run(ctx, t)
}
