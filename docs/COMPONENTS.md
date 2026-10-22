# Components and how they are deployed

agentkit is one binary, `cmd/agentkit`, and `agentkit serve` runs the
components named in `AGENTKIT_COMPONENTS`. They can all run in one process,
or each in a container of its own, without code changes: every link between
two components is a Go interface with an in-process implementation and one
that goes over the wire (T102).

## The components

| Component | `AGENTKIT_COMPONENTS` | Serves | What it is |
|---|---|---|---|
| Agent API | `agent` | `/v1/` | Conversations and runs: the store, the bus between replicas, the run log, leases and recovery (`pkg/agentapi`, `pkg/runner`) |
| Console | `web` | `/` | The web UI, which calls `/v1` on its own origin (`pkg/web`) |
| Runner | `runner` | `/rpc/runner/` | The agents' turn loops: a harness per agent, its model and its tools (`pkg/harness`, `pkg/runner/runnerrpc`) |
| Toolsets | `toolsets` | `/rpc/toolsets/` | The connectors of `mcp.toolsets`, to runners and agent APIs (`pkg/tool/toolrpc`) |
| MCP server | `mcp-server` | `/mcp/<name>` | The same toolsets over MCP, for Claude Code and other MCP clients (`pkg/mcp/server`) |
| A2A | `a2a` (with `agent`) | `/a2a/<agent>`, `/.well-known/agent-card.json` | Each agent as an A2A v1.0 agent: its Agent Card, the message and task methods over its runs (task = run, contextId = conversation), JSON-RPC and HTTP+JSON. It calls the agent API in process with each request's own credential, so `/v1`'s keys, tokens, tenants, scopes and agent visibility apply unchanged (`pkg/a2a`) |
| AG-UI | `agui` (with `agent`) | `/agui/<agent>` | Each agent to AG-UI 1.0 frontends (CopilotKit, `@ag-ui/client`): a `RunAgentInput` POST answered with the run's AG-UI events over SSE (thread = conversation), tool calls, A2UI surfaces as `a2ui-surface` activity snapshots, surface actions, and questions and approvals as interrupts resumed on the same run. It calls the agent API in process with each request's own credential, as `a2a` does (`pkg/agui`) |

How they connect:

```
browser ──/v1──▶ agent ──runner.Agent──▶ runner ──tool.Registry──▶ toolsets ◀──MCP── MCP clients
                  │                                                    ▲
                  └────────────── tool.Registry (GET /v1/toolsets) ────┘
```

| Link | Interface | In process | Over the wire |
|---|---|---|---|
| agent → runner | `runner.Agent` | the agent's `*harness.Harness` | `runnerrpc.Client.Agent(name)`, with `AGENTKIT_RUNNER_URL` |
| runner, agent → toolsets | `tool.Registry` | the registries `toolset.Build` returns | `toolrpc.Client.Registry(name)`, with `AGENTKIT_TOOLSETS_URL` |

The agent API keeps every run where its store is: only the turn loop moves to
the runner, which holds no state. A run the API sends to a runner streams the
same events and fails with the same errors as a local one (the run's partial
work, retryable model errors, refusals), so recovery, retries and the event
stream do not change. The runner is the one component that needs the model's
key. A runner pool (E20, T104) is the same seam: a pool of remote runners is
a `pool.Pool` whose agents are `runnerrpc` clients.

## Environment

| Variable | Read by | Meaning |
|---|---|---|
| `AGENTKIT_RUNNER_URL` | `agent` | Run the agents on the runner at this URL (`http://runner:8080`) instead of in process. The API then builds no model. |
| `AGENTKIT_TOOLSETS_URL` | `agent`, `runner`, `mcp-server` | Take the toolsets from the toolsets component at this URL instead of building them. Every toolset the config names must be served there. |
| `AGENTKIT_RPC_TOKEN` | all | The bearer token between components, 16 bytes or more. The runner and toolsets components require it on an address that is not loopback, and their callers send it. |
| `AGENTKIT_API_TOKEN` | `agent`, `mcp-server` | The bearer token clients send to `/v1/` and `/mcp/`. Required on an address that is not loopback, and checked whenever it is set. |
| `AGENTKIT_A2A_URL` | `a2a` | The origin the Agent Cards name (`https://agents.example.com`), when a proxy in front changes it. Unset, a card names the scheme and host of the request that fetched it. |
| `AGENTKIT_PUBLIC_URL` | `agent` | The server's public origin (`https://agents.example.com`), for MCP OAuth on toolsets added at run time (T095): authorization servers redirect to `/oauth/callback` there, and over https `/oauth/client-metadata.json` there is agentkit's client ID. Unset, those toolsets can't use `auth: oauth`. Needs the credential vault. |
| `AGENTKIT_DEPENDENCY_WAIT` | `agent`, `runner` | How long startup waits for the toolsets component to answer (default `60s`): pods start in any order. |

