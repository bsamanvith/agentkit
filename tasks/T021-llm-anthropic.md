---
id: T021
title: "pkg/llm/anthropic: adapter on the official Go SDK"
epic: E04
status: todo
depends_on: [T020]
---

# T021: `pkg/llm/anthropic`

## Context

Claude is the first provider, through the official `github.com/anthropics/anthropic-sdk-go`, with no translation layer in between, so Claude-specific features keep working ([PLAN.md `llm`](../docs/PLAN.md#llm)).

## Scope

- `anthropic.New(opts...)` implementing `llm.Model`; the client comes from the SDK (API key or the SDK's other credential sources), base URL configurable for tests.
- Default model `claude-opus-5-5`.
- Mapping both ways: messages, tool definitions from `tool.Definition`, tool results (`is_error` for failures), streaming events, stop reasons (including `refusal` with its category), usage including cache tokens.
- Thinking blocks: carried as `llm.Opaque` and sent back unchanged. History is never edited.
- `anthropic.Options`: effort, prompt-cache breakpoints, server-side refusal fallback (on by default, can be turned off), extra beta headers.
- Error mapping to `llm` sentinels; the SDK's own retries stay on.
- Notes for current Claude models: thinking cannot be disabled on Opus 5.5 (effort is the control); forced `tool_choice` returns a 400, so the adapter only uses `auto`.

## Acceptance criteria

- [ ] Passes `llmtest.RunModel` against a local replay server serving recorded API responses (no network in unit tests).
- [ ] The request body for a representative request (system, history with tool use and results, tools, options) matches a golden file.
- [ ] A recorded response with thinking blocks round-trips: the next request contains them byte-identical.
- [ ] A refusal response gives `StopReason` `Refusal` with its category.
- [ ] 429, 529, 400 and 401 responses map to `ErrRateLimited`, `ErrOverloaded`, `ErrInvalidRequest`, `ErrAuth`.
- [ ] `-tags live`: one real streaming request with one tool call completes against the API (skips without credentials).

## Notes

- archtest allowlist: `pkg/llm/anthropic` → `github.com/anthropics/anthropic-sdk-go/...`.
- Record fixtures once with a live run and keep them in `testdata/`; re-record when the API changes.
