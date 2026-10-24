---
id: T123
title: "Memory for agents on the split runner, over pkg/rpc"
epic: E23
status: done
depends_on: [T102, T122]
---

# T123: memory on the runner

## Context

The runner component refused an agent with a `memory` section ("the tenant
does not cross to the runner yet"). The caller's tenant and user now cross
`/rpc/` (runnerrpc's `Caller`, toolrpc's), but the runner image has no
database (`nopostgres`, `noagent`), and should not get one for memory.

## Scope

- The agent API opens the memory of each agent it sends to a runner and
  serves its memory tools at `/rpc/memory/`: toolrpc's protocol under its own
  service name (`toolrpc.RegisterAs`, `NewClientAs`), one registry per agent,
  behind the RPC token. Tool errors and texts cross as they are (limit, not
  text, not found), and the caller's tenant and user with each call.
- The runner builds each such agent's memory tools as a client of
  `$AGENTKIT_MEMORY_URL`. Only the memory tools' names go there; the agent's
  other tools never wait on the API. Without the URL it refuses the agent,
  naming the variable.
- Auto recall (it would run on the runner) is refused on both sides, with
  what to do; the recall tools work.
- Compose and Kubernetes set `AGENTKIT_MEMORY_URL` on the runner.

## Out of scope

- Auto recall on a runner.
- A durable memory store in the base Kubernetes deployment (its store is in
  memory); the local overlay and Compose run on Postgres, where
  `memory.store.backend: "postgres"` keeps memory (T122).

## Acceptance criteria

- [x] Two services on one RPC server stay apart. — test: `toolrpc.TestServicesApart`; the remote registry already passes `tooltest.RunRegistry`
- [x] An agent on a runner (API and runner over HTTP with the token, fake model) saves a memory in one run and reads it in a new conversation; two users stay apart; the limit's text is the one-process text; only the 4 memory calls reach the API (an echo call does not); `/rpc/memory/` without the token is 401. — test: `TestSplitMemory`
- [x] Without `$AGENTKIT_MEMORY_URL` the runner refuses an agent with memory and names the variable; auto recall is refused by the runner and the API. — test: `TestRunnerMemoryNeedsURL`
- [x] The runner's copy of the memory tool names is pkg/memory's. — test: `TestMemoryToolNames`
- [x] Size measured (`go build -trimpath -ldflags='-s -w'`, each image's tags): runner +12 KB (14,717,088 to 14,729,376 bytes), api +16 KB. No SIZES.md row (under 100 KB).

## Notes

- Mutation check: sending every tool name to the API (dropping `namedTools`'s
  filter) fails `TestSplitMemory` (5 calls reach the API, not 4).
- The smoke suite runs the deploy config, whose agents have no memory; the
  split path is covered in process over real HTTP by `TestSplitMemory`.
