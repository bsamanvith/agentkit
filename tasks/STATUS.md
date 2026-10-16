# Plan status

> Maintained by the planner ([docs/WORKFLOW.md](../docs/WORKFLOW.md)). Updated 2026-10-12 at `main` `e80b204` plus planner commits, CI green. Task files are the source of truth for each task; this page is the overview.

## Milestones

| Milestone | Epic | State |
|---|---|---|
| M0 Tooling: `make check`, lint, architecture test, CI | [E01](E01-foundations.md) | Done |
| M1 `pkg/tool` | [E03](E03-tool-core-and-mcp.md) | Done |
| M2 Toolsets from config; one MCP server, one path per toolset | [E03](E03-tool-core-and-mcp.md) | Done |
| M3 `llm`, Anthropic adapter, harness, `flow`, `agentstep`, `agentkit run` | [E04](E04-llm-harness-flow.md) | Done |
| M4 Store, runner, compaction | [E05](E05-store-runner.md) | In progress: `pkg/store` (interfaces, memory store, suites, run lifecycle), the SQLite store (T029), the runner (T030) and the API on the runner (T037: store chosen by `AGENTKIT_STORE`, runs outlive `serve`) done; next compaction (T031). Postgres store (T088) and turn batching (T098) done; run start times and priority are T099 |
| M5 Agent API, `agentkit serve`, image, Compose, smoke tests | [E09](E09-agent-api.md), [E01](E01-foundations.md) | In progress: `agentkit serve`, agent API v0 and the image done; **deployable** |
| M6 Remote tools over ConnectRPC | E06 (to plan) | Planned |
| Credentials, auth, tenancy at the edge | [E07](E07-auth-credentials.md) | **In progress**: T039 done (`pkg/auth`, `f08e82e`); T040, T041 and T042 ready. Needed before any public deployment: `/mcp/` has no auth until T040 |
| First connectors: Tavily, Exa, Parallel (search, extract) | [E10](E10-search-connectors.md) | **Tavily done** (live end to end with real Claude passed); Exa and Parallel on `main`, in review until keys exist; no auth work needed (Tavily keyless or keyed; Exa and Parallel keyed from env) |
| Clients: admin console, CLI and headless, and the APIs behind them | [E11](E11-clients.md) | **In progress**: T053 (`pkg/agentclient`), T054 (`agentkit chat`, `agentkit ask --json`), T062 (agents, conversations, catalog API) T070 (console design, owner-approved), T078 (web workspace, embedded console shell) and T092 (`POST /v1/conversations`) done; T055 (console chat), T063 (agents over MCP), T061 (login), T055/T064 (console) next. UI plan: [docs/UI.md](../docs/UI.md) with a clickable mockup |
| Skills and memory (context store; search by keyword or embeddings, opt-in) | [E12](E12-skills-memory.md) | **Planned** (T057–T060); T057 and T058 can start now |
| Agent protocols: A2UI (agent-driven UI), A2A (agent to agent), AG-UI, MCP Apps | [E13](E13-agent-protocols.md) | **Planned** (T065–T069); after the console basics and the agents API |
| Tasks (background agents) and agent-to-agent messaging | [E14](E14-background-agents-messaging.md) | **Planned** (T071–T074); needs the stream API (T090, E18); the runner (T030) and SQLite store (T029) are done |
| Agent definitions as versioned data (agents, skills, toolsets) | [E15](E15-agent-definitions.md) | **Planned** (T075; T076, T077 to plan); T029 and T062 are done, so T075 can start |
| Runtimes and jobs: temporary environments, exported harness images, results outside chat | [E16](E16-runtimes-jobs.md) | **Shaped**, tasks to plan after the console's first version |
| Tool safety and control: tool kinds and risk, policies, interrupts and approvals, deferred loading, steering | [E17](E17-tool-safety-control.md) | **In progress**: T082 (tool metadata) done; T083–T086 next |
| Streaming and the server backend: SSE + HTTP commands (WebSocket later), Postgres + Redis | [E18](E18-streaming-backend.md) | **In progress**: T088 (Postgres store, event log, `agentkit migrate`), T089 (`pkg/bus`, Redis Streams) and T098 (runner turn batching) done; T090 (stream API) and T093 (row-level security) ready |
| External MCP servers: add any remote MCP server by URL, MCP OAuth, tool pinning | [E19](E19-external-mcp-servers.md) | **Planned** (T094–T097); T094 can start now |
| Google connectors: Gmail, Calendar, Drive as toolsets, one package each, shared `common` | [E21](E21-google-connectors.md) | **In progress** (T108–T111): T108 (`connectors/google/common`) in review; T109 (Gmail) building; T110 (Calendar) and T111 (Drive) start from T108 |
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

