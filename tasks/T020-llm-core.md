---
id: T020
title: "pkg/llm: model interface, messages, streaming, llmtest"
epic: E04
status: done
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

- [x] `Collect` over a stream of text and tool-use events gives the same `Response` as a non-streaming reply. — `TestCollect`
- [x] `Collect` returns the stream's error and no partial response when the stream fails midway. — `TestCollectStreamError`
- [x] Messages with every block type round-trip through JSON unchanged (checkpoints depend on it); `Opaque.Data` is byte-identical. — `TestMessageJSONRoundTrip` (golden file)
- [x] An unknown block type in JSON is an error, not silently dropped. — `TestUnknownBlockType`
- [x] `IsRetryable` is true for rate-limit and overload errors (including wrapped ones) and false for the rest. — `TestIsRetryable`
- [x] `llmtest.Fake` returns scripted replies in order, records each request, streams them as events, and fails the test clearly when it runs out of replies. — `TestFake`
- [x] `llmtest.Fake` passes `llmtest.RunModel`. — `TestFakeConformance`
- [x] `Fake` respects cancellation: a cancelled context ends the stream with the context error. — covered by `RunModel` (`cancel` and `cancelled_context` scenarios, run by `TestFakeConformance`)

## Notes

- archtest: `pkg/llm` imports `pkg/tool` only; no new allowlist entries. `pkg/tool` is unchanged.
- Extra tests beyond the criteria (each guards a real edge): `TestCollectIncompleteStream` (a stream with neither result nor error is `ErrIncompleteStream`), `TestMarshalRejectsInvalidMessages`, `TestToolUseInputIsSemanticallyPreserved`, and `FuzzMessageUnmarshal` (arbitrary bytes never panic; whatever decodes re-encodes and re-decodes identically).

### Decisions T021, T022 and T025 need

- **`Opaque.Data` is encoded as a JSON *string*** holding the exact bytes. Embedding it as a JSON value cannot be made exact: `encoding/json` compacts and HTML-escapes the output of any `MarshalJSON` (so a custom marshaler or `SetEscapeHTML(false)` inside it does not help), and `MarshalIndent` re-indents it. A string decodes back to the same bytes under every encoder. The cost is a less readable checkpoint. `Data` must be valid JSON in UTF-8, and `Provider` must be set; `Marshal` and `Unmarshal` both reject anything else with `ErrInvalidMessage`.
  - For T021: when the adapter puts `Opaque.Data` into the request body, `json.RawMessage` fields go through the same compaction/escaping. The API sees equal JSON (same decoded strings, so signatures verify), but if the golden request must show identical bytes, splice the raw bytes in yourself.
- **`ToolUse.Input` is only semantically preserved**: marshaling compacts it and writes `<`, `>`, `&` as `\u` escapes; decoding normalizes the same way, so decode→encode→decode is stable. A missing or null input becomes `{}`.
- **JSON is strict**: unknown roles, block types and fields are `ErrInvalidMessage`, so an older binary reading a newer checkpoint fails instead of dropping content. `llm.UnmarshalBlock` decodes a single block.
- **`MessageDone` is authoritative.** `Event.Response` holds the finished `Response`; `Collect` returns (a copy of) it and does not rebuild the message from deltas, so there is one source of truth. `RunModel` is what enforces that deltas, `BlockDone` blocks and the final message agree. An adapter must therefore assemble the final message itself.
- **Stream contract** (documented on `EventKind`, checked by `RunModel`): blocks are streamed one at a time, in order, never interleaved. Text block: `TextDelta*`, `BlockDone`. Tool use: `ToolUseStart` (a `ToolUse` with `ID` and `Name`, no input), `ToolInputDelta*`, `BlockDone`. Opaque: `BlockDone` only. Then exactly one `MessageDone`. Failure is a final `(Event{}, err)` with no `MessageDone`.
- **`Event` is a flat struct** (`Kind`, `Index`, `Text`, `Block`, `Response *Response`), 56 bytes, no allocation per delta. `Response` is a pointer so the rare final event does not enlarge every event.
- **`Usage.InputTokens` excludes cached tokens**; the whole prompt is `InputTokens + CacheReadTokens + CacheWriteTokens` (Anthropic's convention). The OpenAI-compatible adapter must subtract cached tokens from `prompt_tokens`.
- **`ToolResult.Content` is a `string`** (images and documents are out of scope); the harness turns tool output JSON into that string.
- **`Request.Provider`** is shared, not copied, by `Request.Clone`: treat the options as immutable once sent.
- **Errors**: besides the five sentinels from the design, `ErrInvalidMessage` (JSON) and `ErrIncompleteStream` (`Collect`). Adapters wrap with `fmt.Errorf("%w: %w", llm.ErrRateLimited, providerErr)`.
- **`RunModel(t, newModel func(t, Scenario) llm.Model, opts...)`**: the suite owns the scripted conversations (`Scenario{Name, Replies}`: `text`, `tool_call`, `opaque`, `cancel`, `cancelled_context`, five `error_*` and `error_mid_stream`). An adapter's factory serves each scenario's `Replies` from its replay server (fixtures named after `Name`) and the suite checks the neutral outcome. `WithOpaque` lets an adapter use a real thinking block of its own provider in the `opaque` scenario. The suite also fails a model that modifies its request. `Reply` has `Err`/`After` (fail after N events) and `Stall` (go quiet after the first event, for cancellation) so the fake and replay servers script the same failures.
- **`llmtest.Fake`** records and returns *deep copies* of requests (`Requests()`), consumes the reply at `Generate` call time, checks the context before every event, and on running out of replies calls `tb.Errorf` (safe from any goroutine) and yields `ErrNoReply`.

### Deferred

- Replies chosen by a matcher (design mentions it): the harness tests in T022 run one model call at a time, so ordered replies are enough for now. Add `Reply` matching when a flow test with parallel branches sharing one fake needs it.
- A retry-after hint on rate-limit errors, a shared stream-assembly helper for adapters, `Usage.Add`, `Message.ToolUses()`: add with the first caller (T021/T022).

### Mutation checks

Each bug was planted alone, `go test ./pkg/llm/...` was run, and the change was reverted. Every one makes a test fail (not a compile error):

| Planted bug | Caught by |
|---|---|
| `Collect` returns the partial response with the stream error | `TestCollectStreamError` |
| `Collect` drops tool-use blocks | `TestCollect` |
| Text deltas out of order (the fake streams them reversed; `Collect` itself does not assemble, see above) | `TestFakeConformance` (`text`, `tool_call`, `opaque`) |
| `Opaque` re-encodes its data via `map[string]any` | `TestMessageJSONRoundTrip`, `FuzzMessageUnmarshal` seeds |
| `Opaque.Data` embedded as a raw JSON value instead of a string | `TestMessageJSONRoundTrip`, `TestUnknownBlockType` |
| Unknown block type skipped instead of failing | `TestUnknownBlockType` |
| `IsRetryable` uses `==` instead of `errors.Is` | `TestIsRetryable` (wrapped and joined rows) |
| Fake records the caller's request without copying | `TestFake` |
| Fake ignores context cancellation | `TestFakeConformance` (`cancel`, `cancelled_context`) |
| Fake emits message done before the content events | `TestFake`, `TestFakeConformance` |

Also run: `go test -race -shuffle=on -count=20 ./pkg/llm/...` (clean), `make fuzz FUZZTIME=60s` (clean), `make check` in a fresh clone.
