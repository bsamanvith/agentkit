---
id: T003
title: Skeleton binaries with health endpoints
epic: E01
status: todo
depends_on: [T001]
---

# T003: Skeleton binaries with health endpoints

## Context

A walking skeleton: `harnessd`, `toolserver`, and `allinone` exist and run before they do anything useful, so containers, compose, smoke tests and CI can be built now and features slot in later.

## Scope

- `cmd/harnessd`, `cmd/toolserver`, `cmd/allinone`, each a thin `main` that wires packages.
- Shared `internal/server` (or `pkg/transport` seed): HTTP server with `GET /healthz` (process alive) and `GET /readyz` (dependencies ready), `slog` JSON logging, graceful shutdown on SIGINT/SIGTERM with a deadline.
- Config from env only for now (`AGENTKIT_ADDR`, `AGENTKIT_LOG_LEVEL`); the YAML config package comes later.
- `GET /version` returning build info (version, commit) injected via `-ldflags`.

## Out of scope

- Any harness/tool functionality.
- The `pkg/config` package.

## Acceptance criteria

- [ ] `/healthz` returns 200 as soon as the server listens.
- [ ] `/readyz` returns 503 until registered readiness checks pass, then 200.
- [ ] On SIGTERM, in-flight requests complete and the process exits 0 within the shutdown deadline.
- [ ] `/version` reports the values set via `-ldflags`.
- [ ] Binary startup to `/healthz` 200 is under 50 ms (measured once, recorded in Notes; not a CI test).

## Notes

- Test the server package with `httptest`; the `cmd/` mains are covered by the smoke suite (T007), not unit tests.
