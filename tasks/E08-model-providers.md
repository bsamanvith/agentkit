---
id: E08
title: More model providers
status: todo
---

# E08: More model providers

Native adapters beyond Anthropic, each passing `llmtest.RunModel` against recorded responses ([PLAN.md `llm`](../docs/PLAN.md#llm)). Also keeps existing adapters honest (recorded fixtures). New providers start when a deployment needs one.

- [ ] [T025](T025-llm-openai-compatible.md) `pkg/llm/openaicompat`: OpenAI-compatible adapter (OpenAI, Grok, Groq, vLLM, Ollama, and gateways such as the LiteLLM proxy)
- [x] [T035](T035-rerecord-anthropic-fixtures.md) Re-record the Anthropic adapter fixtures from the real API (do now: a key is available)
- [x] [T128](T128-llm-gemini.md) `pkg/llm/gemini`: Gemini adapter (owner, 2026-10-24: "soon"), after T125
- Later, unplanned: Bedrock.
