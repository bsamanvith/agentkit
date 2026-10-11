// Package server serves tool registries as MCP servers: many toolsets in one
// process, one URL path each.
//
//	srv, err := server.New(map[string]tool.Registry{
//		"salesforce":     salesforceTools,
//		"gmail-readonly": gmailTools,
//	}, server.WithMiddleware(auth))
//	mux.Handle("/mcp/", srv) // /mcp/salesforce, /mcp/gmail-readonly, GET /mcp/
//
// A client that connects to /mcp/salesforce sees only the Salesforce tools.
// There is one [mcp.Server] per toolset, built once in [New]; the handler
// routes by path. Run it behind your own listener: this package never listens.
//
// By default the endpoints are stateless (no session store, so replicas scale
// behind a plain load balancer); [WithStateful] turns sessions on.
//
// Errors from [tool.Registry.Invoke] map to MCP like this:
//
//   - [tool.ErrInvalidInput] and [tool.ErrToolFailed] become results with
//     isError set, carrying the error message (never a stack trace), so the
//     model can see and react to them;
//   - [tool.ErrNotFound] is a protocol error (unknown tool);
//   - a cancelled or expired context ends the call with the context's error.
//
// A result always has the tool's output as text content, and also as
// structuredContent when the tool declares an output schema.
//
// [ServeStdio] serves a single toolset over stdio for desktop clients, from
// the same code.
package server
