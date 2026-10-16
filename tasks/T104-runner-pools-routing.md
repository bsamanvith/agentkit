---
id: T104
title: "Runner pools: the placement, pkg/runner/pool, the process pool, the agent API on a router"
epic: E20
status: review
depends_on: [T030, T037]
---

# T104: runner pools and routing

## Context

The owner wants several runner implementations, as pools ([E20](E20-runner-pools.md)). This task adds the seam and the routing, with today's in-process runner as the only implementation, so later pools (T105 to T107) are new `Pool`s and config, not changes to the runner.

## Scope

- **`runner.Placement`:** what the runner knows about a run when it asks for its agent: conversation, run ID (empty for a new chat run, whose agent is asked for before the run exists), interactive or background, start or resume. `AgentFactory` takes it instead of a bare conversation ID.
- **`pkg/runner/pool`:**
  - `Isolation` (`Process` < `Sandbox` < `VM`), parsed from and printed as `process`, `sandbox`, `vm`;
  - `Request`: the placement plus the agent's name, the isolation it needs and its in-process harness (`Local`);
  - `Pool` interface: `Name`, `Isolation`, `Agent(ctx, Request)`;
  - `Process(name)`: runs `Request.Local`, the current runner;
  - `Rule` and matches (`Chats`, `Background`, `Agents`, `Needs`);
  - `Router`: rules in order, then the default pool; the first matching pool whose isolation is at least the request's; `ErrNoPool` otherwise. `Default()` is one `process` pool named `default`.
- **Agent API:** `agentapi.WithRouter`; the agent factory routes every chat run through it. `Agent.Isolation` says what an agent needs (zero: `process`). Without a router the server uses `pool.Default()`.

## Out of scope

- Capacity per pool, lanes, serve config, the pool on stored runs: T105.
- Sandbox and VM pools: T106, T107.
- Routing flow runs' agent steps.

## Acceptance criteria

- [x] The runner gives its factory the placement of each start and resume: interactive and background, run ID on resume — test: `runner.TestFactoryGetsPlacement`
- [x] Rules route chats and background runs to their pools; the default takes the rest — test: `pool.TestRouterRoutesByRule`
- [x] A run is never placed in a pool less isolated than its agent needs; a rule whose pool is too weak is passed over; nothing strong enough gives `ErrNoPool` — test: `pool.TestRouterNeverDowngrades`
- [x] A router with unknown pool names, duplicate pools, or no default is refused — test: `pool.TestNewRouterRejects`
- [x] The agent API runs a chat in the pool its router picks, and fails the start with no pool strong enough — test: `agentapi.TestRunsRouteThroughPools`

## Notes

- Mutation list: dropping the isolation check in `Router.Route` fails `TestRouterNeverDowngrades`; routing every run to the default fails `TestRouterRoutesByRule` and `TestRunsRouteThroughPools`; giving the factory an empty placement fails `TestFactoryGetsPlacement`.
