# agentkit: Scope and Module Plan

> Status: planning. This document captures the agreed scope and module shape. Nothing here is implemented yet.

## Goals

- **Go** for fast startup, concurrency, and efficiency.
- **Lightweight containers**: distroless images of roughly 10-20 MB, startup under 50 ms.
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
  mcp/
    client/       connect to external MCP servers
    server/       expose a tool registry as an MCP server
    bridge/       two-way MCP <-> tool protocol mapping
  credentials/    credential types and secure storage backends
  store/          persistence (SQLite, Postgres)
  tenancy/        multi-tenant context helpers
  transport/      shared HTTP/gRPC and streaming plumbing
  observability/  tracing, metrics, logging, audit
  config/         unified YAML/env configuration
cmd/
  toolserver/     lightweight tools API
  harnessd/       harness API server
  allinone/       both in one binary
```

## Packages (`pkg/`)

### `harness`

The agent loop: **plan -> model -> tools -> observe**.

- Streaming of model output and run events.
- Cancellation via `context.Context`.
- Parallel tool calls using goroutines.
- Step and token budgets.
- Pre- and post-tool-call hooks.
- Depends only on interfaces: `Model`, `ToolRegistry`, `Memory`, `Store`.

### `llm`

Provider-agnostic `Model` interface. The harness never sees provider-specific types.

- Adapters:
  - Anthropic (Claude) first.
  - OpenAI-compatible APIs (OpenAI, Grok, Groq, vLLM, Ollama).
  - Later: Bedrock, Gemini.
- Handles streaming, provider tool-call formats, and retries.

### `tool`

Our own tool protocol and the core abstraction.

- `Tool` definition: name, description, JSON Schema for input and output, auth requirements.
- `Invoke(ctx, input)`.
- Registry.
- Middleware: auth, rate limiting, timeouts, logging, validation.
- Typed helper `tool.Func[In, Out]` that derives the schema from Go structs.
- Wire protocol defined in **protobuf**, served over **gRPC** internally and as **JSON/HTTP via ConnectRPC** from the same definitions (for non-Go clients and browsers).

### `mcp`

- **`mcp/client`**: connects the harness to external MCP servers over stdio, Streamable HTTP, and SSE. Discovers their tools and wraps them as `tool.Tool`, so the harness does not care where a tool comes from.
- **`mcp/server`**: exposes any tool registry as a spec-compliant MCP server. This is how new MCP tools are declared.
- **`mcp/bridge`**: two-way mapping between MCP and our tool protocol.

External-facing MCP stays **spec-compliant** (stdio and Streamable HTTP, JSON-RPC). A gRPC transport for MCP exists only as a proposal and is not part of the spec, so gRPC is used only for our internal protocol.

### `credentials`

Framework to maintain and store credentials securely.

- **Types**: API key; OAuth2 (authorization code + PKCE, client credentials, device code, automatic refresh); JWT (sign/verify, JWKS); basic auth; mTLS certificates.
- **Storage backends**: encrypted local file (AES-GCM, key from env or OS keychain); AWS Secrets Manager / KMS; HashiCorp Vault; Kubernetes secrets.
- **Scoping**: per tenant, user, and tool.
- **Usage**: tools receive short-lived injected tokens, never raw secrets.

### `store`

Stateful from day one.

- `Store` interface with **SQLite** (local) and **Postgres** (server) implementations.
- Holds runs, messages, tool calls, checkpoints (for resume), and tenant data.

### `tenancy`

Multi-tenant in v1.

- Go has no thread-locals (goroutines are not threads), so tenant, user, and request info is carried in `context.Context`.
- Set once by middleware; read via helpers such as `tenant.From(ctx)`.
- Postgres row-level security keyed on tenant id.

### `transport`

Shared HTTP and gRPC plumbing; SSE and WebSocket streaming.

### `observability`

OpenTelemetry traces and metrics, `slog` logging, and a tool-call audit log.

### `config`

One YAML/env configuration for enabled tools, MCP servers, credentials, and models.

## Binaries (`cmd/`)

- **`toolserver`**: lightweight tools API serving a registry over our protocol and MCP simultaneously.
- **`harnessd`**: the harness as an API: start a run, stream events, resume, cancel. Uses local tools or remote tool/MCP servers.
- **`allinone`**: both in one binary, for development and small deployments.

Running split or combined is a configuration choice only: the harness reaches tools through the same interface whether they are in-process or remote.

## Decisions

| Topic | Decision |
|---|---|
| License | Apache-2.0 (explicit patent grant) |
| Models | Provider-agnostic `Model` interface; Claude first, then OpenAI-compatible |
| State | Stateful; SQLite locally, Postgres for servers |
| Tenancy | Multi-tenant in v1, scope carried in `context.Context` |
| Protocols | gRPC (plus ConnectRPC JSON/HTTP) internally; spec MCP externally |

## Open items / next steps

- Finalize the project name (`agentkit` is a placeholder).
- Define the protobuf schema for the tool protocol.
- Define the core interfaces: `Model`, `Tool` / `ToolRegistry`, `Memory`, `Store`, credential store.
- Decide milestone ordering across packages.
- Choose the MCP SDK approach (official Go SDK vs. own implementation).
- Define the config file format and schema.
