---
id: T106
title: "Sandbox pool: one gVisor (or similar) container per run on the runtime contract"
epic: E20
status: todo
depends_on: [T104, T079]
---

# T106: sandbox pool

## Context

The middle-ground isolation of [E20](E20-runner-pools.md): a user-space kernel (gVisor's `runsc`) between the agent and the host, cheaper to start than a VM. Needs E16's runtime contract (T079) and an image to run (T080).

## Scope (to plan in detail)

- A `pool.Pool` with isolation `sandbox` whose `Agent` starts one container per run under `runsc`, runs `agentkit job run` on the job spec, and turns the run events it sends back into harness events for the runner.
- Model calls through the server's model proxy with a short-lived job token (owner to confirm, E20 open questions).
- Tools: pure built-ins inside; anything with credentials over MCP back to the server.
- Interrupt and shutdown stop the container; a lost container fails the run retryably.
- Limits: CPU, memory, wall clock, no network but the server.

## Acceptance criteria

- [ ] A run of an agent that needs `sandbox` runs in a `runsc` container and its events reach the run's stream (integration tier, where `runsc` is installed).
- [ ] Killing the container mid-run leaves the run resumable, and resuming it finishes it.
- [ ] The sandbox cannot reach anything but the server (network test).
- [ ] Mutation list recorded.
