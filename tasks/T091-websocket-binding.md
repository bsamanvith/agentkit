---
id: T091
title: "WebSocket binding of the agent API's messages (later)"
epic: E18
status: todo
depends_on: [T090]
---

# T091: WebSocket binding

## Context

SSE + HTTP commands cover the console and CLI. Consumer apps may want one connection with many subscriptions (mobile), realtime voice, or a transport that survives networks that buffer SSE. Decisions: [E18](E18-streaming-backend.md) ("WebSocket").

## Scope

- **Endpoint:** `GET /v1/ws` (upgrade). After the client sends a `hello` with its token, the connection carries:
  - `subscribe` and `unsubscribe` (run, task, user streams; resumable with the same IDs as SSE);
  - the same commands as the HTTP endpoints (send, steer, answer, cancel), each with a client `id` and a `result` reply carrying the same status and error codes.
- **Messages:** the same JSON envelope and event objects as SSE, nothing WebSocket-only.
- **Connection health:** heartbeats; backpressure (slow clients get `gap` then are closed); per-connection limits; auth renewal.
- **Clients:** `agentclient` and the `web` packages get a WebSocket transport behind the same interfaces.

## References

- [RFC 6455](https://www.rfc-editor.org/rfc/rfc6455); [coder/websocket](https://github.com/coder/websocket) (maintained, context-aware Go library)
- The OpenAI Realtime API and Phoenix Channels, for command-and-reply framing over one socket

## Acceptance criteria

- [ ] **Same results either way:** every command and subscription gives the same results over WebSocket as over HTTP and SSE (one shared test table run on both transports).
- [ ] **Resume:** works with the same IDs; slow clients are cut off with a `gap`, not by blocking the server.
- [ ] **Mutation list** recorded.
