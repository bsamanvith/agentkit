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

- Runs and conversations go through the runner and a store chosen by config. The tenant comes from the auth middleware (the static token maps to one tenant until E07).
- **Store by config:** `AGENTKIT_STORE` selects the backend:
  - `memory` is the default, so the quickstart is unchanged;
  - `sqlite:/path/agentkit.db` selects SQLite (T029);
  - `postgres://…` selects Postgres (T088). Whichever of T037 and T088 lands second wires it.
  - An unknown scheme fails at startup.
  - Read T029's "For T037" and T030's notes first.
- **Image size:** linking the SQLite driver takes the image from about 17.4 to 21 MB. The budget is now 30 MB (`IMAGE_BUDGET_MB`, planner 2026-10-12), with every driver in the one default binary and no build tags. Record the new size in the Notes.
- New endpoints: `GET /v1/runs?conversation_id=&status=` (paged), `POST /v1/runs/{id}/resume`, `POST /v1/runs/{id}/decisions` (approve or reject a pending step), `POST /v1/runs/{id}/cancel`, `GET /v1/runs/{id}/events` (replay a run's stored events, then follow it live if still running).
- `POST /v1/runs` on a conversation with an unfinished run → 409 with the run ID (`ErrRunUnfinished`).
- The runner's recovery loop runs inside `agentkit serve`.
- **Conversations without messages (T092):** `POST /v1/conversations` creates an empty conversation (id, tenant, agent, created, empty title until its first run). `store.ConversationStore` only knows a conversation once it has an entry, so the store needs a conversation record of its own (id, tenant, agent, title, created/updated) for creation, listing and the agent check; keep both creation paths (explicit, and a run without `conversation_id`).

## Acceptance criteria

- [ ] The T036 acceptance tests pass unchanged against v1 (same API).
- [ ] Kill the server mid-run (replay e2e, separate process): after restart the run is recovered and finishes; `GET /v1/runs/{id}/events` shows the whole run.
- [ ] Approve and reject through the API on a flow with `Approve`; cancel; 409 on an unfinished conversation.
- [ ] `AGENTKIT_STORE` selects memory or SQLite (and Postgres once T088 is on `main`); an unknown scheme fails startup; the T036 acceptance tests pass on each.
- [ ] Mutation list recorded.
