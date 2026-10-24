---
id: T123
title: "Memory for agents on the split runner, over pkg/rpc"
epic: E23
status: todo
depends_on: [T102, T122]
---

# T123: memory on the runner

## Context

The runner component refuses an agent with a `memory` section ("the tenant
does not cross to the runner yet"). The caller's tenant and user now cross
`/rpc/` (runnerrpc's `Caller`), but the runner image has no database
(`nopostgres`, `noagent`), and should not get one for memory.

## Scope

- A `memory` RPC service (View, Create, StrReplace, Insert, Delete, Rename)
  served by the agent component next to the store, keyed by agent name, and a
  remote `memory.Store` client that carries the caller (`tenancy.Carry`).
- The runner builds the memory tool over the remote store for each agent
  with memory, from the URL in `$AGENTKIT_MEMORY_URL` (the agent API's) and
  the RPC token; without it, it keeps refusing such an agent, saying what to set.
- Errors cross with their kind, so the tool's texts (limit, not text, not
  found) are the same as in one process.
- The Compose and Kubernetes deployments set it.

## Acceptance criteria

- [ ] The remote store passes `memorytest.RunStore`. — test: `memoryrpc.TestConformance`
- [ ] A split deployment (smoke, fake model) keeps a memory in run 1 and reads it in run 2 of a new conversation, for two users apart. — test: `TestSplitMemory`
- [ ] Without `$AGENTKIT_MEMORY_URL` the runner refuses an agent with memory and names the variable. — test: `TestRunnerMemoryNeedsURL`
- [ ] The runner image's size is measured before and after (docs/SIZES.md if it moves 100 KB).
