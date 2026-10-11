---
id: E04
title: llm, Anthropic adapter, harness and flow (M3)
status: done
---

# E04: `llm`, Anthropic adapter, harness and `flow`

The core of the product: a provider-neutral model interface, Claude through the official Go SDK, our own agent loop, and multi-step flows with checkpoints. Decisions and reasons: [PLAN.md decisions](../docs/PLAN.md#decisions) (own harness instead of ADK/Genkit/Eino; native adapters; `flow` instead of a graph DSL).

Everything up to T023 is tested with fakes (no API key, no containers). T024 is the first run against the real Claude API.

## Tasks

E04 is done (M3). T025, the OpenAI-compatible adapter, moved to [E08](E08-model-providers.md): it is not needed until a second provider is.

- [x] [T020](T020-llm-core.md) `pkg/llm`: model interface, message types, streaming events, `llmtest` fake and adapter conformance suite
- [x] [T021](T021-llm-anthropic.md) `pkg/llm/anthropic`: adapter on the official Go SDK (live test passed in T024)
- [x] [T022](T022-harness-loop.md) `pkg/harness`: the agent loop (parallel tools, budgets, hooks, events)
- [x] [T023](T023-flow.md) `pkg/flow`: steps, combinators, checkpoints, resume, approval
- [x] [T024](T024-run-cli.md) `agentkit run`: a real agent and a two-step flow against Claude, end to end (also completes T021's live test); `pkg/agentstep` runs an agent as a flow step

```
T020 ─┬─ T021 ─────────┐
      ├─ T022 ─────────┼─ T024
      └─ T025          │
T023 (needs only E01) ─┘
```

T021, T022 and T023 can run in parallel once T020 is done (T023 even before). T018 (MCP server, E03) is independent of this epic.

## Follow-ups, scheduled when a caller needs them

From the T020 and T022 handoffs. Each becomes its own task when its first real caller appears; until then the detail lives in the linked Notes.
- Harness ([T022 Deferred](T022-harness-loop.md)): compaction inside a run, a replaceable `Validate`, a between-turns hook, a "model call started" event, JSON wire forms for `Event` and `Record` (needed for the remote harness, M6), continuing after `pause_turn`, retries as an `llm.Model` wrapper.
- `pkg/llm`: promote `Message.ToolUses()` and `Usage.Add` once a second caller exists.
- `agentkit flow demo`: its `ErrShapeChanged` message mentions `flow.WithAllowShapeChange`, which the CLI cannot pass. **Decided:** no override flag on a demo; print a CLI hint instead ("the flow's steps changed since this run was checkpointed: start a new run, or finish it with the binary that started it"). Small; do after T003 lands (both touch `cmd/agentkit`).
- `agentkit flow demo` interleaves text when agents run in parallel. The demo is sequential; fix it with the first parallel demo (prefix lines by step path).
- `Validate` strictness: answered in T024. Claude accepts a user message mixing tool results and text; the harness keeps rejecting it **on purpose**. Allowing it belongs with the deferred replaceable `Validate` and the between-turns hook (reminder or compaction layers may want it).
- `pkg/clock` (T019) has landed: switching the harness from `WithNow` is scheduled as [T033](T033-harness-clock.md).
