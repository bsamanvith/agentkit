# Plan status

> Maintained by the planner ([docs/WORKFLOW.md](../docs/WORKFLOW.md)). Updated 2026-10-24 at `main` `9899c35` plus bsamanvith/agentkit#113 (T047/T048 per-user keys, the status pass). Task files are the source of truth for each task; this page is the overview. 108 task files: 98 done, 8 in review (each waits on something a cloud session cannot do, below), 2 paused.

## Milestones

| Milestone | Epic | State |
|---|---|---|
| M0 Tooling: `make check`, lint, architecture test, CI | [E01](E01-foundations.md) | Done; local deploys (T006 k8s, T113 Compose) wait for a run on the owner's Docker Desktop, T056 for a decision |
| M1 `pkg/tool`; M2 toolsets from config, one MCP server | [E03](E03-tool-core-and-mcp.md) | Done |
| M3 `llm`, Anthropic adapter, harness, `flow`, `agentkit run` | [E04](E04-llm-harness-flow.md) | Done |
| M4 Store, runner, compaction | [E05](E05-store-runner.md) | Done: memory, SQLite and Postgres stores, the runner, compaction, run queue order (T099) |
| M5 Agent API, `agentkit serve`, images, smoke tests | [E09](E09-agent-api.md), [E01](E01-foundations.md) | Done: six images built and budget-checked in CI (T102), `make smoke`, `make integration` |
| M6 Remote tools and runners over `pkg/rpc` | (T102) | Done: the split deployment; the caller's tenant and user cross `/rpc/` (2026-10-24) |
| Credentials, auth, tenancy at the edge | [E07](E07-auth-credentials.md) | Done: API keys, JWT/OIDC, local users, the vault, credentials in tools, connecting accounts; T009 superseded by T006 |
| Search connectors: Tavily, Exa, Parallel | [E10](E10-search-connectors.md) | Done: Exa and Parallel use **each user's own key** from a credential slot (2026-10-24); tool-use evals (`make eval`) |
| Clients: console, CLI and headless, and the APIs behind them | [E11](E11-clients.md) | Done; T087 (UX design part 2) waits for the owner's review of mockup v4 |
| Skills and memory | [E12](E12-skills-memory.md) | Done; T060 (semantic search) waits for an embedding provider's key |
| Agent protocols: A2UI, A2A | [E13](E13-agent-protocols.md) | Done (live runs passed 2026-10-24) |
| Tasks (background agents) and agent messaging | [E14](E14-background-agents-messaging.md) | Done; supervised tasks wait for a person (T084, 2026-10-24) |
| Agent definitions as versioned data | [E15](E15-agent-definitions.md) | T075 and T077 (console agent builder) done; T076 (skills store) **to plan** |
| Agents with a choice of model | [E24](E24-agent-models.md) | T125 models, T126 tool description overrides, T127 chat model picker, T077 console agent builder and T128 Gemini done |
| Runtimes and jobs | [E16](E16-runtimes-jobs.md) | **Shaped**, T078–T081 to plan (the ids collide with existing tasks: renumber when planned); owner's call |
| Tool safety and control | [E17](E17-tool-safety-control.md) | Done (live runs passed 2026-10-24) |
| Streaming and the server backend | [E18](E18-streaming-backend.md) | Done |
| External MCP servers | [E19](E19-external-mcp-servers.md) | Done |
| Runner pools | [E20](E20-runner-pools.md) | T104, T105 done; T106 (gVisor) and T107 (microVM) **paused** by the owner (2026-10-22) |
| Agent memory: plain text, size-capped, visible | [E23](E23-agent-memory.md) | T121 (readable text only), T122 (Postgres backend), T123 (memory on the split runner), T124 (memory API, console Memory section), T129 (memory loaded into messages, grep past a threshold) done |
| Google connectors | [E21](E21-google-connectors.md) | Gmail, Calendar done on synthetic cassettes; T111 Drive waits for a Google test account |
| Connectors page, chat tool picker, tool search | [E22](E22-connectors-and-tool-picker.md) | **Done** 2026-10-25: T114–T120 (owner's request), T131 (every connector on k8s and Compose) |
| More model providers | [E08](E08-model-providers.md) | T128 Gemini done; T025 (OpenAI-compatible) waits for an OpenAI key or an Ollama endpoint |
| Agent skills | [E02](E02-agent-skills.md) | Done; T010 waits for one check with Codex |

## Verified with the real model (2026-10-24)

`make test-live` with the project's Anthropic key: every live test passes (skills, memory recall, agents over MCP, A2UI surfaces, A2A streaming, deferred tools native and emulated, deferred skill loading, serve and `ask` with the search agent). The run found a real bug, now fixed: the API refuses a tool result that mixes tool references with text, which a native `skill_load` sent. Exa's and Parallel's own live tests still need a key, which each user now brings.

## Done

T001 · T002 · T003 · T004 · T005 · T007 · T008 · T009 · T011 · T012 · T013 · T014 · T015 · T016 · T017 · T018 · T019 · T020 · T021 · T022 · T023 · T024 · T026 · T027 · T028 · T029 · T030 · T031 · T032 · T033 · T034 · T035 · T036 · T037 · T038 · T039 · T040 · T041 · T042 · T043 · T044 · T045 · T046 · T047 · T048 · T049 · T050 · T051 · T052 · T053 · T054 · T055 · T057 · T058 · T059 · T061 · T062 · T063 · T064 · T065 · T066 · T067 · T068 · T069 · T070 · T071 · T072 · T073 · T074 · T075 · T078 · T082 · T083 · T084 · T085 · T086 · T088 · T089 · T090 · T091 · T092 · T093 · T094 · T095 · T096 · T097 · T098 · T099 · T100 · T101 · T102 · T103 · T104 · T105 · T108 · T109 · T110 · T112 · T121 · T122 · T123 · T124 · T129

## Open

| Task | State | Waits for |
|---|---|---|
| [T006](T006-compose-and-dev-script.md) | Review | `make k8s-up` / `k8s-down` on the owner's Docker Desktop (cloud sessions have no Docker) |
| [T113](T113-compose-local.md) | Review | `make compose-up` / `compose-down` on the owner's Docker Desktop |
| [T010](T010-skills-layout.md) | Review | one check that Codex lists the same skills |
| [T025](T025-llm-openai-compatible.md) | Review | an OpenAI key or an Ollama endpoint for its live test |
| [T056](T056-test-speed.md) | Review | a decision: "under 30 s" no longer fits the repository (on 2026-10-24 `pkg/store/sqlite` alone takes 91 s and `pkg/runner` 45 s under `-race`); a new budget or a split tier |
| [T060](T060-memory-search-semantic.md) | Review | the first embedding provider (Voyage AI, OpenAI, or local) and its key |
| [T087](T087-console-ux-design-2.md) | Review | the owner's approval of mockup v4 |
| [T111](T111-connector-google-drive.md) | Review | a Google test account to record real cassettes, then the mutation list |
| [T130](T130-require-auth-cf-access.md) | Review | bsamanvith/agentkit#112: review fixes in progress; tests to pass the gate and CI |
| [T132](T132-stored-agents-on-runner.md) | Review | its PR: stored agents (the builder, the model picker) on the split runner |
| [T106](T106-sandbox-pool.md), [T107](T107-vm-pool.md) | Paused | the owner (2026-10-22: only the shared runner for now) |

## Waiting on the owner

1. **Docker Desktop runs** of `make k8s-up` and `make compose-up` (T006, T113).
2. **First embedding provider** for semantic memory (T060).
3. **Next larger goal**: E15's skills store and its console editor (T076), E16's runtimes and jobs, or cloud deploy (managed Postgres: RDS, Cloud SQL, Neon, ...).
4. **Exa and Parallel in the deployed configs**: `deploy/k8s` and `deploy/compose` don't serve the search toolsets yet. Adding them needs `credentials.backend: postgres` and `AGENTKIT_CREDENTIALS_KEY` on the api and toolsets containers.
5. **Delete merged branches**: `review/t022-base` and the merged `claude/*` session branches.

## Owner decisions recorded

- 2026-10-12: no Anthropic key in GitHub. Live tests run in Claude sessions (`ANTHROPIC_KEY`, mapped by `make test-live`); T034 is a perf report only.
- 2026-10-12: first connectors are Tavily, Exa and Parallel, for search and extraction ([E10](E10-search-connectors.md)); they run in parallel with E07, keys from the environment for now.
- 2026-10-12: the owner will say how to deploy; no deploy task until then. First target: the Docker image locally, used through the clients.
- 2026-10-12: streaming is **SSE down, HTTP commands up** (WebSocket later as a second binding of the same messages; no RSocket). Server backend: **Postgres** (source of truth, durable event log) + **Redis Streams** (live fan-out across replicas, command signals, rate limits, caches; never the only copy). Text deltas are live-only; replay comes from durable events. Leases stay in Postgres. An in-process bus serves single-node setups (E18).
- 2026-10-12: non-chat agent runs are **tasks** (`/v1/tasks`, matching A2A). Tools carry kind and risk; policies allow, flag, require approval for or deny calls by class; the environment can only narrow; interaction modes `interactive`, `supervised` and `autonomous` (tasks default to autonomous: no interrupting tools, approvals become denials); per-tool deferred loading; skills auto-load their tools; tasks can be steered (E17).
- 2026-10-12 (interview, [docs/PRODUCT.md](../docs/PRODUCT.md)): UI in **React + TS with shadcn/ui and Radix** (replaces "no framework"); shared `agent-ui` components for the desktop-first admin console and future mobile-first consumer apps; **admin console first**; agent definitions **stored and versioned** (E15); ephemeral harness **pluggable** (Go binary first, pi second; E16); farmer's assistant and finance tools are later products on the same core.
- 2026-10-12 (T053, PR #4): the agent API's wire types live in `pkg/agentapi/wire` (imports only `pkg/llm`). Archtest's `SharedChildren` exemption, approved by the owner, lets `pkg/agentapi` import it. Messages on the wire decode **leniently** (unknown fields ignored, unknown block types kept as `llm.Unknown` and stored nowhere), so the API's additive rule holds for messages too; roles stay strict, and a new role means `"v": 2`.
- 2026-10-12: background agents and agent messaging (E14): detached runs with a stored, resumable event stream; schedules and webhooks; mailboxes with sync or async messages, wake on reply, allowlists and loop limits. The runner and SQLite store move to the front.
- 2026-10-12: a dedicated UX design session (T070) lays out the console before T055 builds it; each task now lists the references to study.
- 2026-10-12: agent protocols (E13): agents stream UI as A2UI surfaces (validated against our catalog, rendered with the console's own components); our agents served over A2A and remote A2A agents usable as tools; AG-UI and MCP Apps later. Console look: neutral (grey, white, minimal black; near-black dark mode); tool calls as quiet one-line steps with details on demand.
- 2026-10-12: the web client is an admin console (login, chat with agent picker, conversations, agents, MCP toolsets and tools, credentials), API-first so Claude Code and other harnesses can drive everything; agents also over MCP ([docs/UI.md](../docs/UI.md)).
- 2026-10-12: skills and memory for the harness (E12): Agent Skills format with progressive disclosure; a context store and an opt-in search store (keyword, semantic, hybrid).
- 2026-10-12: two clients, a web client for people and a CLI with a headless mode for our tests and evals (E11). Evals (T050) later, after deploy and clients. Order: T052, T051, then clients.
- 2026-10-12: coverage-guided fuzzing runs in its own CI job, in parallel with `make check`; seed corpora stay in the unit tests.
- 2026-10-12: the planner approves and lands the `pkg/flow` rejected-resume fix from the T013 dry run.

## Watch items

- One local `make check` failed under CPU load and could not be identified (its output was discarded); 6 repeated runs and CI were clean. If it recurs, keep the log and fix the test.

## Decisions log (recent)

Each is recorded in the linked file; this list is for orientation.

- Own harness and `flow`, not ADK, Genkit or Eino; frameworks integrate through MCP ([PLAN.md decisions](../docs/PLAN.md#decisions)).
- One MCP server process, one URL path per toolset; toolsets compiled in, chosen by config (E03).
- Store: `ConversationStore`, `RunStore` with leases and a run lifecycle, `flow.Checkpointer`; `pkg/runner` turns events into entries; SQLite with a pure-Go driver ([E05](E05-store-runner.md)).
- When stopped runs continue: approval, recovery after a crash, retry with backoff, compaction; final runs never resume; one unfinished run per conversation ([E05](E05-store-runner.md)).
- Recovery across tenants through a separate `store.Admin` (T038), never reachable from request handling: handed out by a package function (`memory.Admin(s)`), not a method, so no type assertion on a `RunStore` reaches it (T038 review fix, `db65096`).
- Saved transcripts are not deleted at run end; agent step output holds a reference ([T030](T030-runner.md)).
- Public agent API is HTTP + JSON with SSE; ConnectRPC stays internal ([E09](E09-agent-api.md)).
- Connectors (2026-10-12): outputs identical across providers, inputs may follow the provider (Parallel extract takes `objective`); `max_content_chars` `{search, extract}`; `Retry-After` honored up to 10 s ([E10](E10-search-connectors.md)).
- Connector testing (2026-10-12): one kit (T049) with cassettes, a conformance suite with check IDs and staged rollout, honest fakes, drift checks, secret scans, `synthetic` fixtures kept in review; a separate eval tier (T050) for model behavior. Informed by Airbyte, LangChain, Meltano and Nango practice ([E10](E10-search-connectors.md)).
- E07 (2026-10-12): one auth middleware for `/v1` and `/mcp/`; API keys (hashed) then OIDC/JWT, we are a resource server only; hidden toolsets answer 404; no open MCP off loopback; credentials sealed with AES-GCM bound to tenant/user/slot, tools get a source for one slot, no token passthrough ([E07](E07-auth-credentials.md)).
- E04 closed; T025 moved to E08. `fakellm` kept, extracted from the replay server ([T004](T004-fakellm.md)).
- 2026-10-12 (planner, T029 handoff): `IMAGE_BUDGET_MB` 20 → 30 with every store driver in the one default binary (no build tags); CI builds the image and checks its size on every push (`image` job), which would have caught the `.dockerignore` break fixed in `0f22246`. The store contract rejects times outside 1678–2262 in every backend; T088 adds T029's two `storetest` gaps before the Postgres backend. Migrations run at startup by default, `agentkit migrate` for job-style deploys. Postgres row-level security is T093.
- 2026-10-12: external MCP servers (E19), owner's question "how do I enable my website to accept their MCP". An external server is an `mcp` toolset. Admins add it by URL (API or console, no restart) and users connect their own accounts (the Claude Team model). Auth: none, headers from a credential, or MCP OAuth (pre-registered client, then CIMD, then DCR; PKCE + `resource`). Tool definitions are pinned by hash, and a changed or new tool stays disabled until an admin reviews it. API-added URLs are https only, behind an SSRF-safe dialer. stdio only from the operator's config.
- 2026-10-13 (owner, T100): **chat runs stop with their user.** The stop button or a closed tab (after a 10 s reconnect grace) interrupts the run, which is shown `interrupted` and never recovered in the background. Continue is Resume; a new message cancels it and starts a new run; retryable model errors are retried inline. Background runs (E14) keep detaching and are recovered. This replaces the planner's "a dropped client detaches" note in T037.
- 2026-10-13 (T078 handoff): the phone drawer is our own, not shadcn's `Sheet`, because Radix's modal scroll lock injects a `<style>` the strict CSP blocks; T055 must solve modal overlays the same way before its first dialog. The image with the console was 18.1 MB (console: 0.7 MB). CI hardening is T101; small console size savings are listed in E11 as later work.
