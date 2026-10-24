---
id: T128
title: "pkg/llm/gemini: Gemini adapter"
epic: E08
status: todo
depends_on: [T125]
---

# T128: Gemini adapter

## Context

The owner will add Gemini soon (2026-10-24). With T125 a provider is one package under `pkg/llm`, one builder file and one row in `modelProviders`.

## Scope

- `pkg/llm/gemini` over the Gemini REST API with plain `net/http`, passing `llmtest.RunModel` on recorded responses; known model ids for `GET /v1/models`; its own build tag, like `nomodels`.
- Size measured and logged in docs/SIZES.md.

## Acceptance criteria

- [ ] `llmtest.RunModel` passes on recorded fixtures; live test behind `GEMINI_API_KEY`.
- [ ] An agent allowing `gemini/*` runs on it from the console.

## Notes

- Waits for a Gemini key.
