---
id: T020
title: "pkg/llm: model interface, messages, streaming, llmtest"
epic: E04
status: todo
depends_on: [T017]
---

# T020: `pkg/llm`

## Context

The harness talks to every model through this package and never sees provider types ([PLAN.md `llm`](../docs/PLAN.md#llm)). It has to carry the common ground (text, tool calls, tool results, streaming, usage, stop reasons) without losing provider-specific content such as Claude's thinking blocks, which must be sent back byte-for-byte unchanged.

## Design

- **Messages**: `Message{Role, Content []Block}` with roles `user` and `assistant`. `Block` is a sealed interface with concrete types:
  - `Text`
  - `ToolUse{ID, Name, Input json.RawMessage}`
  - `ToolResult{ToolUseID, Content, IsError}`
  - `Opaque{Provider string, Data json.RawMessage}`: a provider block we do not interpret (thinking with signatures, redacted thinking, compaction). Adapters must return it unchanged on the next request to the same provider, and drop it when talking to another provider.
- **Request**: system prompt, messages, tools (`[]tool.Definition` from `pkg/tool`, no new tool type), max output tokens, and `Provider any` for typed, provider-scoped options (for example `anthropic.Options`). An adapter ignores options that are not its own type.
- **Model interface**: `Generate(ctx, Request) iter.Seq2[Event, error]`. Events: text delta, tool-use start, tool-input delta, block done, message done (final `Message`, `StopReason`, `Usage`). `llm.Collect` turns the stream into a `Response` for callers that do not stream.
- **StopReason**: `EndTurn`, `ToolUse`, `MaxTokens`, `Refusal` (with category and explanation), `Other` (raw value kept).
- **Usage**: input, output, cache-read and cache-write tokens.
- **Errors**: sentinels `ErrRateLimited`, `ErrOverloaded`, `ErrInvalidRequest`, `ErrAuth`, `ErrContextTooLong`, and `IsRetryable(err)`. Adapters wrap provider errors so `errors.Is` works and the original stays reachable with `errors.As`.
- **`pkg/llm/llmtest`**:
  - `Fake`: a scripted model. Replies are given in order (or by matcher), every request is recorded, and streaming is emitted event by event. The harness and flow tests use it.
  - `RunModel(t, newModel)`: conformance suite every adapter runs against its replay server: plain text, a tool-call round trip, event order, `Opaque` blocks preserved, cancellation, error mapping.

## Out of scope

- Images and documents in messages (add when the first real use needs them).
- Any provider adapter (T021, T025).

## Acceptance criteria

- [ ] `Collect` over a stream of text and tool-use events gives the same `Response` as a non-streaming reply. — `TestCollect`
- [ ] `Collect` returns the stream's error and no partial response when the stream fails midway. — `TestCollectStreamError`
- [ ] Messages with every block type round-trip through JSON unchanged (checkpoints depend on it); `Opaque.Data` is byte-identical. — `TestMessageJSONRoundTrip` (golden file)
- [ ] An unknown block type in JSON is an error, not silently dropped. — `TestUnknownBlockType`
- [ ] `IsRetryable` is true for rate-limit and overload errors (including wrapped ones) and false for the rest. — `TestIsRetryable`
- [ ] `llmtest.Fake` returns scripted replies in order, records each request, streams them as events, and fails the test clearly when it runs out of replies. — `TestFake`
- [ ] `llmtest.Fake` passes `llmtest.RunModel`. — `TestFakeConformance`
- [ ] `Fake` respects cancellation: a cancelled context ends the stream with the context error. — covered by `RunModel`

## Notes

- archtest: `pkg/llm` may import `pkg/tool`; no third-party dependencies.
