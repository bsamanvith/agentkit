# Plan status

> Maintained by the planner ([docs/WORKFLOW.md](../docs/WORKFLOW.md)). Updated 2026-10-12 at `main` `448c5d4` plus planner commits, CI green. Task files are the source of truth for each task; this page is the overview.

## Milestones

| Milestone | Epic | State |
|---|---|---|
| M0 Tooling: `make check`, lint, architecture test, CI | [E01](E01-foundations.md) | Done |
| M1 `pkg/tool` | [E03](E03-tool-core-and-mcp.md) | Done |
| M2 Toolsets from config; one MCP server, one path per toolset | [E03](E03-tool-core-and-mcp.md) | Done |
| M3 `llm`, Anthropic adapter, harness, `flow`, `agentstep`, `agentkit run` | [E04](E04-llm-harness-flow.md) | Done |
| M4 Store, runner, compaction | [E05](E05-store-runner.md) | In progress: `pkg/store` (interfaces, memory store, suites, run lifecycle) done |
| M5 Agent API, `agentkit serve`, image, Compose, smoke tests | [E09](E09-agent-api.md), [E01](E01-foundations.md) | In progress: `agentkit serve`, agent API v0 and the image done; **deployable** |
| M6 Remote tools over ConnectRPC | E06 (to plan) | Planned |
| Credentials, auth, tenancy at the edge | [E07](E07-auth-credentials.md) | **Planned** (T039–T045); T039 and T042 ready. Needed before any public deployment: `/mcp/` has no auth today |
| First connectors: Tavily, Exa (search, extract) | [E10](E10-search-connectors.md) | **Planned** (T046, T047), both ready; no auth work needed (Tavily keyless or keyed, Exa keyed from env) |
| More model providers | [E08](E08-model-providers.md) | Later |
| Agent skills | [E02](E02-agent-skills.md) | In progress: `write-tests` exists and was dry-run on `pkg/flow` |

## Verified end to end (planner, 2026-10-11)

The stripped binary of `main` (14.5 MB; image about 17.9 MB of the 20 MB budget), run as a deployment would run it: `AGENTKIT_COMPONENTS=agent,mcp-server`, listening on `0.0.0.0` with `AGENTKIT_API_TOKEN`, real Claude.
- `/readyz` ready (agent, mcp-server); `/v1/runs` without the token: 401.
- A streamed run used the demo arithmetic tool and answered 391 (17×23); events `run_start`, `text_delta`, `tool_call`, `tool_result`, 2× `turn_done`, `run_done`.
- A JSON follow-up in the same conversation answered 400.
- `/mcp/demo` listed `calculate`, `current_time`, `echo` from the same process.
- SIGTERM: draining runs, shutting down, stopped, exit 0.