Every container gets the same config file: the agents and `mcp.toolsets`.
The agent API reads it for agent names and descriptions, the runner for
models and limits, and the toolsets component for the connectors and their
settings. Connector keys (`TAVILY_API_KEY`, ...) go to the toolsets
component only, the model's key (`ANTHROPIC_API_KEY`, or the variable an `openai` model's
`api_key_env` names) to the runner only, and
the store and bus URLs to the agent API only.

A component that serves `/rpc/` checks nothing but the token: keep it on the
cluster network.

`/readyz` of the agent API is degraded, not failed, when its runner cannot be
reached or does not run its agents: the API still serves stored
conversations, and runs fail until the runner is back.

## Images

| Image | `make` | Tags | Holds |
|---|---|---|---|
| `agentkit` | `image` | none | Everything: every component, every store and bus, the CLI |
| `agentkit-local` | `image-local` | `nopostgres noredis` | One machine: memory or SQLite |
| `agentkit-dev` | `image-dev` | `noagui nosqlite` | Deployed replicas: Postgres and Redis; no AG-UI server |
| `agentkit-api` | `image-api` | `nomodels noconnectors nomcp noagui nosqlite` | `agent` and `web`, memory or Postgres, Redis; no model SDK, connectors, MCP or AG-UI server (`agui` runs on the `agentkit` and `agentkit-local` images) |
| `agentkit-runner` | `image-runner` | `noconnectors nomcp noweb noa2a noagui nosqlite nopostgres noredis noagent` | `runner`: the model SDK; no agent API, store, bus, console, connectors, MCP, A2A or AG-UI server |
| `agentkit-toolsets` | `image-toolsets` | `nomodels noweb noa2a noagui nosqlite nopostgres noredis noagent` | `toolsets` and `mcp-server`: the connectors; no model SDK, agent API, store, bus, console, A2A or AG-UI server |

`make images-split` builds the last three. Their sizes and budgets are in
[SIZES.md](SIZES.md); `make deps-check` (part of `make check`) fails when an
image's binary links what its tags leave out. A component an image leaves
out fails at startup with a message naming the tag. Migrations run with the
image that has the store: `agentkit-api migrate` as a job, then serve with
`AGENTKIT_STORE_MIGRATE=off`. CI's image job runs the split images
(`scripts/smoke.sh --images`): each reports ready for its own components
only and serves its own surface, and `agentkit-api` migrates an empty
Postgres (a second run changes nothing, `scripts/migrate-check.sh`) and then
serves runs on it with migrations off. `make smoke` (memory) and
`make integration STORE=postgres` do the same with binaries built with each
image's tags.

### An image of your own

An image is the one binary with a set of build tags, and each tag leaves one
part out:

| Tag | Leaves out | Then |
|---|---|---|
| `nosqlite` | the SQLite store, and the SQLite memory and credential backends | `AGENTKIT_STORE=sqlite` and `backend: "sqlite"` fail at startup |
| `nopostgres` | the Postgres store, its credential backend and `migrate` | `AGENTKIT_STORE=postgres` and `credentials.backend: "postgres"` fail |
| `noredis` | the Redis bus | `AGENTKIT_BUS=redis` fails |
| `nomodels` | the model SDK | the `agent` component needs `AGENTKIT_RUNNER_URL`; no `runner` |
| `noconnectors` | every connector but the demo | the toolsets come from `AGENTKIT_TOOLSETS_URL` |
| `nomcp` | the MCP server | no `mcp-server` component |
| `noweb` | the console | no `web` component |
| `noa2a` | the A2A server | no `a2a` component |
| `noagui` | the AG-UI server | no `agui` component |
| `noagent` | the agent API: its store and bus, the agents' memory, the toolsets added at run time, `migrate` | no `agent` component, so no `a2a` or `agui` (they call the agent API in process); the `runner` component still runs agents, and an `agentkit` toolset calls an agent API at its `settings.url` |

A one-off build takes any mix, for example an API that runs its agents in
process, with Postgres only and its toolsets from a toolsets pod:

```sh
make image IMAGE=agentkit-api-runner TAGS="noconnectors nomcp nosqlite"
```

To make it a named variant that CI keeps honest:

