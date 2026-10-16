---
id: T105
title: "Pool capacity and config: chat and background pools with their own limits, lanes, serve config"
epic: E20
status: todo
depends_on: [T104, T099]
---

# T105: pool capacity and config

## Context

After T104 every run goes through a router, but two `process` pools behave the same: the only point of a separate chat pool and background pool today is that **background work can never take the slots chats need** ([E20](E20-runner-pools.md)).

## Scope

- **Capacity per pool:** a pool has a limit of runs going on. A chat pool admits at the request (full: `429`, as `WithMaxConcurrentRuns` does today for the whole server). A background pool never refuses: its runs wait in the store.
- **Background pools claim by lane:** a background pool is a lane of T099's dispatcher; it claims with `ClaimNext` only while it has a free slot. Recovered runs go back to their pool's lane.
- **Serve config:** a `runners:` section: pools (name, isolation, limit), routes (pool, `runs: chat|background`, agents, isolation), default. Only `process` pools until T106/T107; any other isolation fails startup naming the task.
- **Agents:** `isolation:` in an agent's definition (default `process`).
- **The pool on each run:** stored with the run (migration on every SQL backend), shown by `GET /v1/runs/{id}`, logged, and counted in metrics per pool.
- **Flows:** decide whether a flow run's agent steps route like chats, or with the flow run.

## Acceptance criteria

- [ ] A full background pool never delays a chat: chats start while background runs wait.
- [ ] A background run waits in the store until its pool has a slot, then starts (fake clock).
- [ ] The serve config routes runs as written; an unknown pool or isolation fails startup.
- [ ] A run's pool is stored and returned by the API.
- [ ] Mutation list recorded.
