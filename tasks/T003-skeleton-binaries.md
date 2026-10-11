---
id: T003
title: agentkit binary with component runtime and health endpoints
epic: E01
status: done
depends_on: [T001, T018, T026]
---

# T003: `agentkit` binary with component runtime and health endpoints

## Context

The binary that runs any set of components, as in [ENGINEERING.md §1.5](../docs/ENGINEERING.md#15-components-mix-and-match-deployment). Built at M5, once there are real components (tool registry, MCP server) to run, so the runtime is shaped by real needs.

## Scope

- `pkg/component`: the `Component` interface (`Name`, `Handler`, `Ready`, `Shutdown`); a runtime that mounts each component's handler on one `net/http` server (h2c enabled for ConnectRPC), aggregates readiness, and shuts components down in reverse order.
- Server: `GET /healthz`, `GET /readyz`, `GET /version` (build info via `-ldflags`), `slog` JSON logging, graceful shutdown on SIGINT/SIGTERM with a deadline.
- `cmd/agentkit` already exists (T024: `run`, `flow demo`). Add **`agentkit serve`**: explicit wiring code; component list from env for now (`AGENTKIT_COMPONENTS=mcp-server`), YAML config later. Keep `run` and `flow demo` working.
- First real component: `mcp-server`. `cmd/agentkit` builds the toolsets listed in config with `toolset.Build` (T026) from a catalog of the compiled-in connector factories (one file per build tag), and mounts the T018 handler at `mcp.base_path`. Per-toolset readiness feeds `/readyz`.

## Wiring notes from T018 (see `tasks/T018-mcp-server.md` Notes)

- Mount with `mux.Handle(base+"/", srv)` and **no** `http.StripPrefix`; `regs` come from `toolset.Build`. `examples/mcp-gateway` is the template to copy, including its `http.Server` timeouts.
- Expose the T018 hardening options (call timeout, in-flight cap, session timeout) through the same env/config as the rest.
- **Reverse proxies:** the MCP SDK answers 403 to a request that reaches a loopback address with a public `Host` header (DNS-rebinding protection). A container listening on `0.0.0.0` behind a port mapping is not affected; a same-host reverse proxy forwarding to `127.0.0.1` is. `pkg/mcp/server` does not expose the SDK's switch yet. Add it as an explicit option only when a deployment needs it (decide in T006/M5), and test the Compose topology through its published port.
- Auth and tenancy plug in through `server.WithMiddleware`; values it puts in the request context reach the tool's context. With `pkg/tenancy` (T027) in place, that middleware is planned with E07, not built here.

## Out of scope

- The `pkg/config` package.
- Remote wiring (M6).

## Acceptance criteria

- [x] Only the components listed in config are started; an unknown name fails startup with a clear error. — `cmd/agentkit.TestServeRejectsBadSetup/unknown_component` (and `/component_listed_twice`; every other case there is an invalid environment or toolset config failing startup with a message naming the mistake); `cmd/agentkit.TestE2EServe` (`/readyz` lists exactly `mcp-server`); `component.TestMountsOnlyGivenComponents`, `component.TestNewRejects`
- [x] `/healthz` returns 200 as soon as the server listens. — `component.TestHealthzAsSoonAsListening` (no component ready yet; over HTTP/1.1 and h2c), `cmd/agentkit.TestE2EServe`
- [x] `/readyz` returns 503 until every started component is ready, then 200. — `component.TestReadyz` (503 with each component's reason, 200 once all are ready, degraded counts as ready, back to 503 when one stops being ready); `cmd/agentkit.TestE2EServe` (the optional toolset that failed shows in `/readyz`)
- [x] On SIGTERM, in-flight requests complete, components shut down in reverse order, and the process exits 0 within the deadline. — `cmd/agentkit.TestE2EServe` (real SIGTERM to the binary while an MCP `tools/call` is held in a handler: the call completes, exit 0 within `AGENTKIT_SHUTDOWN_TIMEOUT`); `component.TestGracefulShutdown` (request finishes before any component shuts down, then reverse order, `Serve` returns nil); `component.TestShutdownDeadline` (a stuck request or a stuck component cannot hold the process past the deadline); `component.TestShutdownErrors`
- [x] `/version` reports the values set via `-ldflags`. — `cmd/agentkit.TestE2EServe` (the test binary is built with `-ldflags "-X main.version=… -X main.commit=… -X main.date=…"`), `component.TestVersion`
- [x] Startup to `/healthz` 200 is under 50 ms (measured once, recorded in Notes; not a CI test). — 11 ms median (below)

## Notes

### Files

- `pkg/component`: `doc.go`, `component.go`, `options.go`, tests (`component_test.go`, `example_test.go`).
- `cmd/agentkit`: `serve.go` (command, env, config file), `mcpserver.go` (the `mcp-server` component), `catalog.go` (compiled-in connectors; `demoTools` uses it too), `main.go` (usage, `serve` dispatch, `-ldflags` variables, `cli.getenv`), tests `serve_test.go`, `serve_e2e_test.go`; `e2e_test.go` now builds the test binary with `-ldflags` (T024's tests are unchanged and pass).
- **Outside the listed files:** one line in `internal/archtest/rules.go`, `"cmd/agentkit": {"github.com/modelcontextprotocol/go-sdk/mcp"}`, because the serve e2e test uses the MCP SDK client, as the task asks. Same shape as the line for `examples/mcp-gateway`. Non-test code in `cmd/agentkit` does not import the SDK. `pkg/mcp/server` and `pkg/toolset` are unchanged.

### Command line and environment

```
agentkit serve [-config file]
```

| Setting | Where | Default | Notes |
|---|---|---|---|
| components | `AGENTKIT_COMPONENTS` | `mcp-server` | comma-separated; unknown or repeated names fail startup |
| listen address | `AGENTKIT_ADDR` | `127.0.0.1:8080` | loopback by default: nothing is exposed to the network until a deployment says so. **The image must set `:8080`.** |
| log level | `AGENTKIT_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. JSON (`slog`) to stderr. The MCP SDK's per-request records (3 at info for each stateless request) only show at `debug`. |
| config file | `-config`, else `AGENTKIT_CONFIG` | none | required when `mcp-server` is listed |
| shutdown deadline | `AGENTKIT_SHUTDOWN_TIMEOUT` | `15s` | covers draining requests and every component's `Shutdown` |

Config file (JSON; YAML with the same keys comes with `pkg/config`). Unknown fields anywhere are errors, so a typo fails startup instead of changing what is served:

```json
{
  "mcp": {
    "base_path": "/mcp",          // default "/mcp"; toolsets at {base_path}/{name}
    "call_timeout": "30s",        // default 30s; "0s" = no limit
    "max_in_flight": 512,         // default 512; 0 = no cap (503 + Retry-After past it)
    "stateful": false,            // default stateless
    "session_timeout": "0s",      // stateful only; default never
    "max_body_bytes": 0,          // default 0 = the SDK's 4 MiB
    "toolsets": [
      {"name": "demo-eu", "type": "demo", "settings": {"echo_prefix": "[eu] "}},
      {"name": "gmail", "type": "gmail", "optional": true}
    ]
  }
}
```

(Comments only for this table; the file is plain JSON.) The call-timeout and in-flight defaults are the ones `examples/mcp-gateway` recommends.

Exit codes: 0 after a clean shutdown, 1 for a startup error (message on stderr, `agentkit: …`) or an unclean shutdown (deadline exceeded, a component's `Shutdown` failed), 2 for a usage error.

Log records a deployment can rely on: `{"msg":"listening","addr":"…","components":[…]}` once the port is open (the e2e test reads the port from it when `AGENTKIT_ADDR` ends in `:0`), `"shutting down"`, `"stopped"`, and `"optional toolset skipped"` (warn) per `toolset.Problem`.

### Public API of `pkg/component`

```go
type Component interface {
    Name() string
    Handler() http.Handler
    Ready(ctx context.Context) error   // nil, or why not; an error wrapping ErrDegraded still counts as ready
    Shutdown(ctx context.Context) error
}
type Mount struct { Pattern string; Component Component } // ServeMux pattern, e.g. "/mcp/"; "" = not mounted (a worker)
type BuildInfo struct { Version, Commit, Date string }

func New(mounts []Mount, opts ...Option) (*Runtime, error)
func (r *Runtime) Handler() http.Handler                       // components + /healthz /readyz /version
func (r *Runtime) Serve(ctx context.Context, ln net.Listener) error

WithLogger(*slog.Logger)  WithBuildInfo(BuildInfo)  WithShutdownTimeout(d) // default DefaultShutdownTimeout = 15s
var ErrDegraded, ErrRequestsCutOff, ErrInvalidConfig // ErrRequestsCutOff: requests still in flight at the deadline
```

- `GET /healthz` → 200 `{"status":"ok"}` whenever the server accepts connections.
- `GET /readyz` → 200 or 503 `{"status":"ready"|"not ready","components":[{"name":"mcp-server","status":"ready"|"degraded"|"not ready","reason":"…"}]}`. Components are asked concurrently, with the request's context.
- `GET /version` → `{"version":"…","commit":"…","date":"…"}`.

### Decisions

- **The interface stays the four methods of §1.5; the mount point is given beside it (`Mount.Pattern`).** A component does not choose its URL, the wiring does (the MCP base path comes from config). A ConnectRPC component later mounts at its service path the same way. A pattern the ServeMux rejects (malformed, or clashing with another component or with `/healthz`, `/readyz`, `/version`) fails `New` with `ErrInvalidConfig` instead of panicking.
- **Degraded is ready.** "Make optional toolset failures visible in readiness" could mean 503. It does not: a toolset marked `optional` is one the operator chose to run without. Pulling the instance out of the load balancer because Gmail credentials are missing would take Salesforce down with it. So `mcp-server.Ready` returns an error wrapping `component.ErrDegraded`, and `/readyz` answers 200 with `"status":"degraded"` and the reason naming each skipped toolset. Required toolsets that fail never get this far: startup fails. The reason text is the `toolset.Problem` message, so `/readyz` must stay an internal endpoint (same as `/mcp/`, which lists toolsets).
- **Shutdown order:** stop accepting, drain in-flight requests (`http.Server.Shutdown`), then components in reverse order, all within one deadline. Components shut down after the drain because in-flight requests still use them. Past the deadline the remaining connections are closed, every component still gets its `Shutdown` call (with the expired context), and the runtime does not wait for one that ignores its context: the process must exit.
- **Request contexts are not derived from the signal context** (`BaseContext` uses `context.WithoutCancel`), so SIGTERM does not cancel calls in flight; the drain deadline bounds them instead. A mutation below proves the e2e test notices.
- **h2c** with `http.Server.Protocols` (Go 1.24+), no `golang.org/x/net` dependency. Server timeouts as in `examples/mcp-gateway`: header 10 s, idle 2 min, 64 KiB headers, no read/write timeout (responses stream).
- **Signals stay in `main`** (`signal.NotifyContext` for SIGINT and SIGTERM, already there for `run`); the runtime takes a context. Only `cmd/` owns signals, listeners and env, as in §1.3.
- **Components by env, toolsets by file.** `AGENTKIT_COMPONENTS` is checked before the config is read, and the per-name `switch` in `serve.go` is the wiring (§1.5). An unset or empty list means `mcp-server`, the only component so far.
- **The catalog** is `newCatalog()` in `cmd/agentkit/catalog.go`: `toolset.NewCatalog(toolset.Demo())`. A connector behind a build tag adds its factory there from a small file of its own (not built here; there are no connectors yet).
- `-ldflags` variables are `main.version` (default `dev`), `main.commit`, `main.date`.
- **Stateful mode** is exposed (`stateful`, `session_timeout`) but a stateful client's open stream holds a shutdown until the deadline (`pkg/mcp/server` has no way to close sessions). Stateless, the default, has no such stream. Use stateless in the topologies.

### Tests

- `pkg/component` (unit, fakes, real loopback listener or `httptest`): `TestMountsOnlyGivenComponents`, `TestNewRejects`, `TestHealthzAsSoonAsListening`, `TestReadyz`, `TestVersion`, `TestGracefulShutdown`, `TestShutdownDeadline`, `TestShutdownErrors`, `TestListenerFailure`, `Example`. Invariant checked after every test that serves (the `serve` helper's cleanup): `Serve` returns once its context is done, and every component is shut down exactly once.
- `cmd/agentkit.TestServeRejectsBadSetup` (in-process, 18 cases): every bad env value or config fails with exit 1 (2 for a usage error) and a message naming the mistake. A broken check would start serving instead: the test then ends on its 10 s context with exit 0 and fails rather than hanging.
- `cmd/agentkit.TestE2EServe` (replay e2e tier: the built binary as its own process, no fixtures needed since no model is called). It configures two demo toolsets plus one optional toolset that fails. Checked: `/healthz`, `/version` (`-ldflags`), `/readyz` (degraded, naming the skipped toolset), the MCP SDK client listing and calling tools on `/mcp/demo-a` and `/mcp/demo-b` (each with its own tools and settings), and `GET /mcp/`. Then it sends SIGTERM while a `tools/call` is held inside a handler: the call completes and the process exits 0 within the deadline.
  - **How the call is held, without timing:** the request is sent with `Expect: 100-continue` and its body is held back. Go's server sends `100 Continue` only when the handler first reads the body, so `httptrace.Got100Continue` proves the request is in a handler. The test then sends SIGTERM, waits until the listener refuses connections (the shutdown has begun), and releases the body.
  - My first version only held the body back. It failed 1 run in 10: the request could still be in the listener's accept queue (reset when the listener closes) or unread on an idle keep-alive connection (closed as idle). That is HTTP/1.1's shutdown race, not a server bug, but the test must not depend on it. 120 runs clean after the change.
- `go test -race -shuffle=on -count=20 ./pkg/component/...` passes; `TestE2EServe` passed `-count=60` twice.

### Startup time

From `exec` to the first `/healthz` 200, measured with a throwaway Go program polling every 200 µs: 20 starts, 4 cores, config with three toolsets (one optional that fails), default build (21 MB): **min 9.8 ms, median 11.0 ms, p90 12.2 ms, max 13.7 ms**. With `CGO_ENABLED=0 -trimpath -ldflags "-s -w"` (14 MB, what T005 will build): median 9.6 ms, max 12.6 ms. Most of it is process start; building the demo toolsets is negligible. Well under the 50 ms target.

### Mutation check

Each bug planted alone; both packages' tests run with `-race -count=2`; each plant compiled and made a test fail.

| Planted bug | Caught by |
|---|---|
| `/readyz` 200 before components are ready | `TestReadyz`, `TestMountsOnlyGivenComponents`, `TestHealthzAsSoonAsListening`, `Example` |
| a degraded component counted as not ready | `TestReadyz`, `TestE2EServe` |
| `/healthz` waits for readiness | `TestHealthzAsSoonAsListening`, `TestE2EServe`, `Example` |
| components shut down in forward order | `TestGracefulShutdown`, `TestShutdownDeadline/component`, `TestShutdownErrors` |
| in-flight request cut off at SIGTERM (`Close` instead of `Shutdown`) | `TestE2EServe`, `TestGracefulShutdown`, `TestShutdownDeadline/request` |
| in-flight request cancelled by the signal (`BaseContext` derived from the signal context) | `TestE2EServe`, `TestGracefulShutdown` |
| components shut down before requests drain | `TestE2EServe`, `TestGracefulShutdown`, `TestShutdownDeadline/request` |
| a component's `Shutdown` not bounded by the deadline | `TestShutdownDeadline/component` |
| requests cut off at the deadline not reported | `TestShutdownDeadline/request` (survived the first run, because the components' expired-context errors masked it; fixed by adding `ErrRequestsCutOff` and checking it) |
| a component's shutdown error swallowed | `TestShutdownErrors`, `TestShutdownDeadline/component` |
| a listener failure ignored (`Serve` blocks) | `TestListenerFailure` (it now fails after 30 s instead of relying on the test binary's timeout) |
| duplicate component names accepted | `TestNewRejects/duplicate_name` |
| h2c off | `TestHealthzAsSoonAsListening` |
| `/version` ignores the build info | `TestVersion`, `TestE2EServe` |
| `-ldflags` values not passed to the runtime | `TestE2EServe` |
| an unknown component ignored | `TestServeRejectsBadSetup/unknown_component` |
| an optional toolset failure hidden from readiness | `TestE2EServe` |
| the MCP handler mounted with `http.StripPrefix` | `TestE2EServe` |
| server limits from config ignored (`max_in_flight`) | `TestServeRejectsBadSetup/negative_limit` |
| unknown config fields accepted | `TestServeRejectsBadSetup/unknown_config_field` |

The config-limit plant is caught only through validation: whether the limits act on traffic is `pkg/mcp/server`'s tests' job (T018).

### For T005 (image)

- Build: `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE" ./cmd/agentkit` (14 MB today).
- Entrypoint `agentkit serve`. Set `AGENTKIT_ADDR=:8080` in the image: the default is loopback, which a port mapping cannot reach. Expose 8080.
- `AGENTKIT_CONFIG=/etc/agentkit/config.json` (mount or bake a config); the image has no default toolsets unless one is baked in.
- Health without a shell (`agentkit healthcheck`, T005): `GET /healthz` for liveness, `GET /readyz` for readiness (200 also when degraded). Neither needs auth.
- Logs are JSON on stderr. SIGTERM shuts down cleanly with exit 0; keep the orchestrator's grace period above `AGENTKIT_SHUTDOWN_TIMEOUT` (default 15 s; Docker's default stop timeout is 10 s, so set `stop_grace_period` / `--stop-timeout`, or lower the env).
- Distroless nonroot is fine: no files are written, no shell is needed.

### For T006 (Compose)

- One service per topology role, same image; components through `AGENTKIT_COMPONENTS` (only `mcp-server` exists yet), toolsets in `deploy/local/config/*.json` mounted at `AGENTKIT_CONFIG`.
- Healthcheck: `agentkit healthcheck` (T005) or `/readyz`; `depends_on: condition: service_healthy`.
- Test through the published port. A same-host reverse proxy forwarding to `127.0.0.1` gets 403 from the MCP SDK's DNS-rebinding protection (see the T018 wiring notes above); the container listening on `0.0.0.0:8080` behind a port mapping does not.
- Keep MCP stateless (the default). Stateful sessions hold a shutdown until its deadline.
- **Drain delay (not built):** on SIGTERM the listener closes at once. A request already on its way to it (accept queue, or an idle keep-alive connection the client is reusing) can be reset. Load balancers usually avoid this with a short delay between failing `/readyz` and closing the listener (Kubernetes: a `preStop` sleep). Add a `WithDrainDelay` to the runtime if a topology shows dropped requests during rolling restarts.
