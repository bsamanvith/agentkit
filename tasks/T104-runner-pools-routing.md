---
id: T104
title: "Runner pools: the placement, pkg/runner/pool, the process pool, the agent API on a router"
epic: E20
status: done
depends_on: [T030, T037]
---

# T104: runner pools and routing

## Context

The owner wants several runner implementations, as pools ([E20](E20-runner-pools.md)). This task adds the seam and the routing, with today's runner as the only implementation, so later pools (T105 to T107) are new `Pool`s and config, not changes to the runner.

## Scope

- **`runner.Placement`:** what the runner knows about a run when it asks for its agent: conversation, run ID (empty for a new chat run, whose agent is asked for before the run exists), interactive or background, start or resume. `AgentFactory` takes it instead of a bare conversation ID.
- **`pkg/runner/pool`:**
  - `Isolation` (`Process` < `Sandbox` < `VM`), parsed from and printed as `process`, `sandbox`, `vm`;
  - `Request`: the placement plus the agent's name, the isolation it needs and the agent as the server built it (`Built`: its harness, or its remote runner over `runnerrpc`);
  - `Pool` interface: `Name`, `Isolation`, `Agent(ctx, Request)`, `Close`;
  - `NewProcess(name)`: runs `Request.Built`, the current runner;
  - `Owned`: the `io.Closer`s a pool made, closed by the pool's `Close` (review item D);
  - `Rule` and matches (`Chats`, `Background`, `Agents`, `Needs`, `All`);
  - `Router`: rules in order, then the default pool; the first matching pool whose isolation is at least the request's; `ErrNoPool` otherwise. `Default()` is one `process` pool named `default`. `Router.Close` closes every pool.
- **Agent API:** `agentapi.WithRouter`; the router's factory is the agent factory of the default runner and of a `WithRuns` orchestrator alike. `Agent.Isolation` says what an agent needs (zero: `process`). Without a router the server uses `pool.Default()`, so nothing changes without pool config. The server owns its router and closes it at the end of `Shutdown`, after its runs have stopped.

## Out of scope

- Capacity per pool, lanes, serve config, `isolation:` in agent definitions, the pool on stored runs: T105.
- Sandbox and VM pools: T106, T107 (and E20's open owner questions).
- Routing flow runs' agent steps.

## Acceptance criteria

- [x] The runner gives its factory the placement of each start and resume: interactive and background, run ID on resume, recovery included — test: `runner.TestFactoryGetsPlacement`
- [x] Rules route chats and background runs to their pools; the default takes the rest — test: `pool.TestRouterRoutesByRule`
- [x] A run is never placed in a pool less isolated than its agent needs; a rule whose pool is too weak is passed over; nothing strong enough gives `ErrNoPool` — test: `pool.TestRouterNeverDowngrades`
- [x] A router with unknown pool names, duplicate pools, or no default is refused — test: `pool.TestNewRouterRejects`
- [x] The process pool runs the built agent and refuses a request without one — test: `pool.TestProcessRunsTheBuiltAgent`
- [x] Closing the router closes each pool and the agents they made that are still open, once; released agents are left alone; nothing is handed out after Close; an agent made after Close is closed at once — tests: `pool.TestRouterCloseClosesPoolAgents`, `pool.TestOwnedClosesLateAdds`, `pool.TestProcessClose`
- [x] The agent API runs a chat in the pool its router picks, with its placement, and fails the start (409) with no pool strong enough — test: `agentapi.TestRunsRouteThroughPools`
- [x] The agent API routes a `WithRuns` orchestrator's runs too, and its `Shutdown` closes the router and the agents its pools made — test: `agentapi.TestShutdownClosesPools`

## Notes

- Ported 2026-10-21 from draft PR #20 (branch `claude/project-thread-fqc9um`, 99bd4c8, left as it was) onto main after the T102 split, T031, T040 and T096. What changed in the port:
  - `Request.Local` is now `Request.Built`: since T102 an agent the server built is its harness or a remote runner (`agentapi.Agent.Runner`), and the process pool runs either. `Isolation` `process` means "shares a process with other agents", the server's or a runner component's.
  - The router replaces the server's `agentFor` and is handed to `WithRuns` orchestrators as their `runner.AgentFactory`, so every chat run is routed whichever orchestrator runs it.
  - Review item D: `Pool.Close`, `pool.Owned`, `Router.Close`, `ErrClosed` (matches `runner.ErrShutdown`, so a start after shutdown is a 503 like the runner's), and the server closing its router in `Shutdown`.
  - A pool that wraps the built agent must keep `runner.Compactor` if it wants the runner to compact (T031 checks the agent's type); the process pool returns the built agent itself, so compaction is unchanged.
- Decisions: the server owns the router it is given (closes it in `Shutdown`, after `Runs.Shutdown` and the wait for active runs, also when that wait timed out: the runs were told to stop). `Router.Close` is idempotent and returns the first result. No startup check that each agent's isolation is servable: rules can depend on chat vs background, so it is a run-time `ErrNoPool` (409, `invalid_request`); T105's config can check its own at startup.
- No serve wiring: `agentkit serve` has no pool config until T105, and runs with `pool.Default()`. No new dependency; `cmd/agentkit` (stripped, `CGO_ENABLED=0`) grows by 20 KB (29,143,200 → 29,163,680 bytes), under the 100 KB that needs a docs/SIZES.md row.
- Mutation list (each planted bug made the named test fail):
  - Route ignores a rule pool's isolation → `TestRouterNeverDowngrades`.
  - Route ignores rules' matches (every rule matches) → `TestRouterRoutesByRule`.
  - `Router.Close` does not close its pools → `TestRouterCloseClosesPoolAgents`.
  - `Owned`'s release does not forget the agent (closed twice) → `TestRouterCloseClosesPoolAgents`.
  - `Owned.Add` after Close does not close the late agent → `TestOwnedClosesLateAdds`.
  - A closed router still hands out agents → `TestRouterCloseClosesPoolAgents`.
  - A closed process pool still hands out agents → `TestProcessClose`.
  - `Server.Shutdown` does not close the router → `TestShutdownClosesPools`.
  - The server's request drops the agent's isolation → `TestRunsRouteThroughPools`.
  - `WithRuns` gets an unrouted factory → `TestShutdownClosesPools`.
  - Resume gives the factory a bare placement → `TestFactoryGetsPlacement`.

## For T105

- Config: parse `isolation:` with `pool.ParseIsolation`; build pools and rules into a `pool.Router` and pass it with `agentapi.WithRouter`. The recovery loop uses `Server.Runner()`, whose factory is already the router's.
- The pool a run went to is `Router.Route(req).Name()`; storing it with the run needs the run ID, which a new chat run does not have when its agent is asked for (`Placement.RunID` is empty): record it after `Runs.Start` returns, or give the factory a way to report it.

## For T106 / T107

- A pool that makes agents adds each one (or its container/VM) to a `pool.Owned` and closes it in `Close`; a per-run agent that tears its environment down at the end of its run calls the release `Owned.Add` returned. After `Close`, `Owned.Add` closes what was added late and returns `ErrClosed`.
- A VM pool keyed by session uses `Request.ConversationID`; `Placement.Resume` and `RunID` say whether the run already exists (a resumed run may find its VM warm).
