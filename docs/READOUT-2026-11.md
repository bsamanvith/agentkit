# agentkit: advisor readout, November 2026

> Written 2026-11-10 at `main` `f780d75` (PR #141). It covers everything
> built since the repository started (mid-October 2026): the agentkit
> platform and the first product on it, the farm app. Sources: the
> merged PRs, [PLAN.md](PLAN.md), [PRODUCT.md](PRODUCT.md),
> [COMPONENTS.md](COMPONENTS.md), [CONNECTORS.md](CONNECTORS.md),
> [UI.md](UI.md), [SIZES.md](SIZES.md), [tasks/](../tasks/README.md) and
> the farm app's [FEATURES.md](../apps/farm/docs/FEATURES.md).

## 1. Summary

- **What it is.** agentkit is a Go kit for running Claude agents: one
  small binary that is a library, a server, an MCP tool server and a web
  console. On top of it is the first product, a **farm app for Indian
  farmers** (plots, crop planning, weather alerts, prices and an
  assistant that speaks the farmer's language).
- **How far along.** About 140 merged PRs in four weeks. 136 task files
  (116 done, 18 in review, 2 to do). The platform is feature-complete
  for a first deployment. The farm app has 12 live features and a ranked
  list of what comes next.
- **Proven where.** The full test gate (`make check`, unit tests under
  `-race`) runs on every change. The split deployment passes its smoke
  suite on the real model on both Docker Compose and Kubernetes, in
  cloud sessions and on the owner's Docker Desktop. The tool-use evals
  pass 21 of 21 scenarios. The farm assistant was tested in Telugu, Hindi
  and Hinglish.
- **Main open items.** Farmer accounts (the farm app has no sign-in
  yet). A live mandi-price test needs a data.gov.in key from an Indian
  connection. MCP servers added at run time don't work on the split
  runner. The sandbox and VM runner pools are paused. An agronomist needs
  to review the crop rule values.

## 2. Goals

From [PLAN.md](PLAN.md) and the owner's interview of 2026-10-12
([PRODUCT.md](PRODUCT.md)):

1. **One core for every agent product.** The agent loop, tools, MCP,
   skills, memory, storage, runner and API are written once. A product
   (farmer's assistant, finance tools, an ephemeral VM harness) is mostly
   configuration: its own tools, skills, prompt and a consumer app.
2. **Go, small and fast.** Distroless images under a tracked size budget.
   Every size change is measured and logged with a reason
   ([SIZES.md](SIZES.md)).
3. **Modular by interface.** Store, bus, model, turn loop, run
   orchestration, tools and transport are each an interface with a
   conformance suite, and each implementation is picked in one place.
   That lets the same code run in one process or split across
   containers.
4. **Safe with real accounts.** Multi-tenant from the start, credentials
   sealed per user, tool policies and approvals, and no open MCP off
   loopback.
5. **Tested by outcome.** Unit, conformance, replay, live-model and
   eval tiers ([ENGINEERING.md](ENGINEERING.md) §2). Owner rule: every
   check runs in a cloud session, with no manual steps for the owner.
6. **Product goal (farm).** Help a small farmer in India through the
   year: land, plan, grow, sell, money. Use free data sources first, with
   numbers from tools and not the model.

## 3. Products built

### 3.1 agentkit platform

| Area | What exists | Where |
|---|---|---|
| **Agent loop** | Our own harness: model, then tools, then model. Streaming, bounded parallel tool calls, turn and token budgets, hooks, append-only transcripts that resume after a whole turn, and compaction of long conversations. `flow` adds steps and combinators (sequence, parallel, loop, route, approve) with checkpoints. | `pkg/harness`, `pkg/flow`, `pkg/agentstep` |
| **Models** | Anthropic (thinking, caching, effort) and Gemini (`gemini/…`, free tier). An agent allows a set of models, each with its own prompt, effort and tool-description overrides, and each run picks one. The OpenAI-compatible adapter is written and waits on a key for its live test. | `pkg/llm`, `pkg/llm/gemini`, `pkg/llm/modelset` |
| **Tools and MCP** | Typed tools with JSON Schema. Toolsets come from config, and one MCP server serves each toolset at its own path (`/mcp/<name>`). The MCP client lets outside servers be added by URL at run time, with MCP OAuth, tools pinned by hash, an admin review of changed tools, and an SSRF guard. Tools can be deferred and loaded through `tool_search`. | `pkg/tool`, `pkg/toolset`, `pkg/mcp/*`, `pkg/netguard` |
| **Connectors** | Search: Tavily, Exa, Parallel (each user brings their own key). Google: Gmail, Calendar, Drive, Sheets. CRM: HubSpot, Salesforce (per-user OAuth). Knowledge bases (`knowledge` toolset: chunked docs, keyword or semantic search, citations). Core tools: math, files, `ask_user`. | `connectors/*`, `pkg/knowledge`, `pkg/coretools` |
| **Skills and memory** | Agent Skills format with progressive disclosure, so skills load their tools when used. Memory is plain readable text with size caps (16 KB a file, 1 MB a scope), on SQLite or Postgres. It is loaded into each message up to 4 KB, and past that the agent greps it. Memory is visible and deletable in the console. | `pkg/skill`, `pkg/memory` |
| **Runs, tasks, messaging** | Conversations and runs with leases, crash recovery, retries and an ordered queue. Background tasks with a stored, resumable event stream that can be steered. Schedules, webhooks and triggers. Agent-to-agent mailboxes with loop limits. Supervised tasks that wait for a person. | `pkg/runner`, `pkg/agentapi`, `pkg/trigger`, `pkg/webhook`, `pkg/agentmsg` |
| **Tool safety** | Every tool has a kind (read, write, delete, send, execute) and a risk level. Policies allow, flag, require approval for, or deny a call, and the environment can only narrow them. Interaction modes are interactive, supervised and autonomous. | `pkg/policy` |
| **Agent definitions** | Agents are versioned data: draft, publish, roll back, diff. They can be built in the console. Stored agents also run on the split runner. | `pkg/registry`, T075, T077, T132 |
| **APIs and protocols** | Public HTTP+JSON API with SSE (`/v1`), plus a WebSocket binding. Every agent is also an A2A v1.0 agent and an AG-UI endpoint. Agents can stream A2UI surfaces (forms, choices). Internal links between components use `pkg/rpc`. | `pkg/agentapi`, `pkg/a2a`, `pkg/agui`, `pkg/a2ui`, `pkg/rpc` |
| **Auth and secrets** | API keys with scopes and per-key toolsets. OIDC/JWT, password login, and a Cloudflare Access gate with a require-auth mode. A credential vault (AES-GCM bound to tenant, user and slot) with OAuth connect flows. Postgres row-level security per tenant. | `pkg/auth`, `pkg/credentials`, `pkg/tenancy` |
| **Storage and streaming** | SQLite (local) or Postgres (server), with migrations. Redis Streams for live fan-out across replicas, and Postgres as the durable event log. | `pkg/store`, `pkg/bus`, `pkg/stream` |
| **Console** | React, TypeScript, shadcn/Radix, embedded gzipped in the binary. Sign-in, chat, chats list, agents and the agent builder with versions, toolsets, Connectors, tasks, threads and triggers, and memory. Works on phones. | `web/apps/console`, `web/packages/*` |
| **CLI** | `agentkit run`, `chat`, `ask --json` (headless, for tests and evals), `toolsets`, `mcp-tools`, `migrate`. | `cmd/agentkit` |

### 3.2 Components and deployment

`agentkit serve` runs the components named in `AGENTKIT_COMPONENTS`:
`agent` (API), `web` (console), `runner`, `toolsets`, `mcp-server`,
`a2a`, `agui`. Every link between two components is an interface with
an in-process implementation and a remote one, so one process or a
split deployment is a config change ([COMPONENTS.md](COMPONENTS.md)).

| Way to run | Command | State |
|---|---|---|
| One process, real model | `scripts/try-agent.sh` | Works |
| Split deployment as processes, fake model | `make smoke`, `make integration` | In CI, seconds |
| Split deployment against Postgres and Redis | `make integration STORE=postgres` | CI's integration tier |
| Docker Compose | `make compose-up` (console on :30081), `make cloud-compose-up` | Smoke passes, real model |
| Kubernetes | `make k8s-up` (Docker Desktop, console on :30080), `make cloud-k8s-up` (k3s in a cloud session) | Smoke 8/8 on the real model, in the cloud and on the owner's laptop |
| Farm app on Kubernetes | `scripts/k8s-local.sh farm` (port forward to :30090) | Deployed and tested as a farmer, 2026-11-09 |

**Images** (distroless, size budgets enforced in CI): the all-in-one
`agentkit` (38 MB), `local` (SQLite, 32 MB), `dev` (34 MB), `api`
(24 MB), `runner` (20 MB), `toolsets` (18 MB), `fakellm` (12 MB), and
`farm` (24.4 of 26 MB). Every budget change has a row in
[SIZES.md](SIZES.md) with the measured size, the reason and what gets
cut first.

### 3.3 Farm app

A second app in the same repository: Go in `apps/farm`, and React with
Vite and Leaflet in `web/apps/farm`, embedded in the `farm` binary. It is
built for India: acres, guntas and cents; Telugu, Hindi, Kannada and
Marathi; kharif and rabi.

| # | Live feature | What the farmer gets |
|---|---|---|
| 1 | My plots | Draw on a satellite map, walk the corners with GPS, or paste coordinates. The app checks the true area against the size on the papers, slivers, GPS jumps, overlaps and the village. Search for a village to find the plot. |
| 2 | The land and its surroundings | Slope and runoff, which way each side faces, and nearby roads, canals, wells and power lines with distances. An animated land view. |
| 3 | Farm layout | A suggested gate, path, shed, pond and border trees, each with a reason, and a check of any changes. |
| 4 | What to plant and when | 39 crops scored 0 to 100 for this plot's soil, water, climate and last crop, with sowing windows, reasons, risks and the water gap. Includes crops from similar climates abroad. |
| 5 | Sun and seasons | Sunlight, day length, noon sun and slope effect by month, and a crop calendar. |
| 6 | Season plan | Single or mixed crops cut into strips to scale, with spacing, plant counts, seed, and a plan check that warns before a season is lost. |
| 7 | Plot groups and plant distribution | Blocks, border bands and alley cropping, with shade, edge, water, neighbour and wind effects. |
| 8 | Farm assistant | Answers in the farmer's language and script. Every number comes from a tool. It has 11 skills and a searchable farming handbook. |
| 9 | Web research | Exa or Parallel with the farmer's own key, kept in the vault. |
| 10 | Farm tools over MCP | Partners' agents (an FPO, an agronomist) can use the same tools. |
| 11 | Weather alerts and weekly crop work | A forecast per plot, and alerts only when there is something to do (heavy rain, heat, dry spell, disease weather). An automation engine runs schedules, events and weather triggers. |
| 12 | Crop prices | Mandi prices by district and state, a year of history, minimum support price, and a sell-month estimate as a range with a confidence (arithmetic, not a model guess). |

Data providers are pluggable with fallbacks, and free ones come first:
Nominatim, NASA POWER, OpenStreetMap (Overpass, then the OSM API),
Open-Meteo then Open Topo Data for elevation, Open-Meteo then MET Norway
for weather, and data.gov.in then price files for prices. The *Cost*
column in [FEATURES.md](../apps/farm/docs/FEATURES.md#data-providers)
flags what is not free, such as Open-Meteo's non-commercial terms.

## 4. Chat capabilities

**Console chat** ([UI.md](UI.md)):

- **Streaming answers** over SSE (or WebSocket). Stop interrupts the
  run. Resume continues it. A closed tab stops a chat run after a 10 s
  grace, while background tasks keep running and are recovered.
- **Tool calls** show as quiet one-line steps with details on demand,
  including what the policy decided (allowed, flagged, waiting, denied).
- **Agent picker and model picker.** The model is kept per conversation,
  and each answer names the model in its usage line. Switching provider
  mid-conversation starts a new chat.
- **Tools menu.** A chat can narrow the agent's toolsets and choose
  Direct (all tools in context) or Search (held behind `tool_search`).
- **Questions and approvals.** The agent can ask the user (`ask_user`,
  with choices), and the policy can hold a risky call for approval with
  its exact input. Answers come back on the same run.
- **A2UI surfaces.** Agents show small forms and choice sets inline,
  rendered with the console's own components.
- **Connections in chat.** A missing key or account shows "Connect Exa"
  before sending. A tool that fails because it isn't connected shows a
  card to connect it, and the run carries on.
- **Memory, skills, knowledge.** Memory carries across conversations.
  Skills load their tools when used. Knowledge-base answers cite their
  source.
- **Background work.** Tasks, agent threads and triggers each have their
  own views.

**Other ways to chat:** the CLI (`agentkit chat`, `agentkit ask --json`),
A2A (other agents call ours), AG-UI (CopilotKit and similar frontends),
and MCP (Claude Code uses our toolsets and agents).

**Farm assistant:** a chat drawer on every page that answers in the
farmer's language. It has the farm, planning, map, weather and price
tools, and skills for planning a season, checking a plan, surveying land,
shaping the farm, distributing plants, researching crops and products,
the weather check, drawing a crop plan, managing a crop, and sell or
hold. It also has handbook search.

## 5. Architecture in one picture

```
            browser / CLI / A2A / AG-UI / MCP clients
                              │
                 ┌────────────▼────────────┐
                 │  agent API (/v1, SSE,   │── Postgres (truth, event log, RLS)
                 │  WS) + console + auth   │── Redis Streams (live fan-out)
                 └──────┬───────────┬──────┘
             pkg/rpc    │           │   pkg/rpc (credentials, memory)
                 ┌──────▼──────┐ ┌──▼────────────┐
                 │   runner    │ │   toolsets    │── connectors (Google, CRM,
                 │ harness per │─▶ (and /mcp/*)  │   search, knowledge, MCP)
                 │ agent+model │ └───────────────┘
                 └──────┬──────┘
                        ▼
                Claude / Gemini
```

The same binary runs this as one process (SQLite, in-process bus) or as
separate containers. The farm app is its own binary built from the same
packages (harness, tools, MCP server, credentials, knowledge).

**Key decisions**, recorded in [tasks/STATUS.md](../tasks/STATUS.md):

- We wrote our own harness and `flow` rather than adopting ADK, Genkit
  or Eino. Other frameworks integrate through MCP.
- Streaming is SSE down and HTTP commands up. Postgres is the source of
  truth and Redis does the fan-out. Text deltas are live-only.
- Agents are versioned data, not only config files.
- Connector outputs are the same across providers. Per-user keys, with
  no shared search keys.
- One way to plug in a second implementation: it passes the conformance
  suite and gets one row in the picker. No scattered switches, no
  `init()` registration.
- The UI is React with TypeScript and shadcn/Radix, embedded in the
  binary.

## 6. Testing and quality

| Tier | What | Status |
|---|---|---|
| Gate | `make check`: tidy, format, golangci-lint, dependency rules (`internal/archtest`, `make deps-check`), unit tests under `-race` and shuffle | Every PR, and in CI |
| Conformance | Suites per seam (store, bus, model, tools, transport) that every implementation passes | In `make check` |
| Integration | The agent API suite plus smoke on SQLite, or on Postgres and Redis | CI integration job |
| Fuzz and golden | Coverage-guided fuzzing (its own CI job), golden files | CI and nightly |
| Live model | `make test-live` (skills, memory recall, MCP, A2UI, A2A, deferred tools, serve) | All pass on Claude (2026-10-24). Gemini adapter live-tested on the free tier |
| Evals | `make eval`: tool-use scenarios on real Claude (search connectors, knowledge, distractors, rate limits) | **21/21 scenarios, 3 runs each** (2026-10-25, `evals/results`) |
| Deployment smoke | `make cloud-k8s-smoke`, `make cloud-compose-smoke`, `make k8s-smoke` | 8/8 on the real model, on k3s, Compose and Docker Desktop |
| Farm | Unit and property tests per package. `make farm-eval` runs real-model scenarios (Claude 6/6). Browser walkthroughs with screenshots. A farmer test on Kubernetes on a phone-sized screen | Done for every feature. The farmer test found 9 issues, all fixed in #141 |
| Security | `make vulncheck` (govulncheck on code, modules and the toolchain), secret scans on cassettes | CI and nightly |

About 540 Go test files across 150 packages. Connectors are tested on
cassettes, with a conformance kit and drift checks. Google, HubSpot and
Salesforce cassettes are synthetic until real test accounts exist.

**Known test debt:** T056. "Unit tests under 30 s" no longer fits:
`pkg/store/sqlite` takes about 90 s under `-race`. A new budget or a
split tier is needed. CI has also failed occasionally on module
downloads and on one Postgres test; both passed on a re-run.

## 7. How the work is run

- **Tasks and epics** in `tasks/` (25 epics). Each task has acceptance
  criteria that are each one testable behavior.
- **Repo skills** in `.agents/skills`: plan, design-module, write-tests,
  write-connector, implement-task, review, local-env and farm-features.
  They take an idea to a merged PR the same way each time.
- **Cloud sessions** do everything, including k3s and Compose
  deployments, live-model tests and evals, with no manual steps for the
  owner. The owner granted standing approval to self-review and merge.
- **Size discipline.** Every image change of 100 KB or more is measured
  and logged ([SIZES.md](SIZES.md)). The farm web client has its own
  first-load budget (161 KB).

## 8. Known gaps and risks

| Gap | Why it matters | What unblocks it |
|---|---|---|
| **Farm app has no sign-in** | Every plot sits in one workspace, so alerts have no one to go to. On Kubernetes it is reachable only through a local port forward. | Feature 13 (Google or password sign-in first, phone OTP later). Top of the farm list |
| **Live mandi prices not tested** | data.gov.in and Agmarknet refuse connections from outside India, both from the cloud and from the device sandbox. Only MSP shows on the cluster. | A free data.gov.in key, used from an Indian connection. The provider code and the file fallback are done |
| **MCP servers added at run time don't work on the split runner** | In a split deployment, an agent with `runtime_toolsets` fails at startup ("cannot use toolsets added at run time yet"). It must run in the API's process. | Extend the runner, as stored agents were in T132 |
| **Runner pools paused** | Only the shared runner exists. The gVisor sandbox (T106) and microVM (T107) pools are paused by the owner. Untrusted code and ephemeral jobs need them. | The owner's call, together with E16 (runtimes and jobs) |
| **Crop rule values are starter values** | Recommendations, care advice and thresholds need an agronomist's sign-off before farmers rely on them. | An agronomist's review, then a pilot with real farmers in one district |
| **Free-tier and non-commercial data** | Open-Meteo is free for non-commercial use only. Gemini's free tier has daily limits. SoilGrids and Overpass were down from the cloud. | MET Norway and Open Topo Data fallbacks exist. Paid plans or self-hosting before commercial use |
| **Waits on outside accounts** | Drive cassettes (a Google test account), the OpenAI-compatible live test (a key), semantic memory (an embedding provider) | The accounts and keys named |
| **Unbuilt platform pieces** | Skills store and editor (T076), runtimes and jobs (E16), org-level connections, a user-facing memory view | Planning |
| **Housekeeping** | `tasks/STATUS.md` was last refreshed 2026-10-24. Several merged tasks (T130, T132, T133 to T140) still read `review`. T006 and T113 have now run on Docker Desktop. | One planner pass |
| **Farm UX leftovers** | Closing the assistant loses the chat, the phone's Back leaves the app, automations ask for a cron schedule, research asks the farmer to "paste your API key", bigha isn't offered, and the assistant lists tool names | Small fixes ([farmer test](#6-testing-and-quality)) |

## 9. Next steps

**Farm app** (ranked in [FEATURES.md](../apps/farm/docs/FEATURES.md#what-is-most-needed-next)):

1. **Farmer accounts and sign-in (13).** Unlocks alerts to a person,
   the ledger and the FPO view, and lets the farm app go online beyond
   a port forward.
2. **Alerts where farmers are (14).** Free web push and a Telegram bot
   first, WhatsApp and SMS later (paid).
3. **Crop doctor (15).** Diagnose from a leaf photo, using Gemini's free
   tier for images. Can start now, in parallel with 13.
4. **Ledger by voice (16)**, then **offline and voice (17)**, then
   **Soil Health Card from a photo (19)**.
5. **Not features:** an agronomist's review and a pilot in one district.

**Platform:**

1. Runtime MCP toolsets on the split runner, so the split deployment
   matches one process.
2. Cloud deploy: managed Postgres (RDS, Cloud SQL or Neon) and a public
   ingress behind the require-auth gate.
3. The skills store and console editor (T076), then runtimes and jobs
   (E16) with the paused runner pools when untrusted code arrives.
4. A planner pass: refresh STATUS.md, close the merged tasks, settle
   T056's test-time budget.

## 10. Questions for the advisor

1. **Farm go-to-market:** pilot with individual farmers, or through an
   FPO (one partner, many plots, an assisted rollout)? This changes
   whether accounts or the FPO view comes first.
2. **Agronomy review:** who could review the crop and care rules (an
   agricultural university, KVK, or an independent agronomist)?
3. **Commercial data:** when the app earns money, Open-Meteo needs a
   paid plan. Budget it, self-host, or move to MET Norway and IMD?
4. **Platform focus:** keep agentkit a general kit (finance tools, an
   ephemeral VM harness next), or narrow it to what the farm app needs
   for the next quarter?
