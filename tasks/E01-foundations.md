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
- [ ] [T004](T004-fakellm.md) `fakellm` scripted model server (needs the `llm` work from M3)
- [ ] [T005](T005-container-image.md) Distroless container image
- [ ] [T006](T006-compose-and-dev-script.md) Compose topologies and `scripts/dev.sh`
- [ ] [T007](T007-e2e-smoke-suite.md) E2E smoke suite
- [ ] [T008](T008-ci-pipeline.md) Full CI pipeline (images, integration, smoke)

## CI follow-ups

- [ ] [T034](T034-nightly-ci.md) Nightly CI: live tests and a non-gating perf report (needs the `ANTHROPIC_API_KEY` repository secret)

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
- E07 Credentials, auth and tenancy at the edge: credential backends (`secrets` topology, kind), and auth plus tenant resolution as `server.WithMiddleware` on every MCP toolset path (`pkg/tenancy` is ready). *(to be planned)*
- Later: MCP client and bridge, Postgres and tenancy, observability.
