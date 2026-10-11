---
id: E03
title: Tool core and MCP server (M1, M2)
status: in-progress
---

# E03: Tool core and MCP server

The first real feature. `pkg/tool` is what every other part depends on (harness, MCP server, `tools` component, remote client), it is pure Go with no IO, and it exercises every convention in [ENGINEERING.md](../docs/ENGINEERING.md) once: interface + `xxxtest` + conformance suite, sentinel errors, golden files, fuzzing. The MCP server then gives a real end-to-end test without any containers: Claude Code or Codex calling Go functions we wrote.

## Tasks

- [ ] [T017](T017-tool-core.md) `pkg/tool`: definition, `tool.Func[In, Out]`, registry, middleware, conformance suite *(in review)*
- [ ] [T018](T018-mcp-stdio-server.md) MCP server over stdio for any `tool.Registry`

Depends on [E01](E01-foundations.md) T001 and T002.
