---
id: T132
title: "Stored agents on the split runner: the builder and model picker on k8s and Compose"
epic: E15
status: review
depends_on: [T075, T102, T125]
---

# T132: stored agents on the split runner

## Context

T075 kept stored agents (the console's agent builder, T077) only where the
agent API builds agents itself. In the split deployment (k8s, Compose) the
agent API runs its agents on the runner at `$AGENTKIT_RUNNER_URL`, and its
image has no model adapters (`nomodels`), so the registry was off there: no
builder, no stored agents, no model picker for them. Found by the first
`make k8s-up` run on the owner's laptop (bsamanvith/agentkit#114).
T075's follow-up: "Stored agents on a separate runner component".

## Scope

- `pkg/runner/runnerrpc`: `WithStored(StoredBuilder)` makes a runner build
  agents from definitions. `Client.StoredAgent`/`StoredModelAgent` send the
  definition with each run (method `RunStored`, newer than every other, so
  an older runner fails the run rather than run a config agent of the same
  name) and each compaction; `Client.CheckStored` builds one without
  running it (`CheckStored`). A refusal is a `*DefinitionError` with its
  fields across the wire; a runner without `WithStored` answers
  `ErrNoStored`.
- `cmd/agentkit`: the stored-agent checks and build move to `stored.go`
  (no build tag): `checkStored` (decode and every check that needs nothing
  built) and `buildStored`. The registry (`registry.go`, now `!noagent`
  only) builds locally when the models are built in, or, with a runner,
  checks a definition, has the runner build it, and runs it there. The
  runner registers `WithStored` (unless `agent_registry.enabled` is false)
  with an LRU of what it built (256, or `agent_registry.cache_size`), keyed
  by the definition's bytes, and gets every toolset of the file, as the
  agent API does with the registry on.
- Smoke suite: `TestBuilder` saves and publishes an agent through the API
  and runs it on its default model and on another it allows.

## Out of scope

- Runtime toolsets (T096) and MCP registry search on the split runner: the
  runner reads no toolsets added at run time yet, so a stored agent there
  cannot name `runtime_toolsets` (refused by field).
- Skills, memory, UI, `can_message` and webhooks of stored agents: refused
  as before (T075).

## Acceptance criteria

- [x] A stored agent's run carries its definition and runs the agent the
  runner builds from it, not a served agent of the same name — test:
  `runnerrpc.TestStoredAgentRuns`
- [x] A refused definition fails a check, a run and a compaction with the
  builder's fields, not retryable — test: `runnerrpc.TestStoredAgentRefused`
- [x] A runner without `WithStored` refuses stored agents (`ErrNoStored`) —
  test: `runnerrpc.TestNoStoredAgents`
- [x] A stored agent runs on another model the built agent allows — test:
  `runnerrpc.TestStoredAgentOnAModel`
- [x] A runner older than stored agents does not run one — test:
  `runnerrpc.TestOldRunnerRefusesAStoredAgent`
- [x] An agent API on a runner, building no model, refuses a definition by
  field (its checks' and the runner's) and saves nothing; a published one
  runs on the runner, built once, with a toolset no config agent names, and
  on each of its models — test: `cmd/agentkit.TestSplitStoredAgents`
- [x] The runner makes the agent API's checks whoever asks — test:
  `cmd/agentkit.TestRunnerChecksStoredAgents`
- [x] In a deployment, an agent saved through the API runs on its default
  and another model — test: `test/e2e.TestBuilder` (`make smoke`,
  `make cloud-k8s-smoke` on the real model)

## Notes

- Why the definition rides with each run instead of the runner reading the
  registry: the runner stays stateless (no store, no call back to the agent
  API), a definition never changes once saved, and the runner's LRU keyed by
  its bytes builds each version once. A definition is a few KB.
- Mutation self-check (each planted alone; the named test failed):
  1. The runner's LRU never hits: `TestSplitStoredAgents` (m4 built again).
  2. The remote builder skips `CheckStored`: `TestSplitStoredAgents` (tools
     clash and bad model saved).
