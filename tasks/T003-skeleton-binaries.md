---
id: T003
title: agentkit binary with component runtime and health endpoints
epic: E01
status: todo
depends_on: [T001]
---

# T003: agentkit binary with component runtime and health endpoints

## Context

A walking skeleton: the `agentkit` binary runs and can start any set of components before they do anything useful, so containers, compose, smoke tests and CI can be built now and features slot in later. Design: [ENGINEERING.md §1.5](../docs/ENGINEERING.md#15-components-mix-and-match-deployment).

## Scope

- `pkg/component`: the `Component` interface, a runtime that starts the configured components in order, mounts their handlers on one server, aggregates readiness, and shuts them down in reverse order.
- Server: `GET /healthz` (process alive), `GET /readyz` (all started components ready), `GET /version` (build info via `-ldflags`), `slog` JSON logging, graceful shutdown on SIGINT/SIGTERM with a deadline.
- `cmd/agentkit`: thin `main`. Reads the component list from env for now (`AGENTKIT_COMPONENTS=harness,tools`, `AGENTKIT_ADDR`, `AGENTKIT_LOG_LEVEL`); the YAML config package comes later.
- Placeholder `harness` and `tools` components that only report ready, so topologies can be exercised.

## Out of scope

- Real harness/tool functionality and the local/remote dependency wiring (comes with E04/E05).
- The `pkg/config` package.

## Acceptance criteria

- [ ] Only the components listed in config are started; an unknown name fails startup with a clear error.
- [ ] `/healthz` returns 200 as soon as the server listens.
- [ ] `/readyz` returns 503 until every started component is ready, then 200.
- [ ] On SIGTERM, in-flight requests complete, components shut down in reverse start order, and the process exits 0 within the deadline.
- [ ] `/version` reports the values set via `-ldflags`.
- [ ] Startup to `/healthz` 200 is under 50 ms (measured once, recorded in Notes; not a CI test).

## Notes

- Test `pkg/component` with fake components and `httptest`; `cmd/agentkit` itself is covered by the smoke suite (T007), not unit tests.
