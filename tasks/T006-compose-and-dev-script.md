---
id: T006
title: Compose topologies and scripts/dev.sh
epic: E01
status: todo
depends_on: [T005]
---

# T006: Compose topologies and scripts/dev.sh

## Context

One command brings up any topology from [ENGINEERING.md §3.2](../docs/ENGINEERING.md#32-topologies) and returns only when it is usable.

## Scope

- `deploy/local/compose.yaml` with profiles: `allinone`, `split`, `deps`, `obs`. (`mcp` and `secrets` profiles are added by their epics; leave a commented placeholder.)
- Healthchecks on every service; `depends_on: condition: service_healthy`.
- Per-topology config in `deploy/local/config/`; fixed host ports documented in `deploy/local/README.md`.
- `fakellm` is part of every app topology; `AGENTKIT_LLM=anthropic` + `ANTHROPIC_API_KEY` switches to the real API.
- `scripts/dev.sh` subcommands: `up`, `down [-v]`, `status`, `logs`, `smoke`, `reset`, `run`, `help`.
  - Remembers the last topology in `.data/dev-topology` so `reset`/`smoke` need no args.
  - `run <cmd>` does `go run ./cmd/<cmd>` with SQLite under `.data/`, fakellm started alongside if needed.
  - `bash` + `set -euo pipefail`, `shellcheck`-clean, works on Linux and macOS.
- Prints URLs of the running services after `up`.

## Out of scope

- Kubernetes (T009).
- The e2e test contents (T007): `smoke` just invokes them.

## Acceptance criteria

- [ ] `scripts/dev.sh up` (default `allinone`) returns with all services healthy; `status` shows them.
- [ ] `scripts/dev.sh up split` starts `harnessd`, `toolserver`, `postgres`, `fakellm`, all healthy.
- [ ] `scripts/dev.sh up deps` exposes Postgres at the documented port and `AGENTKIT_PG_URL` from `status` connects.
- [ ] `scripts/dev.sh down -v` leaves no containers or volumes for the project.
- [ ] `scripts/dev.sh up split obs` combines profiles.
- [ ] `shellcheck scripts/*.sh` passes (part of `make lint`).

## Notes

- These criteria are verified by the CI job in T008 running the script, not by Go tests.
