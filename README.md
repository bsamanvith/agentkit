# agentkit

A modular Go framework for building agentic harnesses, tools, MCP servers/clients, and secure credential handling.

- **Harness**: provider-agnostic agent loop with streaming, cancellation, parallel tool calls, budgets and hooks.
- **Flows**: multi-step and multi-agent runs (sequence, parallel, loop, route, human approval) with a checkpoint after every step, so runs resume.
- **Tools**: our own lightweight tool protocol (ConnectRPC: gRPC and JSON/HTTP from one handler).
- **MCP**: one server process serves many toolsets (Salesforce, HubSpot, Gmail, ...), each at its own URL path, chosen by config; also connects to external MCP servers.
- **Credentials**: API key, OAuth2, JWT, basic, and mTLS with pluggable secure storage backends.

Each package can be imported on its own or run as a lightweight server. One `agentkit` binary runs any mix of components, in one container or spread across several.

## Status

**Early development.** Milestones from the [build order](docs/ENGINEERING.md#0-build-order):

| Milestone | State |
|---|---|
| M0 Tooling: `make check`, lint, architecture test, test helpers, CI | Done |
| M1 `pkg/tool`: tool definitions, `tool.Func[In, Out]`, registry, middleware, conformance suite | Done |
| M2 Toolsets from config; one MCP server, one URL path per toolset | Done ([T026](tasks/T026-toolsets-from-config.md), [T018](tasks/T018-mcp-server.md)) |
| M3 `llm`, Anthropic adapter, harness, `flow` | Done ([E04](tasks/E04-llm-harness-flow.md)): `pkg/llm`, the Anthropic adapter (live-tested), `pkg/harness`, `pkg/flow`, `pkg/agentstep` (agents as resumable flow steps) and `agentkit run` / `agentkit flow demo`, run end to end against Claude ([T024](tasks/T024-run-cli.md)). The OpenAI-compatible adapter moved to [E08](tasks/E08-model-providers.md) |
| M4 Store, runner and compaction | **In progress** ([E05](tasks/E05-store-runner.md)): `pkg/clock`, `pkg/tenancy`, the flow step-tree check and `pkg/store` done; next the SQLite store and the runner |
| M5 Agent API, containers and pods | **In progress**: `agentkit serve`, the agent API v0 (`POST /v1/runs`, streamed over SSE, in memory; [T036](tasks/T036-agent-api-v0.md)) and the distroless image ([T005](tasks/T005-container-image.md)) done: **deployable**. Next Compose and persistence (T037) |
| M6 remote tools | Planned |

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

Run an agent against Claude with the built-in demo tools, or a two-step flow that stops for approval and is resumed by a second process (credentials from `ANTHROPIC_API_KEY` or `ant auth login`):

```sh
go run ./cmd/agentkit run "What is 48271 times 9733?"
go run ./cmd/agentkit flow demo -state demo.json "Go generics"   # researches, then waits for approval
go run ./cmd/agentkit flow demo -state demo.json -approve        # a new process writes the draft
```

Serve the agent API and the same tools over MCP from one process, and try them with a short demo ([docs/QUICKSTART.md](docs/QUICKSTART.md)):

```sh
export ANTHROPIC_API_KEY=sk-ant-...
scripts/try-agent.sh            # build, start, demo (a streamed tool run, a follow-up, MCP), keep serving
```

## Development

Requires Go (the right toolchain is fetched automatically from `go.mod`).

```sh
make check    # tidy, format, lint, architecture rules, unit tests (race, shuffled)
make fuzz     # run every fuzz target (FUZZTIME=10s by default)
make golden   # rewrite golden files after an intended change; review the diff
make test-live  # live tests against Claude (ANTHROPIC_API_KEY, or ANTHROPIC_KEY as set in Claude sessions); never run in CI
make help     # all targets
```

How we work:
- **Plan and conventions**: [docs/PLAN.md](docs/PLAN.md) (what we build), [docs/ENGINEERING.md](docs/ENGINEERING.md) (module design, testing, local environments, agent skills) and [docs/WORKFLOW.md](docs/WORKFLOW.md) (how the planner and parallel sessions work, and lessons learned).
- **Status**: [tasks/STATUS.md](tasks/STATUS.md): done, running, next, and what waits on whom.
- **Tasks**: one file per task in [tasks/](tasks/), grouped by epics. A task's acceptance criteria are its test list.
- **Workflow**: changes are committed straight to `main`; `make check` must pass first, and CI runs it again on every push.

## License

[Apache-2.0](LICENSE)
