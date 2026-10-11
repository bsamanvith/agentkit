---
id: E04
title: llm, Anthropic adapter, harness and flow (M3)
status: in-progress
---

# E04: `llm`, Anthropic adapter, harness and `flow`

The core of the product: a provider-neutral model interface, Claude through the official Go SDK, our own agent loop, and multi-step flows with checkpoints. Decisions and reasons: [PLAN.md decisions](../docs/PLAN.md#decisions) (own harness instead of ADK/Genkit/Eino; native adapters; `flow` instead of a graph DSL).

Everything up to T023 is tested with fakes (no API key, no containers). T024 is the first run against the real Claude API.

## Tasks

- [x] [T020](T020-llm-core.md) `pkg/llm`: model interface, message types, streaming events, `llmtest` fake and adapter conformance suite
- [ ] [T021](T021-llm-anthropic.md) `pkg/llm/anthropic`: adapter on the official Go SDK **← next**
- [ ] [T022](T022-harness-loop.md) `pkg/harness`: the agent loop (parallel tools, budgets, hooks, events) **← next**
- [x] [T023](T023-flow.md) `pkg/flow`: steps, combinators, checkpoints, resume, approval
- [ ] [T024](T024-run-cli.md) `agentkit run`: a real agent and a two-step flow against Claude, end to end
- [ ] [T025](T025-llm-openai-compatible.md) `pkg/llm/openaicompat`: OpenAI-compatible adapter (OpenAI, Ollama, vLLM, gateways)

```
T020 ─┬─ T021 ─────────┐
      ├─ T022 ─────────┼─ T024
      └─ T025          │
T023 (needs only E01) ─┘
```

T021, T022 and T023 can run in parallel once T020 is done (T023 even before). T018 (MCP server, E03) is independent of this epic.
