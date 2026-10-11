---
id: T037
title: "agent component v1: on the runner (persist, resume, approve, cancel, list)"
epic: E09
status: todo
depends_on: [T036, T030]
---

# T037: Agent API v1

## Context

Swap T036's in-memory runs for `pkg/runner` (T030), keeping the API, so runs survive restarts and the run lifecycle (E05 decisions) is reachable over HTTP.

## Scope

- Runs and conversations through the runner and the SQLite store (T029); tenant from the auth middleware (static token maps to one tenant until E07).
- New endpoints: `GET /v1/runs?conversation_id=&status=` (paged), `POST /v1/runs/{id}/resume`, `POST /v1/runs/{id}/decisions` (approve or reject a pending step), `POST /v1/runs/{id}/cancel`, `GET /v1/runs/{id}/events` (replay a run's stored events, then follow it live if still running).
- `POST /v1/runs` on a conversation with an unfinished run → 409 with the run ID (`ErrRunUnfinished`).
- The runner's recovery loop runs inside `agentkit serve`.

## Acceptance criteria

- [ ] The T036 acceptance tests pass unchanged against v1 (same API).
- [ ] Kill the server mid-run (replay e2e, separate process): after restart the run is recovered and finishes; `GET /v1/runs/{id}/events` shows the whole run.
- [ ] Approve and reject through the API on a flow with `Approve`; cancel; 409 on an unfinished conversation.
- [ ] Mutation list recorded.
