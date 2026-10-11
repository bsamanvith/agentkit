---
id: T003
title: agentkit binary with component runtime and health endpoints
epic: E01
status: todo
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

- [ ] Only the components listed in config are started; an unknown name fails startup with a clear error.
- [ ] `/healthz` returns 200 as soon as the server listens.
- [ ] `/readyz` returns 503 until every started component is ready, then 200.
- [ ] On SIGTERM, in-flight requests complete, components shut down in reverse order, and the process exits 0 within the deadline.
- [ ] `/version` reports the values set via `-ldflags`.
- [ ] Startup to `/healthz` 200 is under 50 ms (measured once, recorded in Notes; not a CI test).

## Notes

- Test `pkg/component` with fake components and `httptest`; `cmd/agentkit` itself is covered by the smoke suite (T007), not unit tests.