Not verified here: the container itself (no Docker daemon in the planner's environment; T005 verified the image with Docker).

The same checks are one command on a laptop: `scripts/try-agent.sh` ([docs/QUICKSTART.md](../docs/QUICKSTART.md)), verified on 2026-10-12 with `--demo` and with Ctrl-C in serve mode.

## Done

T001 tooling · T002 testkit · T003 `agentkit serve` · T005 image and `agentkit healthcheck` · T017 `pkg/tool` · T018 MCP server · T019 `pkg/clock` · T020 `pkg/llm` · T021 Anthropic adapter · T022 harness · T023 `flow` · T024 `agentkit run`, `agentstep` · T026 `pkg/toolset` · T027 `pkg/tenancy` · T028 `pkg/store` · T032 flow step-tree check · T036 agent API v0

## Running or waiting

| Task | State |
|---|---|
| [T013](T013-skill-write-tests.md) `write-tests` dry runs | The `pkg/flow` dry run found a real bug (a rejected run could be resumed into `failed`); **fixed on `main`** by the planner (`a645012`) with a deterministic regression test. The dry run's flow tests (invariants after every run, seeded simulation, `FuzzSimulatedFlow`) are on `main` as `5a13386`. The T018 dry run stopped unfinished. |

## Next (ready to start)

| Task | What | Why now |
|---|---|---|
| [T038](T038-store-admin.md) | `store.Admin`, the cross-tenant read for recovery | Small; unblocks T029 and T030 in parallel. **Task card queued** (2026-10-12) |
| [T046](T046-connector-tavily.md) | `connectors/tavily`: search and extract, keyless or keyed | Owner's first connector; **task card queued** |
| [T047](T047-connector-exa.md) | `connectors/exa`: search and extract | Owner's second connector, parallel with T046; **task card queued**; live tests need `EXA_API_KEY` in sessions |
| [T039](T039-auth-principals-middleware.md) | `pkg/auth`: principals, API keys, edge middleware | Starts E07's inbound track; new package only, parallel with E05 |
| [T042](T042-credentials-core.md) | `pkg/credentials`: secrets, sealer, vault, memory backend | Starts E07's credentials track; new package only, parallel with everything |
| [T029](T029-store-sqlite.md) | SQLite store | After T038; persistence for the API (T037) |
| [T030](T030-runner.md) | Runner: entries, cache, leases, recovery, retry, approvals, cancel | After T038; the core of M4 |
| [T006](T006-compose-and-dev-script.md) | Compose topologies and `scripts/dev.sh` | Image exists; repeatable local runs |
| [T033](T033-harness-clock.md) | Harness on `pkg/clock` | Small, independent |
| [T035](T035-rerecord-anthropic-fixtures.md) | Re-record Anthropic fixtures from the real API | A key is available |
| [T010](T010-skills-layout.md) | `AGENTS.md`, `CLAUDE.md`, skills README, lint | Small, independent |
| [T034](T034-nightly-ci.md) | Nightly benchstat report (non-gating, no secrets) | Small; starts the perf history |

## Blocked on other tasks

| Task | Waits for |
|---|---|
| [T031](T031-compaction.md) Compaction | T030 |
| [T037](T037-agent-api-v1.md) Agent API v1 on the runner | T030, T029 |
| [T040](T040-serve-auth-wiring.md) `serve` behind auth, [T041](T041-auth-jwt-oidc.md) JWT/OIDC | T039 |
| [T043](T043-credentials-backends.md) credential backends, [T044](T044-credentials-in-tools.md) credentials in tools | T042 (T043's SQLite part also T029) |
| [T045](T045-connect-accounts.md) Connecting accounts (OAuth2 + PKCE) | T040, T043, T044 |
| [T004](T004-fakellm.md) `fakellm`, [T007](T007-e2e-smoke-suite.md) smoke suite, [T008](T008-ci-pipeline.md) full CI | T006 |

## Waiting on the owner

1. **`EXA_API_KEY` in Claude sessions** (like `ANTHROPIC_KEY`) for T047's live tests; optional `TAVILY_API_KEY` (Tavily's live tests run keyless without it).
2. **Delete the `review/t022-base` branch**, and the merged `claude/*` session branches including `claude/t013-flow-tests` (all their work is on `main`).

## Owner decisions recorded

- 2026-10-12: no Anthropic key in GitHub. Live tests run in Claude sessions (`ANTHROPIC_KEY`, mapped by `make test-live`); T034 is a perf report only.
- 2026-10-12: first connectors are Tavily and Exa, for search and extraction ([E10](E10-search-connectors.md)); they run in parallel with E07, keys from the environment for now.
- 2026-10-12: the owner will say how to deploy; no deploy task until then.
- 2026-10-12: the planner approves and lands the `pkg/flow` rejected-resume fix from the T013 dry run.

## Watch items

- One local `make check` failed under CPU load and could not be identified (its output was discarded); 6 repeated runs and CI were clean. If it recurs, keep the log and fix the test.

## Decisions log (recent)

Each is recorded in the linked file; this list is for orientation.

- Own harness and `flow`, not ADK, Genkit or Eino; frameworks integrate through MCP ([PLAN.md decisions](../docs/PLAN.md#decisions)).
- One MCP server process, one URL path per toolset; toolsets compiled in, chosen by config (E03).
- Store: `ConversationStore`, `RunStore` with leases and a run lifecycle, `flow.Checkpointer`; `pkg/runner` turns events into entries; SQLite with a pure-Go driver ([E05](E05-store-runner.md)).
- When stopped runs continue: approval, recovery after a crash, retry with backoff, compaction; final runs never resume; one unfinished run per conversation ([E05](E05-store-runner.md)).
- Recovery across tenants through a separate `store.Admin` (T038), never reachable from request handling.
- Saved transcripts are not deleted at run end; agent step output holds a reference ([T030](T030-runner.md)).
- Public agent API is HTTP + JSON with SSE; ConnectRPC stays internal ([E09](E09-agent-api.md)).
- E07 (2026-10-12): one auth middleware for `/v1` and `/mcp/`; API keys (hashed) then OIDC/JWT, we are a resource server only; hidden toolsets answer 404; no open MCP off loopback; credentials sealed with AES-GCM bound to tenant/user/slot, tools get a source for one slot, no token passthrough ([E07](E07-auth-credentials.md)).
- E04 closed; T025 moved to E08. `fakellm` kept, extracted from the replay server ([T004](T004-fakellm.md)).
