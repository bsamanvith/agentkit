---
id: T004
title: fakellm scripted model server
epic: E01
status: todo
depends_on: [T003]
---

# T004: fakellm scripted model server

## Context

Local runs and e2e tests must be deterministic and must not need an API key. `fakellm` answers model requests from a script, so the harness can be exercised end to end, including tool calls and streaming.

## Scope

**Decision (planner, after T024):** `fakellm` is still needed, because Compose and the smoke suite need a model endpoint that runs as its own container with no key, and T024's replay server exists only inside a test. It is not a second implementation. Start from what exists: T024's replay end-to-end tests (`cmd/agentkit/e2e_test.go`) already run a local server that replays recorded Claude responses (`testdata/e2e/<scenario>/NN.http`). Extract that server into `cmd/fakellm` rather than writing a second one, so recorded fixtures and scripted replies share one format where possible.


- `cmd/fakellm`: HTTP server implementing a subset of the Anthropic Messages API: `POST /v1/messages`, non-streaming and SSE streaming, `tool_use` content blocks.
- Script format: JSONL in `deploy/local/fakellm/`, each entry = optional match (substring of last user message or tool result) + response (text and/or tool calls). First match wins; a default fallback response.
- Select a script per request with an `X-Fakellm-Script` header, else the server default (`FAKELLM_SCRIPT`).
- Logs each request so e2e failures are debuggable.

## Out of scope

- OpenAI-compatible mode (open question in ENGINEERING.md §5).
- Token accounting beyond fixed fake usage numbers.

## Acceptance criteria

- [ ] A matching entry returns its text response in Anthropic response shape.
- [ ] A tool-call entry returns a `tool_use` block; the follow-up request with the `tool_result` matches the next entry.
- [ ] `stream: true` returns a valid SSE event sequence (`message_start` ... `message_stop`) — checked with a golden file.
- [ ] No match and no fallback returns a 4xx with a clear error body.
- [ ] The official Anthropic Go SDK, pointed at `fakellm`, can complete a request (proves wire compatibility).

## Notes

-
