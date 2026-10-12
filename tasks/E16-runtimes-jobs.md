---
id: E16
title: "Runtimes and jobs: temporary environments, exported harness images, results outside chat"
status: todo
---

# E16: runtimes and jobs

Owner ideas (2026-10-12, [docs/PRODUCT.md](../docs/PRODUCT.md)): run an agent in a **temporary environment** and keep its output and history in a separate **jobs** collection (not chat), and export the **harness with tools and skills baked into an image** for ephemeral VMs. The harness inside is **pluggable** (our Go binary first, the pi harness second). Deep dives come later; this epic fixes the shape.

## Decisions so far

| Question | Decision |
|---|---|
| Tasks (formerly "jobs") | **Owner's name: _task_**, shared with E14's background runs. A task here is a run that is not part of a chat: input (message, data, files), the agent version, the runtime, events, the final output and the message history. Stored in its own collection, listed and shown by the console with the same run-viewer components (agent-ui). Started by the API, a trigger (E14) or another system. |
| Runtime contract | One contract for any harness in any environment: **in**: a job spec (agent definition export, input, limits, callback URL and a short-lived job token); **tools**: built in (baked toolsets) or reached over MCP back to the server; **out**: our run events posted to the server as they happen (or a JSONL file collected at the end), then the result. Our Go binary implements it first (`agentkit job run --spec`); a pi adapter implements the same contract second. |
| Environments | (1) a local process or container on the server's host (first, for development and tests); (2) ephemeral VMs or a sandbox service (later, provider chosen in a deep dive). |
| Exported images | `agentkit export --agent X --image` builds an OCI image: the binary, the agent's published definition, its skills and toolsets (credentials never baked in; passed at start or fetched with the job token). |

## Tasks (to plan in detail after the console's first version)

- [ ] T078 Jobs: the collection, API (`/v1/jobs`), job token, events and results, console list and viewer *(to plan)*
- [ ] T079 Runtime contract and `agentkit job run` (Go harness), local container runner *(to plan)*
- [ ] T080 Exported harness images (`agentkit export --image`) *(to plan)*
- [ ] T081 pi harness adapter for the runtime contract *(to plan)*
