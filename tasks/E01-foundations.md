---
id: E01
title: Engineering foundations (tooling, tests, local pods)
status: todo
---

# E01: Engineering foundations

Put [docs/ENGINEERING.md](../docs/ENGINEERING.md) into practice before feature work starts, so every later package lands with the same shape, the same test tiers, and a one-command local environment.

## Outcome

- `make check` runs lint, import-boundary rules, and unit tests.
- `scripts/dev.sh up` brings up a working stack; `scripts/dev.sh smoke` proves it works.
- CI runs the same commands.

## Tasks

Ordered roughly by dependency.

- [ ] [T001](T001-tooling-and-lint.md) Toolchain, Makefile, lint, import boundaries
- [ ] [T002](T002-testkit.md) Test helpers: testkit and fake clock
- [ ] [T003](T003-skeleton-binaries.md) Skeleton binaries with health endpoints
- [ ] [T004](T004-fakellm.md) `fakellm` scripted model server
- [ ] [T005](T005-container-image.md) Distroless container image
- [ ] [T006](T006-compose-and-dev-script.md) Compose topologies and `scripts/dev.sh`
- [ ] [T007](T007-e2e-smoke-suite.md) E2E smoke suite
- [ ] [T008](T008-ci-pipeline.md) CI pipeline
- [ ] [T009](T009-kind-cluster.md) kind cluster and k8s overlays (optional, can slip to a later epic)

```
T001 ─┬─ T002
      └─ T003 ─┬─ T005 ─ T006 ─ T007 ─ T008
           T004┘                  └─ T009
```

## Next epics (to be broken down)

- E02 Core interfaces: `llm.Model`, `tool.Tool`/registry, `store.Store`, credential store, tenancy context, each with its `xxxtest` package and conformance suite.
- E03 Tool protocol: protobuf, gRPC + ConnectRPC, `toolserver`.
- E04 Harness loop on top of E02 (with `fakellm`).
- E05 MCP client/server/bridge (`mcp` topology).
- E06 Credentials backends (`secrets` topology).