1. Makefile: add the name to `VARIANTS`, its tags as `TAGS_<name>` and a
   budget as `IMAGE_BUDGET_MB_<name>`. `make image-<name>` and
   `make image-size-<name>` then exist.
2. `scripts/check-deps.sh`: say what it must not link in `forbidden()`
   (`make deps-check` refuses a variant it doesn't know).
3. docs/SIZES.md: a row with its measured size (AGENTS.md, Sizes).

A new part to leave out is a file with `//go:build !notag` and a `_none.go`
twin that returns `notBuilt` (`cmd/agentkit/backends.go` lists them). Store
and bus backends are such files in `pkg/store/storeopen` and
`pkg/bus/busopen`.

## Plugging in another implementation

Each seam is a small interface with a conformance suite; a new
implementation passes the suite and is picked by a spec or config value.

| Seam | Interface | Suite | Picked by |
|---|---|---|---|
| Store | `storeopen.Store` (one transactional store: conversations, runs, events, commands, checkpoints, the run index and runs' webhooks) | every `storetest.Run*` and `Simulate*` | `AGENTKIT_STORE` through `storeopen.Backends` (or `storeopen.WithBackends` in a program of your own) |
| Bus | `bus.Bus`: an atomic conditional append with replay, not a work queue | `bustest.Run` | `AGENTKIT_BUS` through `busopen.Backends` |
| Model | `llm.Model` | `llmtest` | `model.provider` through `modelProviders` (`cmd/agentkit/models.go`) |
| Turn loop | `runner.Agent` | runner tests | `AGENTKIT_RUNNER_URL`, or an `agentapi.Agent`'s `Runner` |
| Where a run's turn loop runs | `pool.Pool` (default: one `process` pool running the agent as built) | `pkg/runner/pool` tests | `agentapi.WithRouter` (a `pool.Router`: rules, then the default pool, never a pool weaker than `agentapi.Agent.Isolation`, each pool with its limit; E20), built from the serve config's `runners` section (`cmd/agentkit/runners.go`) |
| Run orchestration | `agentapi.Runs` (default `*runner.Runner`: in process, under leases) | agentapi tests | `agentapi.WithRuns` |
| Tools | `tool.Registry` | `toolsettest` | the config's toolsets (`connectors/`, write-connector skill) |
| Transport | `rpc.Conn` | rpc tests | `rpc.Dial` |
| Memory (context store) | `memory.Store` (an agent's notes under `/memories`, scoped by tenant or user) | `memorytest.RunStore` | an agent's `memory.store.backend` through `memoryBackends` (`cmd/agentkit/memory.go`) |
| Credential vault storage | `credentials.BlobStore` (sealed blobs only, versions for compare-and-swap) | `credentialstest.RunBlobStore`, `RunVault`, `CheckAtRest` | the config's `credentials.backend` through `credentialBackends` (`cmd/agentkit/credentials.go`) |
| Console sessions | `login.Store` (sign-in sessions, keyed by the SHA-256 of the session id; default `login.MemoryStore`) | `logintest.RunStore` | `login.WithStore` where `cli.login` builds the sign-in (`cmd/agentkit/users.go`); only the memory store today |
| Memory (search store) | `memory.Index` (items found by query, scoped by tenant or user) | `memorytest.RunIndex` (keyword, semantic and hybrid, each with its relevance set), `memorytest.RunEmbedderChange` for a persistent one | an agent's `memory.index.backend` through `memoryIndexBackends` (`cmd/agentkit/memory_open.go`) |
| Embedder | `memory.Embedder` (texts to vectors for semantic and hybrid search) | `embedtest.Run` (cassettes: batching, the 429 retry, the body limit, the key canary) | an agent's `memory.index.embedder.type` in `newEmbedder` (`cmd/agentkit/memory_embed.go`) |

A new store backend: a package under `pkg/store` that passes the suites, a
`Backend` in `storeopen` behind a build tag with its `_none.go` twin, its
SDK in `internal/archtest/rules.go`, and a row in docs/SIZES.md. A bus
backend is the same in `busopen`. A model provider is a package under
`pkg/llm`, a `models_<name>.go` that builds it, and its line in
`modelProviders`. It must give up on a silent stream: its own idle timeout
if it can see the provider's keep-alives, else `llm.WithIdleTimeout`; its
conformance test declares it with `llmtest.WithStallTimeout`. A memory
backend is a package under `pkg/memory` that runs each command as one
transaction through `pkg/memory/internal/fileops` (the commands' rules live
there once) and passes `memorytest.RunStore`, plus its line in
`memoryBackends`, behind a build tag when it brings a driver. A search
store backend passes `memorytest.RunIndex`, ranks keyword searches with
`pkg/memory/internal/keyword` and semantic and hybrid ones with
`pkg/memory/internal/semantic` (so every backend returns the same results,
scores included), and adds its line to `memoryIndexBackends`. An embedding
API is a constructor in `pkg/memory/embed` (or its own package) that passes
`embedtest.Run` on its cassettes, plus its `type` in `newEmbedder`.

## Topologies

All in one (today's default, plain function calls):

```sh
AGENTKIT_COMPONENTS=agent,web,mcp-server agentkit serve -config config.json
```

The runner bundled with the API, the toolsets apart:

```sh
# toolsets container
AGENTKIT_COMPONENTS=toolsets,mcp-server AGENTKIT_RPC_TOKEN=... AGENTKIT_API_TOKEN=... agentkit serve
# API and runner in one container
AGENTKIT_COMPONENTS=agent,web AGENTKIT_TOOLSETS_URL=http://toolsets:8080 AGENTKIT_RPC_TOKEN=... agentkit serve
```

Three containers, one per component:

```sh
docker network create agk
docker run -d --name toolsets --network agk -v $PWD/config.json:/etc/agentkit/config.json:ro \
  -e AGENTKIT_COMPONENTS=toolsets,mcp-server -e AGENTKIT_RPC_TOKEN=$RPC_TOKEN -e AGENTKIT_API_TOKEN=$API_TOKEN agentkit-toolsets
docker run -d --name runner --network agk -v $PWD/config.json:/etc/agentkit/config.json:ro \
  -e AGENTKIT_COMPONENTS=runner -e AGENTKIT_RPC_TOKEN=$RPC_TOKEN \
  -e AGENTKIT_TOOLSETS_URL=http://toolsets:8080 -e ANTHROPIC_API_KEY agentkit-runner
docker run -d --name api --network agk -p 8080:8080 -v $PWD/config.json:/etc/agentkit/config.json:ro \
  -e AGENTKIT_COMPONENTS=agent,web -e AGENTKIT_RPC_TOKEN=$RPC_TOKEN -e AGENTKIT_API_TOKEN=$API_TOKEN \
  -e AGENTKIT_RUNNER_URL=http://runner:8080 -e AGENTKIT_TOOLSETS_URL=http://toolsets:8080 agentkit-api
```

On Kubernetes the same three become three Deployments and Services, with the
config in a ConfigMap and the tokens and keys in Secrets:
[deploy/k8s/base/split.yaml](../deploy/k8s/base/split.yaml). `make k8s-up` runs them, with
Postgres, on Docker Desktop's Kubernetes ([deploy/k8s/README.md](../deploy/k8s/README.md)).

## Adding a method or a service

The transport is `pkg/rpc`: a method is declared once, as a typed value, and
that value both registers the server's handler and makes the client's call.
Messages are JSON, so the types already in `pkg/llm` and `pkg/harness` cross
as they are.

To add a method to a service (say, `toolsets/Describe`):

1. Declare it next to the others: `describeMethod = rpc.Unary[describeReq, describeResp]{Name: Service + "/Describe", Errors: codes}`.
2. In `Register`, handle it: `describeMethod.Handle(s, func(ctx, r describeReq) (describeResp, error) { ... })`.
3. On the client, call it: `describeMethod.Call(ctx, c.conn, describeReq{...})`.

A stream (events as they happen) is `rpc.Stream[Req, Msg]` and a handler
returning `iter.Seq2[Msg, error]`.

To put a new interface on the wire (a credential vault, a memory store):

1. A package `xxxrpc` next to the interface, with `Service`, the methods, the
   error `Code`s callers tell apart, `Register(s *rpc.Server, impl ...)` and
   a `Client` whose methods implement the interface. `pkg/tool/toolrpc` is
   the model to copy.
2. Run the interface's conformance suite through `rpc.Pipe` and over HTTP
   (`toolrpc`'s `TestConformance`), so the remote implementation cannot drift.
3. In `cmd/agentkit`: a component name, a constructor that serves it at
   `rpc.PathPrefix + Service + "/"`, a `..._URL` variable that swaps the
   local implementation for the client, and a tag for an image that leaves
   it out (`split.go`, `backends.go`).

Why not ConnectRPC, as ENGINEERING.md §1.5 first planned: the interfaces on
the wire already have JSON forms (`llm.Message`, harness events), so this
needs no protobuf definitions, no code generator and no dependency (it adds
about 0.16 MB to an image), and it is curl-able. If a non-Go client of our
internal services appears, the method values are the place to generate a
schema from.
