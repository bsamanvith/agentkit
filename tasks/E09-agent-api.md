---
id: E09
title: Agent API (the harness as a service)
status: todo
---

# E09: Agent API

`agentkit serve` serves MCP toolsets (T003), but nothing serves the **agent**: there is no API to send a prompt and get the harness's run back. This epic adds the `agent` component from [PLAN.md components](../docs/PLAN.md#components-and-deployment-cmd): start a run, stream its events, continue a conversation, and, once the store exists, resume, approve, cancel and list.

## Decisions

| Question | Decision |
|---|---|
| Wire format of the public run API | **HTTP + JSON, with Server-Sent Events** for the event stream: works with `curl`, browsers (`EventSource`/`fetch`), and any language without generated clients. ConnectRPC stays the protocol between our own components (E06, remote tools). A ConnectRPC form of this API can be added later from the same handlers if a typed client is needed. |
| Event format | The harness events with JSON wire forms (a T022 deferred item, done here): `run_start`, `text_delta`, `tool_call`, `tool_result`, `turn_done` (with usage and timings), `run_done` / `run_error`. Versioned (`"v": 1`), additive changes only. |
| Tools available to the agent | Named toolsets from the same config as `mcp-server` (`pkg/toolset`), merged into one registry for the harness, in process (no MCP hop). Tool name clashes between toolsets fail startup unless the agent config asks for a `<toolset>__` prefix. |
| Two steps | **v0 (T036)** needs nothing unbuilt: conversations in memory, lost on restart, documented as such. **v1 (T037)** switches to `pkg/runner` (T030) for persistence, resume, approvals, cancel and listing, keeping the same API. |
| Auth before E07 | A static bearer token from the environment, required whenever the API listens on a non-loopback address. Real auth and tenants come with E07 as middleware. |

## Tasks

- [x] [T036](T036-agent-api-v0.md) `agent` component v0: runs and conversations over HTTP + SSE, in memory
- [x] [T037](T037-agent-api-v1.md) `agent` component v1: on `pkg/runner` (persist, resume, approve, cancel, list)

T036 can start now, in parallel with T005 (image). Together they are the shortest path to deploying the API and testing it with a real model.
