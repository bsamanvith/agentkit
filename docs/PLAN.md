# agentkit: Scope and Module Plan

> The product map (layers, products and ideas, runtimes, UI) is in [PRODUCT.md](PRODUCT.md).

> Status: agreed scope and module shape, being implemented in the [build order](#build-order). Done so far: M0 (tooling), M1 (`pkg/tool`), M2 (toolsets and the MCP server) and M3 (`pkg/llm`, the Anthropic adapter, `pkg/harness`, `pkg/flow`, `pkg/agentstep` and `agentkit run`, run end to end against Claude; the OpenAI-compatible adapter moved to E08). Current focus: M4 ([E05](../tasks/E05-store-runner.md): `pkg/store` done, SQLite and the runner next) and M5 (agent API v0 and the image done; deployable). Overview: [tasks/STATUS.md](../tasks/STATUS.md).

## Goals

- **Go** for fast startup, concurrency, and efficiency.
- **Lightweight containers**: distroless images, startup under 50 ms. Size target 10-20 MB; the enforced budget is set after measuring the first real image (M5).
- **Modular**: every package is usable on its own as a library, or run as a server.

### Design rules

- Interfaces first.
- No global state.
- Standard library first; keep dependencies small.
- `context.Context` everywhere (cancellation, deadlines, tenant/request scope).

## Repository layout

```
pkg/
  harness/        agent loop
  llm/            provider-agnostic model interface + adapters
  tool/           our tool protocol and core abstraction
  toolset/        named toolsets built from config (factories, catalog, filters)
  mcp/
    client/       connect to external MCP servers
    server/       serve many toolsets over MCP, one URL path each
    bridge/       two-way MCP <-> tool protocol mapping
  credentials/    credential types and secure storage backends
  store/          persistence (SQLite, Postgres)
  tenancy/        multi-tenant context helpers
  transport/      shared HTTP/gRPC and streaming plumbing
  observability/  tracing, metrics, logging, audit
  config/         unified YAML/env configuration
  component/      component contract and runtime (what cmd/agentkit runs)
connectors/       one package per external system (salesforce, hubspot, gmail, ...), each a toolset factory
cmd/
  agentkit/       one binary; which components run is set by config
  fakellm/        scripted model server for local runs and tests
```

## Packages (`pkg/`)

### `harness`

The agent loop: **model -> tools -> results -> model**, until the model answers.

- Streaming of model output and run events.
- Cancellation via `context.Context`; stopping iteration early cancels the model call and the running tools.
- Parallel tool calls using goroutines, bounded; results go back in one message, in call order.
- Turn and token budgets.
- Pre- and post-tool-call hooks.
- Depends only on two interfaces, `llm.Model` and `tool.Registry`, plus an injected clock. It stores nothing and imports no adapter, so providers, tool backends, storage and tracing all plug in from other packages.
- Memory is a value, not a dependency: a run takes a `Transcript` (JSON-serializable) and returns the grown one. Each finished turn is delivered as an event carrying the messages it added and a `Record` (message times, model and tool timings, usage), so a store, a tracing exporter or a UI consume events, and retries, provider options or tracing wrap the model and registry.
- Append-only conversation history: model output (including thinking blocks) is passed back unchanged, never edited. A run that stops early leaves a transcript that ends after a whole turn and can be resumed.

### `flow`

Multi-step and multi-agent execution on top of the harness, as plain Go plus a few combinators. No graph DSL.

- `Step` interface: anything that turns `State` into new `State`. An agent run is a `Step`; so is a plain Go function.
- Combinators: `Sequence`, `Parallel` (fan out, merge), `Loop` (until a condition or a limit), `Route` (pick the next step from output), `Approve` (pause until a human decides).
- The runner checkpoints `State` after every step through a `Checkpointer` interface (in memory first, `store` later). Resume loads the last checkpoint and skips finished steps; `Approve` is a checkpoint that waits.
- Every step emits the same event type, so logs, UI and the audit trail see one stream.
- A declarative (config-defined) graph is built only if it is ever needed, on top of these combinators. Durable-execution engines (DBOS, Temporal, Restate) stay an option for runs that must survive for days, behind the same `Checkpointer` interface.

### `llm`

Provider-agnostic `Model` interface. The harness never sees provider-specific types.

- Native adapters, each on the provider's official SDK:
  - Anthropic (Claude) first, with full feature support (thinking blocks, prompt caching, effort, refusal stop reasons).
  - OpenAI-compatible APIs (OpenAI, Grok, Groq, vLLM, Ollama). This adapter also covers gateways such as the LiteLLM proxy or OpenRouter, for deployments that want one; we never require a gateway.
  - Later: Gemini, Bedrock.
- Handles streaming, provider tool-call formats, and retries.
- Provider features with no neutral equivalent are carried as typed, provider-scoped options and opaque blocks, never dropped silently.

### `tool`

Our own tool protocol and the core abstraction.

- `Tool` definition: name, description, JSON Schema for input and output, auth requirements.
- `Invoke(ctx, input)`.
- Registry.
- Middleware: auth, rate limiting, timeouts, logging, validation.
- Typed helper `tool.Func[In, Out]` that derives the schema from Go structs.
- Wire protocol defined in **protobuf** and served with **ConnectRPC** (`connect-go`). One handler on a standard `net/http` server speaks the gRPC, gRPC-Web and Connect (JSON over HTTP/1.1) protocols, so we do not need `grpc-go` or a second port.

### `mcp`

- **`mcp/client`**: connects the harness to external MCP servers over stdio, Streamable HTTP, and SSE. Discovers their tools and wraps them as `tool.Tool`, so the harness does not care where a tool comes from.
- **`mcp/server`**: **one process serves many toolsets, each at its own URL path** over Streamable HTTP: `/mcp/salesforce`, `/mcp/hubspot`, `/mcp/gmail`. A client connects to the path of the toolset it wants and sees only those tools. No stdio server per connector. Stateless mode by default, so replicas scale behind a plain load balancer. A single toolset can also be served over stdio for desktop clients, from the same code.
- **`mcp/bridge`**: two-way mapping between MCP and our tool protocol.

**Toolsets and connectors** (`pkg/toolset`):

- A **connector** is a Go package (for example `connectors/salesforce`) that provides a toolset factory: given its settings, it returns its tools.
- **Build time** decides which connectors are compiled into the image (all by default; build tags drop heavy ones). Go cannot load packages at runtime, so this is the only way to choose code.
- **Config** decides which toolsets run, under which names and with which settings. Two instances of one connector (two Salesforce orgs) are two named toolsets. Include/exclude filters expose a subset of a connector's tools.
- The harness uses the same toolsets in process, with no MCP hop.

```yaml
mcp:
  base_path: /mcp
  toolsets:
    - { name: salesforce, type: salesforce, settings: { instance_url: https://acme.my.salesforce.com } }
    - { name: hubspot,    type: hubspot }
    - { name: gmail,      type: gmail, include: [gmail_search, gmail_read*] }
```

Auth, tenant resolution and per-tenant credentials plug in as HTTP middleware in front of every toolset path (credentials, [E07](../tasks/E07-auth-credentials.md)).

External-facing MCP stays **spec-compliant** (stdio and Streamable HTTP, JSON-RPC). A gRPC transport for MCP exists only as a proposal and is not part of the spec, so gRPC is used only for our internal protocol.

### `credentials`

Framework to maintain and store credentials securely.

- **Types**: API key; OAuth2 (authorization code + PKCE, client credentials, device code, automatic refresh); JWT (sign/verify, JWKS); basic auth; mTLS certificates.
- **Storage backends**: encrypted local file (AES-GCM, key from env or OS keychain); AWS Secrets Manager / KMS; HashiCorp Vault; Kubernetes secrets.
- **Scoping**: per tenant, user, and tool.
- **Usage**: tools receive short-lived injected tokens, never raw secrets.

### `store`

Stateful from day one.

- **SQLite** (local) and **Postgres** (server) first. The interfaces must not assume SQL, so key-value and document backends can implement them later.
- Holds runs, messages, tool calls, checkpoints (for resume), and tenant data.
- Several small interfaces, one per kind of data (`RunStore`, `CheckpointStore`, `ConversationStore`, ...), not one large `Store`. A backend can implement all of them.
- **Conversations** (direction for E05; the harness holds no store, see [`harness`](#harness)):
  - A conversation is an append-only log. A message's JSON (`llm.Message`) is stored exactly and is the only thing ever read back into a request; times, usage and tool-call timings sit beside it, for display and billing.
  - Two reads: **history** for people (every message, paged with opaque backend cursors, seek by message ID or time) and **context** for the agent (the latest compaction summary plus the messages after it). Compaction adds a checkpoint; it never edits or deletes messages.
  - Writes need one primitive that every backend has: create an entry only if it is absent, numbered per conversation, so concurrent writers conflict instead of interleaving. One entry per turn makes a turn atomic.
  - Active conversations keep their decoded context in an in-process cache keyed by the log position it was built from. The log only grows, so the cache is appended to, never invalidated.
  - The store never imports `harness`. The component that runs agents turns events into entries and a stored context back into a `Transcript`.

### `tenancy`

Multi-tenant in v1.

- Go has no thread-locals (goroutines are not threads), so tenant, user, and request info is carried in `context.Context`.
- Set once by middleware; read via helpers such as `tenant.From(ctx)`.
- Isolation is enforced in the application layer for every backend (every store query is scoped by the tenant in `ctx`), and checked by the store conformance suite for both SQLite and Postgres.
- Postgres row-level security keyed on tenant id is an additional layer on top, not the only one.

### `transport`

Shared HTTP and gRPC plumbing; SSE and WebSocket streaming.

### `observability`

OpenTelemetry traces and metrics, `slog` logging, and a tool-call audit log.

### `config`

One YAML/env configuration for enabled tools, MCP servers, credentials, and models.

## Components and deployment (`cmd/`)

There is one binary, `agentkit`. It is made of **components**, and config decides which components run in a given process:

- **`harness`**: the harness as an API: start a run, stream events, resume, cancel.
- **`tools`**: a tool registry served over our protocol (gRPC/ConnectRPC).
- **`mcp-server`**: the same registry exposed as a spec-compliant MCP server.
- More as needed (for example a credentials broker), each one a package with the same component contract.

Each dependency between components is configured as **`local`** (in-process, a direct call) or **`remote`** (a URL; reached through a client that implements the same Go interface). The harness reaches tools through `tool.Registry` whether that is an in-memory registry or a gRPC client to another container.

That gives three ways to package, all from the same code:

1. **Everything in one container**: `components: [harness, tools, mcp-server]`, all deps `local`.
2. **Split across containers**: the same image run several times with different configs, talking over gRPC (internal) or MCP (external).
3. **Embedded**: a Go program imports the packages and wires its own `main`, without our binary at all.

Details: [ENGINEERING.md §1.5](ENGINEERING.md#15-components-mix-and-match-deployment).

## Decisions

| Topic | Decision |
|---|---|
| License | Apache-2.0 (explicit patent grant) |
| Models | Provider-agnostic `Model` interface; Claude first, then OpenAI-compatible |
| State | Stateful; SQLite locally, Postgres for servers |
| Tenancy | Multi-tenant in v1, scope carried in `context.Context` |
| Protocols | ConnectRPC internally (speaks gRPC, gRPC-Web and JSON from one handler); spec MCP externally |
| MCP library | Official Go SDK (`github.com/modelcontextprotocol/go-sdk`) |
| JSON Schema | `github.com/google/jsonschema-go` (same library the MCP SDK uses): derive schemas from Go structs and validate input |
| Go version | `go 1.26.0` minimum in `go.mod` (oldest supported release); `toolchain go1.27.2` for development |
| Agent loop | Our own `pkg/harness`, not an agent framework (Google ADK, Genkit, Eino). Frameworks bring their own model, tool and session types; ADK Go has no Claude support and its model interface is built on Gemini types; Eino is pre-1.0 |
| Agent memory | The harness holds no store: a run takes a `Transcript` and returns a grown one, and each turn is an event (messages plus a `Record`). Persistence, history, compaction and caching live in `store` and the component that runs agents |
| Workflows | Our own `pkg/flow` (sequence, parallel, loop, route, approve) with a checkpoint at every step |
| Model gateways | Native adapters; LiteLLM-style gateways work through the OpenAI-compatible adapter, never required |
| Other agent frameworks | Integrations, not foundations: ADK, Genkit and Eino agents use our tools through our MCP server; a Claude adapter for ADK may ship later as a separate optional module |
| Go modules | One module for the whole repo; split out a nested module only if a backend's dependencies cause problems for users |
| Deployment | One `agentkit` binary/image; components and local-vs-remote wiring chosen by config |

## Build order

Build the product from the inside out, and add infrastructure when there is something real for it to run. Detail and task links: [ENGINEERING.md §0](ENGINEERING.md#0-build-order).

1. **M0 Tooling**: `make check`, lint, architecture test, minimal CI. *Done.*
2. **M1 `pkg/tool`**: tool definition, `tool.Func[In, Out]`, registry, middleware, conformance suite. *Done.*
3. **M2 Toolsets and MCP server**: toolsets built from config, many toolsets served by one HTTP process at one path each. Usable from Claude Code, Codex or any MCP client. *Done.*
4. **M3 `llm` + Anthropic adapter + harness + `flow`**: run an agent loop and a multi-step flow from a small CLI. *Done* (E04; the OpenAI-compatible adapter moved to [E08](../tasks/E08-model-providers.md), built when a second provider is needed).
5. **M4 `store` (SQLite), `runner`, compaction** ([E05](../tasks/E05-store-runner.md)): runs, conversations and checkpoints persist; resume works; long conversations compact. **Current focus.**
6. **M5 Agent API and containers**: `pkg/component` and `agentkit serve` (*done*, T003); the agent API over HTTP + SSE ([E09](../tasks/E09-agent-api.md): v0 in memory now, v1 on the runner after M4); `fakellm`, image, Compose, smoke tests.
7. **M6 Tool protocol over ConnectRPC**: remote registry; the `split` topology.
8. Then MCP client and bridge, Postgres and tenancy, credentials, observability. Auth and credentials ([E07](../tasks/E07-auth-credentials.md)) are planned and run in parallel with M4: they are needed before any public deployment. The first connectors, Tavily, Exa and Parallel for web search and extraction ([E10](../tasks/E10-search-connectors.md)), run in parallel too. Then the clients ([E11](../tasks/E11-clients.md)): a web client served by `agentkit` itself and a CLI with a headless mode, to deploy the API in Docker and test the harness through them. Skills and memory ([E12](../tasks/E12-skills-memory.md)) plug into the harness as tools and context blocks, without the harness holding a store.

## Open items / next steps

- Finalize the project name (`agentkit` is a placeholder).
- Define the protobuf schema for the tool protocol (M6).
- Define the config file format and schema (M5).
