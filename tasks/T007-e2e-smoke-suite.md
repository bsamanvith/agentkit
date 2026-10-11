---
id: T007
title: E2E smoke suite
epic: E01
status: todo
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

- [ ] `scripts/dev.sh smoke` passes against `allinone` and against `split`.
- [ ] Stopping one service makes the suite fail with a message naming that service (verified once by hand, recorded in Notes).
- [ ] The suite runs in under 30 s at skeleton level.

## Notes

-
