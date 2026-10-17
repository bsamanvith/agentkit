---
id: T007
title: E2E smoke suite
epic: E01
status: done
depends_on: [T006]
---

# T007: E2E smoke suite

## Context

A small black-box suite that proves a running topology works. It grows by a few flows per epic, never into an exhaustive suite: details are proven by unit and integration tests ([ENGINEERING.md §2.3](../docs/ENGINEERING.md#23-how-many-tests-one-per-behavior-not-per-function)).

## Scope

**Decision (planner, after T024):** the smoke suite reuses the replay approach from `cmd/agentkit/e2e_test.go`: the same recorded fixtures, served by `fakellm` (T004) as a container, with the binary pointed at it through `ANTHROPIC_BASE_URL`. It adds only what replay e2e cannot cover: real containers, networking between them, healthchecks.


- `test/e2e` package, `//go:build e2e`.
- Targets come from env (`AGENTKIT_HARNESS_URL`, `AGENTKIT_TOOLS_URL`, ...) set by `scripts/dev.sh smoke`; tests for services not in the current topology skip.
- Helpers: a client for each service, `testkit.Eventually` for async results.
- Initial flows (skeleton level): health/ready/version for every service in the topology.
- Convention doc in `test/e2e/README.md`: each epic adds at most a handful of flows, one per user-visible feature.

## Out of scope

- Load and performance tests.

## Acceptance criteria

- [x] `make smoke` passes against the split deployment (and `make k8s-smoke` against the cluster); `allinone` is covered by `make web-smoke`.
- [x] Stopping one service makes the suite fail with a message naming that service (verified once by hand, recorded in Notes).
- [x] The suite runs in under 30 s at skeleton level.

## Notes

- 2026-10-17 (rescoped with T006): Compose and `scripts/dev.sh` were replaced by Docker Desktop's Kubernetes (`make k8s-up`), and `fakellm` (T004) is not built, so the suite runs against two topologies: `make smoke` (`scripts/smoke.sh`: the split deployment's three binaries, built with their images' tags, as processes, on the console's fake model, `web/apps/console/e2e/fake-model.ts`) and `make k8s-smoke` (the cluster, real model). Targets come from `AGENTKIT_URL` and `AGENTKIT_API_TOKEN`. CI runs `make smoke` in the `web` job, and now builds the split images (`make images-split`) in the `image` job.
- Flows: health/ready/version (each readiness component), auth (401 without the token), the console's page, the toolsets the API lists, one chat run with a tool call, kept by the store. About 0.1 s against a running stack, 5 s for `make smoke` with the builds.
- Stopping a service, checked by hand: the runner, `runner: run failed: ... connection refused` and `agent: degraded: runner at ...`; the toolsets, `toolsets: the echo tool failed` and `toolsets: GET /v1/toolsets: 500`.
- Found while writing it: a chat run whose runner could not be reached reported `run_done` with no answer and dropped the user's message (the inline retry took "transcript does not end on the user" for "the model answered"). Fixed in `pkg/runner/exec.go` (`TestRetryBeforeTheRunStartedSendsTheMessageAgain`).
- Fixed 2026-10-17: with the toolsets down, the agent component reads `degraded` in `/readyz` ("toolsets at ...") and the catalog lists those toolsets as `unavailable` instead of answering 500 (`TestE2ESplitDeployment` kills the toolsets process to check).
