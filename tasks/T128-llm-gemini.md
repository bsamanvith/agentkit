---
id: T128
title: "pkg/llm/gemini: Gemini adapter"
epic: E08
status: done
depends_on: [T125]
---

# T128: Gemini adapter

## Context

The owner will add Gemini soon (2026-10-24). With T125 a provider is one package under `pkg/llm`, one builder file and one row in `modelProviders`.

## Scope

- `pkg/llm/gemini` over the Gemini REST API with plain `net/http`, passing `llmtest.RunModel` on recorded responses; known model ids for `GET /v1/models`; its own build tag, like `nomodels`.
- Size measured and logged in docs/SIZES.md.

## Acceptance criteria

- [x] `llmtest.RunModel` passes on recorded fixtures; live test behind `GEMINI_API_KEY`.
- [x] An agent allowing `gemini/*` runs on it from the console.

## Notes

- Done 2026-10-25. The fixtures are written by hand from recorded replies, as
  the suite needs (`llmtest.Scenario`); `llmtest.WithoutCacheWrites` because
  Gemini reports none. Thought signatures ride in opaque blocks
  (`pkg/llm/gemini` doc). The SSE reader and retries moved to
  `pkg/llm/internal/llmhttp`, shared with `openaicompat`.
- Live (2026-10-25, the owner's free-tier key): `TestLive*` pass on
  `gemini-3.5-flash` and `gemini-3.5-flash-lite`; `gemini-3.8-flash` answered
  503 (high demand) most of the morning, and `gemini-3.1-pro-preview` has no
  free-tier quota (429). A served agent ran parallel tool calls on
  `gemini/gemini-3.5-flash` from the console.
- `make test-live` and the local deploy scripts take `GEMINI_API_KEY` or
  `GEMINI_KEY` (the cloud environment's name).
