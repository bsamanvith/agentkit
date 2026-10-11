# agentkit

A modular Go framework for building agentic harnesses, tools, MCP servers/clients, and secure credential handling.

- **Harness**: provider-agnostic agent loop with streaming, cancellation, parallel tool calls, budgets and hooks.
- **Flows**: multi-step and multi-agent runs (sequence, parallel, loop, route, human approval) with a checkpoint after every step, so runs resume.
- **Tools**: our own lightweight tool protocol (ConnectRPC: gRPC and JSON/HTTP from one handler).
- **MCP**: connect to external MCP servers and expose any tool registry as a spec-compliant MCP server.
- **Credentials**: API key, OAuth2, JWT, basic, and mTLS with pluggable secure storage backends.

Each package can be imported on its own or run as a lightweight server. One `agentkit` binary runs any mix of components, in one container or spread across several.

## Status

**Early development.** Milestones from the [build order](docs/ENGINEERING.md#0-build-order):

| Milestone | State |
|---|---|
| M0 Tooling: `make check`, lint, architecture test, test helpers, CI | Done |
| M1 `pkg/tool`: tool definitions, `tool.Func[In, Out]`, registry, middleware, conformance suite | Done |
| M2 MCP server over stdio | Planned ([T018](tasks/T018-mcp-stdio-server.md)) |
| M3 `llm`, Anthropic adapter, harness, `flow` | **In progress** ([E04](tasks/E04-llm-harness-flow.md)): `pkg/llm` done, next [T021](tasks/T021-llm-anthropic.md) (Anthropic adapter) and [T022](tasks/T022-harness-loop.md) (harness) |
| M4 SQLite store, M5 containers and pods, M6 remote tools | Planned |

## What works today

Define a tool from a typed Go function; the JSON Schema is derived from the types and every call's input is validated before your code runs:

```go
type addInput struct {
	A int `json:"a" jsonschema:"first number"`
	B int `json:"b" jsonschema:"second number"`
}
type addOutput struct {
	Sum int `json:"sum"`
}

add, err := tool.Func("add", "Add two integers.",
	func(_ context.Context, in addInput) (addOutput, error) {
		return addOutput{Sum: in.A + in.B}, nil
	})
// handle err
reg, err := tool.NewRegistry([]tool.Tool{add}, tool.WithMiddleware(tool.Timeout(5*time.Second)))
// handle err
out, err := reg.Invoke(ctx, "add", json.RawMessage(`{"a":2,"b":3}`)) // {"sum":5}
```

Errors are classified for the caller: `tool.ErrInvalidInput` (bad input, the model can retry), `tool.ErrToolFailed` (the tool ran and failed, panics included), `tool.ErrNotFound`, and the context's own error when a run is cancelled.

## Development

Requires Go (the right toolchain is fetched automatically from `go.mod`).

```sh
make check    # tidy, format, lint, architecture rules, unit tests (race, shuffled)
make fuzz     # run every fuzz target (FUZZTIME=10s by default)
make golden   # rewrite golden files after an intended change; review the diff
make help     # all targets
```

How we work:
- **Plan and conventions**: [docs/PLAN.md](docs/PLAN.md) (what we build) and [docs/ENGINEERING.md](docs/ENGINEERING.md) (module design, testing, local environments, agent skills).
- **Tasks**: one file per task in [tasks/](tasks/), grouped by epics. A task's acceptance criteria are its test list.
- **Workflow**: changes are committed straight to `main`; `make check` must pass first, and CI runs it again on every push.

## License

[Apache-2.0](LICENSE)
