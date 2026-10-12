---
id: T082
title: "Tool metadata: kind, risk, interrupts, approval, tags, display; MCP annotations; connectors classified"
epic: E17
status: todo
depends_on: [T017]
---

# T082: tool metadata

## Context

Policies (T083), interrupts (T084), the console's tool display and the agent builder all need tools to say what they do. Decisions: [E17](E17-tool-safety-control.md).

## Scope

- **New fields:** optional fields on `tool.Definition` (`Kind`, `Risk`, `Interrupts`, `RequiresApproval`, `Tags`, `Display{Title, Icon, Present, Past}`), with JSON names, validation, and `Clone` covering them. Definitions without them still validate.
- **Classification:** `tool.Classify(def) (kind, risk)` applies the default for unclassified tools (`write`/`medium`).
- **MCP mapping:** to and from MCP tool annotations (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`, `title`). `pkg/mcp/server` publishes them.
- **Built-in tools classified:** the demo tools; Tavily, Exa and Parallel (search and extract are `read`/`low`, tag `external`).
- **Kit check:** T049's kit gains a check (staged opt-in, then mandatory) that every connector tool declares `kind` and `risk`.
- **API:** `/v1/toolsets/{name}/tools` (T062) returns the new fields.

## References

- [MCP tools: annotations](https://modelcontextprotocol.io/specification/latest/server/tools) (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`)
- docs/UI.md "Tool calls" (the display fields the console needs)
- OWASP Top 10 for LLM Applications, "Excessive Agency" (why classes and least privilege)

## Acceptance criteria

- [ ] **Round trip:** the new fields JSON round-trip, and `Clone` deep-copies them (a planted shared slice is caught).
- [ ] **MCP mapping:** to and from annotations is lossless where both sides have the concept (table test), and `tools/list` over MCP shows the hints.
- [ ] **Default class:** an unclassified tool is reported as `write`/`medium` by `Classify`.
- [ ] **Built-in tools:** all are classified, and the kit check passes for the three connectors.
- [ ] **Mutation list** recorded.
