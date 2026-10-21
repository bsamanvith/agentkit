// Package component runs a set of components on one HTTP server: the shape of
// every agentkit deployment, whichever pieces a container runs.
//
// A [Component] is built by its own typed constructor (explicit wiring in
// cmd/agentkit, no dependency bag) and handed to [New] with the pattern its
// handler is mounted at:
//
//	rt, err := component.New([]component.Mount{
//		{Pattern: "/mcp/", Component: mcpServer},
//	}, component.WithLogger(logger), component.WithBuildInfo(info))
//	err = rt.Serve(ctx, listener) // until ctx is done, then a graceful shutdown
//
// Next to the components' handlers the server answers:
//
//   - GET /healthz: 200 as soon as the server accepts connections (liveness).
//   - GET /readyz: 200 when every component's Ready returns nil or an error
//     matching [ErrDegraded], else 503. The body says why, per component:
//     {"status":"not ready","components":[{"name":"a","status":"not ready","reason":"..."}]}.
//     A component status is "ready", "degraded" or "not ready". A component
//     that implements [Reporter] adds "details": what it loaded.
//   - GET /version: the [BuildInfo], as JSON.
//
// The server speaks HTTP/1.1 and HTTP/2 over cleartext (h2c), for ConnectRPC.
//
// When the context given to [Runtime.Serve] is done (cmd/agentkit cancels it
// on SIGINT or SIGTERM), the runtime stops accepting connections, lets
// in-flight requests finish, then shuts the components down in reverse order,
// all within one deadline ([WithShutdownTimeout]). Request contexts do not
// derive from that context, so a signal does not cancel requests in flight.
//
// This package never listens: the caller (only cmd/) owns the listener.
package component