T001 tooling · T002 testkit · T003 `agentkit serve` · T005 image and `agentkit healthcheck` · T017 `pkg/tool` · T018 MCP server · T019 `pkg/clock` · T020 `pkg/llm` · T021 Anthropic adapter · T022 harness · T023 `flow` · T024 `agentkit run`, `agentstep` · T026 `pkg/toolset` · T027 `pkg/tenancy` · T028 `pkg/store` · T038 `store.Admin` · T046 Tavily connector · T049 connector test kit · T051 cassette OpenAPI validation · T052 kit hardening · T053 `pkg/agentclient` · T032 flow step-tree check · T036 agent API v0 · T029 SQLite store · T054 `agentkit chat` and `ask` · T062 agents, conversations and catalog API · T070 console UX design (owner-approved) · T030 `pkg/runner` · T078 web workspace · T092 `POST /v1/conversations` · T082 tool metadata · T088 Postgres store and event log · T089 `pkg/bus` · T098 runner turn batching · T037 agent API v1 on the runner

## Running or waiting

| Task | State |
|---|---|
| [T013](T013-skill-write-tests.md) `write-tests` dry runs | The `pkg/flow` dry run found a real bug (a rejected run could be resumed into `failed`); **fixed on `main`** by the planner (`a645012`) with a deterministic regression test. The dry run's flow tests (invariants after every run, seeded simulation, `FuzzSimulatedFlow`) are on `main` as `5a13386`. The T018 dry run stopped unfinished. |

## Next (ready to start)

