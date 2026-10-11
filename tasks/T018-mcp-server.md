---
id: T018
title: "MCP server: many toolsets on one HTTP server, one URL path each"
epic: E03
status: done
depends_on: [T017]
---

# T018: MCP server, many toolsets per process

## Context

One process serves many toolsets (Salesforce, HubSpot, Gmail, ...), each at its own URL path, instead of one stdio server per connector. A client points at `https://host/mcp/salesforce` and sees only the Salesforce tools. Design: [PLAN.md `mcp`](../docs/PLAN.md#mcp). Uses the official Go SDK (v1.8.0 at planning time), whose `NewStreamableHTTPHandler(getServer func(*http.Request) *mcp.Server, opts)` picks an MCP server per request.

This task is only the HTTP/MCP layer. It takes already-built toolsets as input (`map[string]tool.Registry`); building them from config is T026, and wiring both into `cmd/agentkit` is T003.

## Scope

- `pkg/mcp/server`: `New(toolsets map[string]tool.Registry, opts...) (*Server, error)` returning an `http.Handler`:
  - `POST {base}/{toolset}` is the Streamable HTTP endpoint for that toolset (one `mcp.Server` per toolset, built once at startup).
  - Unknown toolset → 404. Toolset names follow the tool name rule (`^[a-zA-Z0-9_-]{1,64}$`) so they are safe as path segments.
  - `GET {base}/` lists the served toolsets (name, tool count) for discovery and debugging.
  - **Stateless mode by default** (no session store, so replicas scale behind a plain load balancer); stateful mode is an option.
  - Request body size limit (the SDK's default unless configured).
  - A `Middleware func(http.Handler) http.Handler` option for what comes later: auth, tenant resolution into `ctx`, rate limits, audit. Nothing built in yet.
- Error mapping: `ErrInvalidInput` and `ErrToolFailed` → tool result with `isError: true` (message only, never a stack trace); `ErrNotFound` → protocol error; client cancellation cancels the tool's context.
- Results: text content always; `structuredContent` too when the tool has an output schema.
- `ServeStdio(ctx, reg tool.Registry)`: one toolset over stdio, for local use with desktop clients. A thin wrapper over the same `mcp.Server` construction; not the main path.
- Example program: one process serving two toolsets (`tooltest` fixtures split into two sets) on two paths.

## Out of scope

- Building toolsets from config, and connector packages (T026, later connector epic).
- Authentication and per-tenant credentials (E07); only the middleware hook here.
- An aggregate endpoint serving all toolsets at once (add later if a client needs it, with tool names prefixed by toolset).

## Acceptance criteria

Use the SDK's own client over `httptest` (no subprocesses, no sleeps).

- [x] Two toolsets on one server: a client at `{base}/a` lists exactly toolset `a`'s tools with their schemas, and a client at `{base}/b` exactly `b`'s. Golden file for one `tools/list` result. — `TestToolsetsAreIsolated` (golden `testdata/tools_list_a.golden.json`)
- [x] `tools/call` with valid input returns the tool's output as text and as `structuredContent` when the tool has an output schema. — `TestCallTool`
- [x] Invalid input and tool failure come back as `isError: true` results whose text has no stack trace; an unknown tool is a protocol error. — `TestCallErrors`
- [x] An unknown toolset path returns 404; an invalid toolset name is rejected by `New`. — `TestRouting`, `TestNewRejects`
- [x] Cancelling the client request cancels the tool's context. — `TestCancellation`
- [x] Stateless mode: two clients with no session affinity can call the same toolset concurrently (`-race`). — `TestStatelessConcurrentClients` (and `TestStateful` for the option)
- [x] Middleware option wraps every toolset endpoint (a test middleware rejecting a missing header blocks all paths). — `TestMiddleware`
- [x] `ServeStdio` serves one toolset to an SDK client over an in-memory transport. — `TestServeStdio` (through `ServeTransport`, which `ServeStdio` calls with stdin/stdout)
- [x] Manual: the example server added to Claude Code as an HTTP MCP server (`claude mcp add --transport http a http://localhost:PORT/mcp/a`) lists and calls its tools. Recorded in Notes. Ran and recorded in Notes.

## Notes

- archtest allowlist: `pkg/mcp/server` → `github.com/modelcontextprotocol/go-sdk/mcp` and `.../jsonrpc` (the SDK's JSON-RPC error type). One line in `internal/archtest/rules.go`, plus a second for `examples/mcp-gateway` (its test imports the SDK client, as the example criterion requires): one line more than the brief allowed, scoped to that package and to `mcp` only.
- `go.mod`: `github.com/modelcontextprotocol/go-sdk v1.8.0` plus its transitive requirements (`golang.org/x/sync` moves to v0.20.0). `pkg/tool` was not changed.

### Public API

```go
func New(toolsets map[string]tool.Registry, opts ...Option) (*Server, error) // *Server is an http.Handler
func (s *Server) Toolsets() []ToolsetInfo                                    // name + tool count, sorted
func (s *Server) ServeHTTP(w, r)

WithBasePath(p)        // default "/mcp"
WithStateful()         // default stateless
WithMaxBodyBytes(n)    // default: the SDK's 4 MiB
WithMiddleware(mw)     // func(http.Handler) http.Handler; repeatable, first is outermost
WithLogger(*slog.Logger)
WithCallTimeout(d)     // cancel a call after d; the client gets an isError result
WithMaxInFlight(n)     // past n concurrent toolset requests: 503 + Retry-After, before any decoding
WithSessionTimeout(d)  // stateful mode: close idle sessions

func ServeStdio(ctx, reg tool.Registry, opts ...Option) error
func ServeTransport(ctx, reg tool.Registry, t mcp.Transport, opts ...Option) error
var ErrInvalidName, ErrInvalidConfig
```

### Decisions

- **Raw schemas pass through.** `mcp.Tool.InputSchema` and `OutputSchema` are `any`, and the SDK accepts a `json.RawMessage`, so tools are registered with the low-level `Server.AddTool` and our schemas reach clients unchanged (checked against `Registry.List` in `TestToolsetsAreIsolated`). The low-level API does no input validation; the registry already validates every call (`tool.Func`), so the server does not do it twice. The SDK panics on an unusable definition; `addTool` recovers and `New` returns an error instead.
- **One `mcp.Server` per toolset, built once in `New`**; its tools are listed once there, so a registry whose tools change later needs a new `Server`. All endpoints share one `http.Handler` that routes on the path.
- **Error mapping**, in `callHandler`: `ErrInvalidInput` / `ErrToolFailed` → `isError` result with `err.Error()` (a `*tool.PanicError` keeps its stack out of `Error()`, so no stack reaches the client; `TestCallErrors` asserts this, which is a case where the message is the behavior). `ErrNotFound` → JSON-RPC invalid-params (the SDK itself answers the same for names it never had). A done context → the context's error. Any other error is a generic `internal error` to the client and is logged (`WithLogger`), so a remote registry's transport failures do not leak internals.
- **Results**: text is the tool's output JSON; `structuredContent` is the same JSON when the tool declares an output schema.
- **Cancellation**: `StreamableHTTPOptions.PropagateRequestCancellation` is set so the tool's context ends when the client's request does. `TestCancellation` guards it.
- **Middleware wraps the whole handler**, not just toolset paths: also the `GET {base}/` listing and unknown paths, so an auth middleware reveals nothing (not even which toolsets exist) to unauthenticated callers. Values the middleware puts in the request context reach the tool's context (`TestMiddleware`), which is how tenant resolution (E07) will work.
- **`GET {base}/`** returns `{"toolsets":[{"name":"a","tools":2}, ...]}`. In stateless mode the SDK answers GET/DELETE on a toolset path with 405; clients cope (Claude Code connected fine).
- **`ServeTransport`** is an addition to the spec: `ServeStdio` is a one-line call of it with `mcp.StdioTransport`, and the in-memory test needs a transport parameter.
- The toolset name rule is duplicated from `pkg/tool` / `pkg/toolset` so the packages stay independent.
- `FuzzServeHTTP` posts arbitrary paths and bodies at the handler: valid status, no panic.

### Performance and reliability (stress-tested)

Load test: the real server over loopback TCP, 4 shared cores (server and load generator on the same box, so these are floors), tiny `echo` call, stateless mode. The harness was throwaway; `BenchmarkCallTool` in the package is the rerunnable part.

| Load | Result |
|---|---|
| `tools/call`, 16 / 64 / 512 concurrent clients | 3.6k / 5.0k / 7.3k req/s, 0 errors; p50 3.9 ms / 12 ms / 64 ms, p99 13 / 36 / 188 ms |
| `tools/list`, 64 clients | 7.0k req/s |
| full SDK client sessions (`initialize` + calls), 64 clients | 2.7k calls/s |
| 128 clients cancelling blocked calls after 5-35 ms, 10 s | 31k cancelled calls; goroutines back to baseline (2) and heap flat afterwards: no leaks |
| `-race` build, mixed list/call load, 64 clients | no data races |
| 512 clients with `WithMaxInFlight(64)` | 41% shed as instant 503s; the rest got p50 39 ms (vs 63 ms unlimited), p99 139 ms (vs 178 ms) |

Findings:

- **Our layer is not the cost.** CPU and allocation profiles put `route` and `callHandler` under 1% of allocations. Per stateless request the server allocates about 320 KB / 290 allocs, and 93% of that is inside the SDK (v1.8.0): it builds a fresh buffered `segmentio/encoding` JSON decoder several times per request (`readBatch`, `extractRequestMeta`, `validateRequestMeta`), each with a large buffer. GC is therefore about a third of CPU under load. We cannot fix that without forking the SDK; options if it ever matters: report upstream, or raise `GOGC` / set `GOMEMLIMIT` in the deployment. Stateful mode was not measured (session reuse might allocate less, but it needs session affinity); stateless stays the default.
- `StreamableHTTPOptions.JSONResponse` (plain JSON instead of SSE framing) made no measurable difference, so the SDK default stays.
- Memory is stable: heap after GC stays at 3-12 MB at 64-512 clients.
- Added after the test, because a stuck connector or an overload is the realistic failure: `WithCallTimeout` (cooperative tools only; a tool that ignores its context keeps its goroutine, which the `tool.Tool` contract forbids), `WithMaxInFlight` (cheapest possible load shedding: the check happens before the SDK decodes anything), `WithSessionTimeout`. `examples/mcp-gateway` shows the settings to copy: 30 s call timeout, 512 in flight, and `http.Server` header/idle timeouts and a header size cap (no read/write timeouts, because responses can stream).
- Not done here, on purpose: per-client rate limiting and auth (middleware, E07), health and readiness endpoints (`Component`, T003), metrics and tracing (observability epic).

Mutation check for these options (each planted alone, each caught): call timeout not applied (`TestCallTimeout`); timeout reported as a protocol error (`TestCallTimeout`); in-flight limit ignored (`TestMaxInFlight`); in-flight slot never released (`TestMaxInFlight`); no `Retry-After` on the 503 (`TestMaxInFlight`).

### For T003 (wiring)

```go
catalog, _ := toolset.NewCatalog(toolset.Demo() /*, connectors... */)
regs, problems, err := toolset.Build(ctx, catalog, cfg.MCP.Toolsets)
srv, err := server.New(regs, server.WithBasePath(cfg.MCP.BasePath), server.WithMiddleware(auth))
mux.Handle(cfg.MCP.BasePath+"/", srv) // mount WITHOUT http.StripPrefix
```

`examples/mcp-gateway` is exactly this on one port. `Server.ServeHTTP` is the `Component.Handler()` for an `mcp-server` component; it never listens (only `cmd/` does).

### Deferred / watch out for

- **DNS-rebinding protection**: the SDK rejects (403) requests that arrive over a loopback address with a non-localhost `Host` header. A reverse proxy on the same host that forwards the public `Host` to `127.0.0.1:port` will hit this; `StreamableHTTPOptions.DisableLocalhostProtection` exists but is not exposed here. Decide when T003 / the image topology is known.
- **Stateful mode** exposes no session timeout or `Close`; sessions live until the process ends. Add `WithSessionTimeout` if stateful mode is used for real.
- An aggregate endpoint serving all toolsets, auth, per-tenant credentials: out of scope, as planned.

### Manual check (Claude Code)

Ran with `claude` 2.1.296 in this environment, against the example built from `examples/mcp-gateway` and listening on `127.0.0.1:8080`:

```
claude mcp add --transport http demo-maths http://127.0.0.1:8080/mcp/demo-maths
claude mcp list        -> demo-maths: http://127.0.0.1:8080/mcp/demo-maths (HTTP) - Connected
claude -p "Using only the demo-maths MCP server: list the tool names it offers, then call its calculate tool with op=mul a=6 b=7 ..." --model claude-haiku-5-5 --allowedTools mcp__demo-maths__calculate
  -> "exposes one tool, calculate ... calculate(6 x 7) returned 42"
```

The path served only the filtered tool (`include: [calculate]`), and the call went through. One model call, Haiku, to keep cost down.

### Example (T018 scope + the end-to-end proof)

`examples/mcp-gateway`: `config.json` (embedded, or `-config file`) has two differently configured `demo` instances (`demo-eu`, `demo-ist`) and one filtered (`demo-maths`, `include: [calculate]`), built with `toolset.Build` and served by `server.New` on one port. `TestGateway` starts it on `httptest` and checks with the SDK client that each path lists exactly its own tools and that calls work (including that the two instances behave differently).

### Mutation check

Each bug planted alone; each made a test fail (no compile errors).

| Planted bug | Caught by |
|---|---|
| tool failure sent as a protocol error | `TestCallErrors` (invalid input, tool failure, panic) |
| invalid input sent as success | `TestCallErrors/invalid_input`, `/missing_input` |
| a stack trace in the error text | `TestCallErrors/tool_panic` |
| an unknown tool returned as `isError` | `TestCallErrors/tool_that_vanished_from_the_registry_is_a_protocol_error` |
| cancellation not propagated | `TestCancellation` |
| `structuredContent` missing | `TestCallTool`, `TestServeStdio`, `TestStatelessConcurrentClients` |
| toolset `a`'s tools visible at path `b` | `TestToolsetsAreIsolated`, `TestCallTool`, `TestMiddleware` |
| an unknown path not 404 | `TestRouting` |
| middleware skipped on one path | `TestMiddleware/blocks_every_path_without_the_header` |
| the stateless option ignored | `TestStatelessConcurrentClients`, `TestToolsetsAreIsolated` |
