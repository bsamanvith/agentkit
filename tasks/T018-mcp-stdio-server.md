---
id: T018
title: MCP server over stdio for any tool.Registry
epic: E03
status: todo
depends_on: [T017]
---

# T018: MCP server over stdio

## Context

The first real end-to-end path: any `tool.Registry` becomes an MCP server that Claude Code, Codex, or any MCP client can launch and call. Uses the official Go SDK (decision in [PLAN.md](../docs/PLAN.md#decisions)).

## Scope

- `pkg/mcp/server`: `New(reg tool.Registry, opts...)` returns a server that answers `tools/list` and `tools/call` from the registry. Transport-neutral; stdio here, Streamable HTTP comes with T003.
- Error mapping: `ErrInvalidInput` and `ErrToolFailed` become tool results with `isError: true` (the model sees them and can retry); `ErrNotFound` becomes a protocol error; panics never reach the client.
- Structured output: when a tool has an output schema, return `structuredContent` as well as text content.
- `cmd/agentkit-mcp-demo` (or an `examples/` program): a few demo tools served over stdio, to add to Claude Code / Codex by hand.

## Acceptance criteria

- [ ] Using the SDK's in-memory transport, an MCP client lists exactly the registry's tools with their schemas. — golden file for the `tools/list` result.
- [ ] `tools/call` with valid input returns the tool's output as text and structured content.
- [ ] Invalid input and tool failure come back as `isError: true` results, not protocol errors.
- [ ] Calling an unknown tool returns a protocol error.
- [ ] Cancelling the client request cancels the tool's context.
- [ ] Manual: the demo server added to Claude Code and to Codex lists and calls its tools. Recorded in Notes.

## Notes

-
