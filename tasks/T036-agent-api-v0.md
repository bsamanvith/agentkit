---
id: T036
title: "agent component v0: runs over HTTP + SSE, in memory"
epic: E09
status: todo
depends_on: [T003, T021, T022, T026]
---

# T036: Agent API v0

## Context

The first API to run an agent: a client posts a prompt and streams back what the harness does. Decisions in [E09](E09-agent-api.md). Everything it needs exists: `pkg/component` and `agentkit serve` (T003), the Anthropic adapter (T021), the harness (T022), toolsets (T026).

## Scope

- An `agent` component in `agentkit serve`, selected with `AGENTKIT_COMPONENTS=agent` (alone or with `mcp-server`), configured in the same config file under `agent`:
  - model: provider `anthropic`, model ID (default `claude-opus-5-5`), effort and output limit;
  - system prompt;
  - the toolsets the agent may use (by name, built with `toolset.Build`, merged into one registry in process), with optional `<toolset>__` prefixing;
  - limits: max turns, max tokens, max concurrent runs (503 with `Retry-After` past it), run timeout.
- Endpoints (base `/v1`):
  - `POST /v1/runs` `{"message": "...", "conversation_id": "..."?}`: starts a run. Returns `text/event-stream` of versioned events (`run_start` carrying the run and conversation IDs, `text_delta`, `tool_call`, `tool_result`, `turn_done` with usage and timings, then `run_done` with the final answer or `run_error` with a typed error). With `Accept: application/json` it waits and returns the final result instead.
  - `GET /v1/runs/{id}`: status and result of a recent run (in memory, bounded, oldest evicted).
  - `GET /v1/conversations/{id}`: the conversation's messages (in memory).
- A client disconnecting cancels the run (the harness's context), and the transcript keeps its completed turns.
- Conversations and runs live in memory: lost on restart, bounded by count and age. Documented clearly; T037 replaces this with the runner.
- Auth: if `AGENTKIT_API_TOKEN` is set, every `/v1` request needs `Authorization: Bearer <token>`; the component refuses to start on a non-loopback address without it.
- JSON wire forms for harness `Event` and `Record` (T022 deferred): defined here, versioned, golden-tested.

## Out of scope

- Persistence, resume, approvals, cancel by ID, listing runs (T037 on the runner).
- Real auth and tenants (E07).
- Other providers (E08).

## Acceptance criteria

- [ ] Event JSON for every event kind matches golden files; unknown fields are additive (a client ignoring them still parses).
- [ ] A run streams events in order and ends with exactly one `run_done` or `run_error`; the concatenated `text_delta`s equal the final answer. Checked by an invariant helper after every test (§2.6).
- [ ] A follow-up with `conversation_id` continues the same conversation: the model's request contains the earlier turns unchanged.
- [ ] The agent can call tools from two configured toolsets; a name clash fails startup unless prefixing is on.
- [ ] Client disconnect cancels the run: no further model or tool calls after it (driven from the client side, no timing).
- [ ] Concurrency limit: past it, 503 with `Retry-After`; concurrent runs on different conversations do not interfere (`-race`).
- [ ] With `AGENTKIT_API_TOKEN` set, a missing or wrong token is 401 on every `/v1` path; on a non-loopback address without a token, startup fails.
- [ ] Replay e2e (the built binary, like T024/T003): `agentkit serve` with `agent` + `mcp-server`, a prompt that needs a tool, against recorded Claude responses; `/readyz` includes the agent component.
- [ ] `-tags live`: the same against the real API.
- [ ] Mutation list recorded (at least: events out of order, a missing terminal event, conversation history not passed on, disconnect not cancelling, token check skipped on one path, the concurrency limit ignored).

## Notes

- archtest: the component may live in `cmd/agentkit` or a new `pkg/agentapi`; if a package, it imports `harness`, `llm`, `tool`, `toolset`, `component`, never an adapter (the adapter is wired in `cmd/agentkit`).