| Task | What | Why now |
|---|---|---|
| [T039](T039-auth-principals-middleware.md) | `pkg/auth`: principals, API keys, edge middleware | **Done** (`f08e82e`, CI green): T040, T041 and T061 can start; read its "For T040", "For T041" and "For T061" Notes first |
| [T040](T040-serve-auth-wiring.md), [T041](T041-auth-jwt-oidc.md) | `serve` behind auth; JWT/OIDC | **Ready** (T039 done) |
| [T063](T063-agents-over-mcp.md) | Agents over MCP for Claude Code | **Ready** (T062 done) |
| [T055](T055-web-client.md), [T061](T061-local-users-login.md), [T064](T064-console-admin-pages.md) | Console shell and chat; local users and login; admin pages | **T055 ready** (T078, T062, T070 done; read T055's "From T078's handoff"); T061 ready; T064 after T055 |
| [T057](T057-skills.md), [T058](T058-memory-context-store.md) | Skills (Agent Skills format, progressive disclosure); memory context store (Claude memory-tool shape) | Ready; new packages, parallel with everything |
| [T056](T056-test-speed.md) | `make test` back under 30 s (52 s now) | Ready; small |
| [T050](T050-connector-tool-evals.md) | Tool-use evals: real Claude with faked connectors | **Later** (owner): after the API is deployed and the clients exist; runs on `agentkit ask --json` |
| [T047](T047-connector-exa.md) | `connectors/exa`: search and extract | **Review**: on `main`, on the kit (`2f7b951`, `049a7cc`); waits for `EXA_API_KEY` (live test, re-recording synthetic cassettes) |
| [T048](T048-connector-parallel.md) | `connectors/parallel`: search and extract | **Review**: on `main`, on the kit (`baf4248`); waits for `PARALLEL_API_KEY` (live test, re-recording synthetic cassettes) |
| [T042](T042-credentials-core.md) | `pkg/credentials`: secrets, sealer, vault, memory backend | Starts E07's credentials track; new package only, parallel with everything |
| [T100](T100-interactive-chat-runs.md) | Runner: interactive chat runs stop with their user (stop, closed tab), show `interrupted`, are never recovered in the background | **Start now** (T030, T088, T098 done): T037 and T099 wait on it |
| [T090](T090-stream-api.md) | Stream API across replicas: replay from Postgres, tail Redis, commands to the lease holder | **Mostly done** (bsamanvith/agentkit#13, merged 2026-10-15): run streams and stop/cancel across replicas, local/dev image variants. Left: steering (needs T086), `GET /v1/events` (needs T040) |
| [T103](T103-shared-run-watch.md) | One watch per run per replica, shared by its viewers: one Redis connection and one held-check per run, not per viewer | **Review**: bsamanvith/agentkit#18. The takeover criterion is tested in process, not in `TestE2EServeReplicas` (see its Notes) |
| [T093](T093-postgres-rls.md) | Postgres row-level security by tenant | **Ready** (T088 done); small |
| [T099](T099-run-queue-order.md) | Runs: start time, lane and priority, `ClaimNext` | After T100 (migration 0006; skips interactive runs) |
| [T083](T083-tool-policy.md) | Tool policy: presets, rules, allow/flag/approve/deny | **Ready** (T082 done); T084 follows with T030 done |
| [T031](T031-compaction.md) | Compaction | **Ready** (T030 done) |
| [T087](T087-console-ux-design-2.md) | Console UX design part 2: skills, agent builder, tool policies, interrupts and approvals, tasks with steering; mockup v4 | **Ready** (owner approved v3) |
| [T108](T108-google-common.md) | `connectors/google/common`: the client, token sources, settings, paging and text limits the Google connectors share | **Review** (draft PR with Gmail); T110 and T111 build on it |
| [T109](T109-connector-gmail.md), [T110](T110-connector-google-calendar.md), [T111](T111-connector-google-drive.md) | Gmail, Google Calendar and Google Drive connectors | T109 in progress; T110 and T111 **ready** once T108 is on `main` or its branch |
| [T094](T094-mcp-client-toolset.md) | `pkg/mcp/client` and the `mcp` toolset type: use external MCP servers' tools, pin fingerprints | **Ready** (E19); new package, tests against our own MCP server |
| [T101](T101-ci-hardening.md) | CI hardening: actions pinned by SHA, no persisted credentials, workflow lint | **Ready**; small, from T078's review |
| [T102](T102-component-images.md) | An image per component (agent, MCP, console), a migrate image, and the all-in-one image; a budget each | After T037 (rewrites `serve.go` and adds `store.go`); owner asked 2026-10-14 |
| [T006](T006-compose-and-dev-script.md) | Compose topologies and `scripts/dev.sh` | Image exists; repeatable local runs |
| [T033](T033-harness-clock.md) | Harness on `pkg/clock` | Small, independent |
| [T035](T035-rerecord-anthropic-fixtures.md) | Re-record Anthropic fixtures from the real API | A key is available |
| [T010](T010-skills-layout.md) | `AGENTS.md`, `CLAUDE.md`, skills README, lint | Small, independent |
| [T034](T034-nightly-ci.md) | Nightly benchstat report (non-gating, no secrets) | Small; starts the perf history |

## Blocked on other tasks

| Task | Waits for |
|---|---|
| [T043](T043-credentials-backends.md) credential backends, [T044](T044-credentials-in-tools.md) credentials in tools | T042 |
| [T091](T091-websocket-binding.md) WebSocket binding | T090 |
| [T095](T095-mcp-oauth-client.md) MCP OAuth for external servers | **In progress**: the core (`pkg/mcp/oauth`: discovery, CIMD/DCR, PKCE + resource, refresh, provider profiles) is built and tested on fakes. Wiring needs T094 (client), T042 (vault) and T096 (API, callback page) |
| [T096](T096-mcp-servers-api-console.md) Add MCP servers at runtime: API, console, SSRF guard | T094, T062 (done), T040, T082; screens from T087 |
| [T097](T097-mcp-registry-search.md) MCP Registry search | T096 |
| [T045](T045-connect-accounts.md) Connecting accounts (OAuth2 + PKCE) | T040, T043, T044 |
| [T004](T004-fakellm.md) `fakellm`, [T007](T007-e2e-smoke-suite.md) smoke suite, [T008](T008-ci-pipeline.md) full CI | T006 |

## Waiting on the owner

1. **Managed Postgres** for deployments (RDS, Cloud SQL, Neon, ...): needed before the first deploy with more than one replica (E18).
1. **First embedding provider** for semantic memory (T060): Voyage AI, OpenAI, or a local model through the OpenAI-compatible endpoint.
2. **`EXA_API_KEY` and `PARALLEL_API_KEY` in Claude sessions** (like `ANTHROPIC_KEY`) for T047's and T048's live tests; optional `TAVILY_API_KEY` (Tavily's live tests run keyless without it).
3. **Delete the `review/t022-base` branch**, and the merged `claude/*` session branches including `claude/t013-flow-tests` and `claude/festive-napier-f0b111` (all their work is on `main`).

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
