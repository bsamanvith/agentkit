---
id: E20
title: "Runner pools: chat, background, sandboxed and per-session VM runners behind one interface"
status: in-progress
---

# E20: runner pools

Owner request (2026-10-16): several implementations of the agent runner, organised as **pools**. Today's in-process runner stays the default. Later:

1. a pool for **chats** (interactive runs, a user waiting);
2. a pool for **background runs** (tasks, jobs, webhooks, recovered runs);
3. a pool of **isolated VMs, one per session**, for agents that must not share a kernel with anything else;
4. a **middle-ground sandbox** pool (gVisor or similar): a user-space kernel per run, cheaper than a VM, stronger than a process.

## What a pool is

`pkg/runner` keeps a run's persistence: the conversation log, the run record, its lease, recovery and retries. None of that moves. What a pool changes is **where the agent's turn loop runs**: the harness, its model calls and its tool calls. That is already one seam, [`runner.Agent`](../pkg/runner/runner.go): the runner asks its `AgentFactory` for the agent of a run and streams that agent's harness events into the store.

So a pool is a named source of `runner.Agent`s with an **isolation level**:

| Isolation | Where the agent runs | Pool kinds |
|---|---|---|
| `process` | As the server built the agent, in a process shared with other agents: its harness in the server's process, or a runner component it reaches over `pkg/runner/runnerrpc` (T102) | default, chat, background |
| `sandbox` | One sandboxed container per run (gVisor `runsc` or similar), running `agentkit job run` on the E16 runtime contract | sandbox |
| `vm` | One microVM per session (conversation), kept warm across its runs, reclaimed when idle | vm |

A sandbox or VM agent is a thin `runner.Agent` that sends the harness input into the environment and streams harness events back. The runner cannot tell it from the in-process harness, so leases, recovery, flushing, interrupts and the stream API work unchanged.

## Routing

`pkg/runner/pool` routes each run to a pool (T104):

- The runner tells its factory what it knows about the run: a [`runner.Placement`](../pkg/runner/runner.go) (conversation, run ID when it exists, interactive or background, start or resume).
- The server adds the agent's name, the **least isolation the agent needs** (`agentapi.Agent.Isolation`), and the agent as built (`Request.Built`: its harness, or its remote runner), and asks the router. Every chat run goes through it, whether the default runner or a `WithRuns` orchestrator runs it.
- The router tries its rules in order (match chats, background runs, agent names, required isolation), then the default pool, and takes the **first matching pool whose isolation is at least what the agent needs**. It never downgrades: an agent that needs `vm` with no `vm` pool fails to start (`ErrNoPool`) instead of running in a process.
- With nothing configured there is one `process` pool named `default`: today's behaviour.

## Decisions so far

| Question | Decision |
|---|---|
| What moves into the sandbox or VM | The agent's turn loop: harness, model calls, tool calls. The runner, the store and the stream API stay in the server. |
| Interface | `runner.Agent` is the runner implementation's interface; no second runner. Pools choose the implementation per run. |
| Default | One `process` pool, `default`. Chat and background pools are both `process` pools until they differ in capacity (T105). |
| Never downgrade | A run is placed only in a pool at least as isolated as its agent requires. |
| Flows | A flow run's agent steps run with the flow run, not routed like chats (T105): a flow is one run, under one lease, and its steps take no slots of their own (a flow waiting on a full chat pool for a step would hold its own slot meanwhile). Flow runs themselves are in no pool yet (`pool` empty); when something starts them over the API, they are placed as background runs, in their pool's lane. |
| Capacity | Per pool (T105): `pool.WithLimit` gives a pool `Slots`. A chat takes one at the request (429 `over_capacity` when full); a background pool's dispatcher (`runner.WithSlots`) claims only while it holds one, so its runs wait in the store. A pool is a lane: a background run is queued in its pool's lane and stores its pool, and goes back to it on every resume. |
| Contract for remote runners | E16's runtime contract (T079): job spec in, run events out, tools built in or over MCP back to the server; or `pkg/runner/runnerrpc` (T102) to a runner component in the environment, which already streams harness events and errors on the wire. The sandbox and VM pools pick one in T106. |
| Lifecycle | A pool owns the agents it makes: `Pool.Close` closes those still open (`pool.Owned` keeps them), `Router.Close` closes every pool, and the agent API's `Shutdown` closes its router after its runs stop. A process pool makes nothing and closes nothing. After Close, asking for an agent fails with `pool.ErrClosed` (which matches `runner.ErrShutdown`). |

## Open questions (for the owner, before T106/T107)

- **Model credentials in a sandbox or VM:** pass a short-lived token for a model proxy in the server (keys never enter the environment), or the provider key itself? Proposal: the proxy.
- **Tools that touch the server's data** (memory, files, connectors with credentials): run in the environment with a scoped job token, or call back to the server over MCP? Proposal: built-in pure tools (math, files in the session's workspace) run inside; everything with credentials goes over MCP back to the server.
- **VM provider:** Firecracker on our own hosts, or a sandbox service (E2B, Modal, Fly Machines, Cloud Run with gVisor)? Proposal: one local provider first (Firecracker or plain `runsc` containers on the host for development), a hosted one after.
- **Per session** means per conversation? Proposal: yes; a VM is bound to one conversation and reused by its runs until idle for N minutes.

## Tasks

- [x] [T104](T104-runner-pools-routing.md) Pool interface and routing: `runner.Placement`, `pkg/runner/pool`, the `process` pool, the agent API on a router, the pools' Close
- [x] [T105](T105-pool-capacity-config.md) Pool capacity and config: chat and background pools with their own limits, background pools claim by lane (T099), `runners:` in the serve config, `isolation:` on agents, the pool on each run
- [ ] [T106](T106-sandbox-pool.md) Sandbox pool: one gVisor (or similar) container per run on the E16 runtime contract
- [ ] [T107](T107-vm-pool.md) VM pool: one microVM per session, warm across its runs, reclaimed when idle

Related: [E16](E16-runtimes-jobs.md) (runtime contract T079, exported images T080), [T099](T099-run-queue-order.md) (lanes and `ClaimNext`), [T071](T071-background-runs.md) (background runs).
