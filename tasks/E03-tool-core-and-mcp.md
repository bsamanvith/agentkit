---
id: E03
title: Tool core, toolsets and MCP server (M1, M2)
status: done
---

# E03: Tool core, toolsets and MCP server

The first real feature. `pkg/tool` is what every other part depends on (harness, MCP server, `tools` component, remote client), it is pure Go with no IO, and it exercises every convention in [ENGINEERING.md](../docs/ENGINEERING.md) once: interface + `xxxtest` + conformance suite, sentinel errors, golden files, fuzzing. Then one MCP server process serves many toolsets, each at its own URL path (`/mcp/salesforce`, `/mcp/gmail`, ...), chosen by config: no stdio server per connector.

## Tasks

- [x] [T017](T017-tool-core.md) `pkg/tool`: definition, `tool.Func[In, Out]`, registry, middleware, conformance suite
- [x] [T026](T026-toolsets-from-config.md) `pkg/toolset`: build named toolsets from config (factories, catalog, filters)
- [x] [T018](T018-mcp-server.md) MCP server: many toolsets on one HTTP server, one URL path each

T026 and T018 are independent (T018 takes built toolsets as input) and can run in parallel. T003 (E01) wires them into `cmd/agentkit`.

Depends on [E01](E01-foundations.md) T001 and T002.
