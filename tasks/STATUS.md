# Plan status

> Maintained by the planner ([docs/WORKFLOW.md](../docs/WORKFLOW.md)). Updated 2026-10-11 at `main` `ee02b0f`, CI green. Task files are the source of truth for each task; this page is the overview.

## Milestones

| Milestone | Epic | State |
|---|---|---|
| M0 Tooling: `make check`, lint, architecture test, CI | [E01](E01-foundations.md) | Done |
| M1 `pkg/tool` | [E03](E03-tool-core-and-mcp.md) | Done |
| M2 Toolsets from config; one MCP server, one path per toolset | [E03](E03-tool-core-and-mcp.md) | Done |
| M3 `llm`, Anthropic adapter, harness, `flow`, `agentstep`, `agentkit run` | [E04](E04-llm-harness-flow.md) | Done |
| M4 Store, runner, compaction | [E05](E05-store-runner.md) | In progress |
| M5 Agent API, `agentkit serve`, image, Compose, smoke tests | [E09](E09-agent-api.md), [E01](E01-foundations.md) | In progress |
| M6 Remote tools over ConnectRPC | E06 (to plan) | Planned |
| Credentials, auth, tenancy at the edge | E07 (to plan) | Planned |
| More model providers | [E08](E08-model-providers.md) | Later |
| Agent skills | [E02](E02-agent-skills.md) | In progress (`write-tests` exists) |

## Done

T001 tooling · T002 testkit · T003 `agentkit serve` · T017 `pkg/tool` · T018 MCP server · T019 `pkg/clock` · T020 `pkg/llm` · T021 Anthropic adapter · T022 harness · T023 `flow` · T024 `agentkit run`, `agentstep` · T026 `pkg/toolset` · T027 `pkg/tenancy` · T032 flow step-tree check

## Running

| Task | What | Notes |
|---|---|---|
| [T028](T028-store-interfaces.md) | Store interfaces, in-memory store, conformance suites | Scope grew (run lifecycle); may split RunStore parts out |
| [T013](T013-skill-write-tests.md) | `write-tests` skill | Skill exists; dry runs pending |

## Next (ready to start)

| Task | What | Why now |
|---|---|---|
| [T036](T036-agent-api-v0.md) | Agent API v0: runs over HTTP + SSE, in memory | Shortest path to a deployable agent API (card queued) |
| [T005](T005-container-image.md) | Distroless image, `agentkit healthcheck` | With T036, enough to deploy and test (card queued) |
| [T033](T033-harness-clock.md) | Harness on `pkg/clock` | Small, independent |
| [T035](T035-rerecord-anthropic-fixtures.md) | Re-record Anthropic fixtures from the real API | A key is available now |
| [T010](T010-skills-layout.md) | `AGENTS.md`, `CLAUDE.md`, skills README, skill and task lint | Small, independent |

## Blocked on other tasks

| Task | Waits for |
|---|---|
| [T029](T029-store-sqlite.md) SQLite store | T028 |
| [T030](T030-runner.md) Runner (recovery, retry, approvals, cancel) | T028 (and T033 is nice to have) |
| [T031](T031-compaction.md) Compaction | T030 |
| [T037](T037-agent-api-v1.md) Agent API v1 on the runner | T036, T030 |
| [T006](T006-compose-and-dev-script.md) Compose, [T007](T007-e2e-smoke-suite.md) smoke suite, [T008](T008-ci-pipeline.md) full CI | T005 (T004 `fakellm` for keyless CI) |
| [T034](T034-nightly-ci.md) Nightly live tests and perf report | The `ANTHROPIC_API_KEY` repository secret |

## Waiting on the owner

- **Where to deploy** the API server (a VM with Docker, Cloud Run, Fly.io, Kubernetes, ...). A deploy task is written once this is known.
- **`ANTHROPIC_API_KEY` repository secret** on GitHub, for T034.
- **Delete the `review/t022-base` branch**: everything in it is on `main`.
- Whether to start the queued cards (T033, T035, T010).

## Decisions log (recent)

Each is recorded in the linked file; this list is for orientation.

- Own harness and `flow`, not ADK, Genkit or Eino; frameworks integrate through MCP ([PLAN.md decisions](../docs/PLAN.md#decisions)).
- One MCP server process, one URL path per toolset; toolsets compiled in, chosen by config (E03).
- Store: `ConversationStore`, `RunStore` with leases and a run lifecycle, `flow.Checkpointer`; `pkg/runner` turns events into entries; SQLite with a pure-Go driver ([E05](E05-store-runner.md)).
- When stopped runs continue: approval, recovery after a crash, retry with backoff, compaction; final runs never resume; one unfinished run per conversation ([E05](E05-store-runner.md)).
- Saved transcripts are not deleted at run end; agent step output holds a reference ([T030](T030-runner.md)).
- Public agent API is HTTP + JSON with SSE; ConnectRPC stays internal ([E09](E09-agent-api.md)).
- E04 closed; T025 moved to E08. `fakellm` kept, extracted from the replay server ([T004](T004-fakellm.md)).
