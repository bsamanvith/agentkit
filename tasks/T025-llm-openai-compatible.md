---
id: T025
title: "pkg/llm/openaicompat: OpenAI-compatible adapter"
epic: E04
status: todo
depends_on: [T020]
---

# T025: `pkg/llm/openaicompat`

## Context

One adapter for every OpenAI-compatible endpoint: OpenAI, Grok, Groq, vLLM, Ollama, and gateways such as the LiteLLM proxy or OpenRouter ([PLAN.md decisions](../docs/PLAN.md#decisions): gateways are supported, never required). Can slip to a later epic if not needed for M3.

## Scope

- Adapter on the official OpenAI Go SDK, with base URL and model configurable.
- Mapping for messages, tools, tool results, streaming, stop reasons and usage. `llm.Opaque` blocks from other providers are dropped when sending.

## Acceptance criteria

- [ ] Passes `llmtest.RunModel` against a replay server with recorded responses.
- [ ] The request body for a representative request matches a golden file.
- [ ] `-tags live`: one request with a tool call completes against a configured endpoint (Ollama locally, or OpenAI with a key); skips without configuration.

## Notes

- archtest allowlist: `pkg/llm/openaicompat` → the OpenAI Go SDK.
