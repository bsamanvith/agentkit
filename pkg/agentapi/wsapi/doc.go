// Package wsapi serves the agent API's WebSocket binding (T091, E18): one
// connection at [wire.WSPath] that carries the API's commands and run event
// streams as JSON messages ([wire.WSMessage]), for clients that want many
// streams over one connection, or sit behind networks that buffer SSE.
//
// It is a binding, not a second API: [New] takes the API's [http.Handler]
// (agentapi's Server.Handler), and each command or subscription is served
// by that handler as the HTTP request it stands for, with the connection's
// credentials. Its result is that response's status and body, and a
// stream's events are the SSE stream's events, byte for byte. So
// authentication, scopes, tenants, error codes and resume by event ID are
// the HTTP API's, by construction:
//
//	hello        GET  /v1/auth/me             (authenticates; a later one renews)
//	subscribe    GET  /v1/runs/{id}/events    (after: Last-Event-ID)
//	send         POST /v1/runs                (body: RunRequest; streams)
//	resume       POST /v1/runs/{id}/resume    (streams)
//	stop         POST /v1/runs/{id}/stop
//	cancel       POST /v1/runs/{id}/cancel
//	answer       POST /v1/runs/{id}/input     (body: InputRequest)
//	decide       POST /v1/runs/{id}/decisions (body: DecisionRequest)
//	steer        POST /v1/tasks/{id}/messages (body: SteerRequest; key: Idempotency-Key)
//	pause        POST /v1/tasks/{id}/pause
//	resume_task  POST /v1/tasks/{id}/resume
//
// Each connection writes from one queue of at most [WithQueue] messages. A
// client that reads slower than its streams fill it is never waited for:
// every stream it has open gets a [wire.WSGap] naming the last event sent
// on it, and the connection closes with [wire.WSCloseSlow]; the client
// subscribes again from there. Heartbeats (pings, [WithHeartbeat]) close a
// connection whose peer is gone. A connection holds at most
// [WithMaxStreams] streams and [WithMaxCommands] commands not yet answered;
// past them a command gets 503 over_capacity.
//
// Mount [Server] next to the API, at [wire.WSPath]: the upgrade itself
// needs no credentials (a browser cannot set headers on it), the hello
// does. Its origin check (RFC 6455's, [WithOriginPatterns]) keeps other
// sites' pages from using a session cookie through it.
package wsapi
