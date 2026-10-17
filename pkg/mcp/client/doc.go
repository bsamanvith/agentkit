// Package client uses other MCP servers' tools as [tool.Tool]s: the client
// half of E19, on the official Go SDK's [mcp.Client].
//
//	c, err := client.Connect(ctx, client.Config{URL: "https://mcp.example.com/mcp"})
//	defs, err := c.Tools(ctx)  // every page of tools/list, as the server sent them
//	t, err := c.Wrap(defs[0])  // a tool.Tool that calls it
//
// [Factory] is the toolset type "mcp", so a server named in config is a
// toolset like any connector:
//
//	{"name": "linear", "type": "mcp", "settings": {
//	  "url": "https://mcp.linear.app/mcp",
//	  "headers": {"Authorization": "Bearer ${LINEAR_API_KEY}"}}}
//
// # Transports
//
// Streamable HTTP, falling back to the older HTTP+SSE transport when the
// server does not speak it ([TransportAuto]), or either one alone. stdio runs
// a command, and only from the operator's config: the command never comes
// from a request.
//
// # Calls
//
// A call has a timeout ([Config.CallTimeout]) and a result size cap
// ([Config.MaxResultBytes]). Cancelling its context sends
// notifications/cancelled (the SDK does). Everything that can go wrong on the
// server's side (an isError result, a protocol error, a timeout, an oversized
// result, a dropped session or process) is a [tool.ErrToolFailed] the model
// sees, never a panic. When the server has dropped the session, the call is
// sent once more on a new one: the server never ran it. Any other broken
// connection is reopened on the next call.
//
// # Untrusted servers
//
// A server's tool definitions are claims. Wrapped tools carry descriptions
// cleaned by [SanitizeDescription] (the raw text stays in the definitions
// [Client.Tools] returns, for review) and metadata from the server's
// annotations, which may raise a tool's risk but never lower it: a
// readOnlyHint is kept as the tag [TagReadOnlyHint], not as kind read.
// [Fingerprint] and [Diff] are what pinning a server's tools (T096) is built
// on, and [Client.ToolsChanged] signals notifications/tools/list_changed.
// Results are third-party data.
package client
