---
id: E01
title: Engineering foundations (tooling, tests, local pods)
status: in-progress
---

# E01: Engineering foundations

Put [docs/ENGINEERING.md](../docs/ENGINEERING.md) into practice. Split in two phases following the build order ([§0](../docs/ENGINEERING.md#0-build-order)): the checks every change needs come first (M0); containers and local pods come once there are real components to run (M5).

## Phase 1: M0, before any feature

- [x] [T001](T001-tooling-and-lint.md) Toolchain, Makefile, lint, architecture test, minimal CI
- [x] [T002](T002-testkit.md) Test helpers (testkit)

## Phase 2: M5, after E03 (real components exist)

- [x] [T003](T003-skeleton-binaries.md) `agentkit` binary with component runtime and health endpoints
- [x] [T004](T004-fakellm.md) `fakellm` scripted model server (needs the `llm` work from M3)
- [x] [T005](T005-container-image.md) Distroless container image (read T003's "For T005" Notes first)
- [ ] [T006](T006-compose-and-dev-script.md) Local deploy on Docker Desktop's Kubernetes (`make k8s-up`)
- [x] [T113](T113-compose-local.md) Local deploy in Docker Compose, beside the Kubernetes one (`make compose-up`) — in review: `docker compose up` not yet run (needs Docker)
- [x] [T007](T007-e2e-smoke-suite.md) E2E smoke suite
- [x] [T008](T008-ci-pipeline.md) Full CI pipeline (images, integration, smoke)
- [x] [T102](T102-component-images.md) An image per component, a migrate image, and the all-in-one image (after T037)

## CI follow-ups

- [x] [T034](T034-nightly-ci.md) Nightly perf report in CI, non-gating and with no secrets (live tests run in Claude sessions)

## When first needed

- [ ] [T019](T019-clock.md) `clock` package and fake clock (moved to [E05](E05-store-runner.md))

Moved out: [T009](T009-kind-cluster.md) kind cluster now belongs to the credentials epic (E07).

```
T001 ─ T002 ─ (E03: T017 ─ T018) ─ T003 ─ T005 ─ T006 ─ T007 ─ T008
                                   T004 ┘
```

## All epics

- E01 Foundations (this file).
- [E02](E02-agent-skills.md) Agent skills, growing alongside the code.
- [E03](E03-tool-core-and-mcp.md) Tool core and MCP server (M1, M2).
- [E04](E04-llm-harness-flow.md) `llm`, Anthropic adapter, harness and `flow` (M3). **Current focus.**
- E05 `store` SQLite, runs and checkpoints (M4). *(to be planned)*
- E06 Tool protocol over ConnectRPC, remote registry, `split` topology (M6). *(to be planned)*
- [E07](E07-auth-credentials.md) Credentials, auth and tenancy at the edge (planned 2026-10-12).
- Later: MCP client and bridge, Postgres and tenancy, observability.
- [ ] [T056](T056-test-speed.md) Test speed: `make test` back under 30 s (52 s on 2026-10-12)
