---
id: T021
title: "pkg/llm/anthropic: adapter on the official Go SDK"
epic: E04
status: review
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

- [x] Passes `llmtest.RunModel` against a local replay server serving recorded API responses (no network in unit tests). — `TestConformance` (all 16 scenarios)
- [x] The request body for a representative request (system, history with tool use and results, tools, options) matches a golden file. — `TestRequestBodyGolden` (`testdata/request.golden.json`)
- [x] A recorded response with thinking blocks round-trips: the next request contains them byte-identical. — `TestThinkingBlocksRoundTrip`
- [x] A refusal response gives `StopReason` `Refusal` with its category. — `TestRefusalStopReason`
- [x] 429, 529, 400 and 401 responses map to `ErrRateLimited`, `ErrOverloaded`, `ErrInvalidRequest`, `ErrAuth`. — `TestErrorMapping`
- [ ] `-tags live`: one real streaming request with one tool call completes against the API (skips without credentials). — `TestLiveToolCall` is written and compiles, but **has not run**: this environment has no `ANTHROPIC_API_KEY`, no `ANTHROPIC_AUTH_TOKEN` and no `ant` CLI, so it skipped. Run `go test -tags live -count=1 -run TestLiveToolCall -v ./pkg/llm/anthropic/` with a key, then tick this and set the status to `done`.

## Notes

- archtest allowlist: `pkg/llm/anthropic` → `github.com/anthropics/anthropic-sdk-go`, `.../option`, `.../packages/param`, `.../shared/constant` (SDK v1.80.0).
- `pkg/llm` and `llmtest` are unchanged.
- **Fixtures are hand-written**, not recorded: no credentials here. They follow the documented SSE event format and are stored as raw HTTP responses (`curl -i` form, read with `http.ReadResponse`), so a real recording can be dropped in. An SSE comment line `: replay stall` makes the replay server go quiet there until the client cancels. The conformance fixtures (`testdata/conformance/`) must stay hand-written anyway: the suite compares against its own constants. Re-record the others (`thinking`, `refusal_cyber`, `fallback`, ...) from a live run once a key is available.
- Extra tests beyond the criteria, each for a real edge: `TestOptions` (precedence and defaults), `TestInvalidRequests` (rejected before sending), `TestNewRejectsInvalidSettings`, `TestTruncatedToolCallIsDropped`, `TestInterleavedBlocksStreamOneAtATime`, `TestStreamCutShortIsOverloaded`, `TestRetriesStayOn`, `TestFallbackLeavesOutDeclinedThinking`, `FuzzStream` (arbitrary event streams: no panic; either a final message consistent with its events and valid tool input, or an error matching a sentinel), and `ExampleNew`.

### Decisions

- **Beta endpoint, always.** Requests go through `client.Beta.Messages.NewStreaming`, which carries `fallbacks`. The refusal fallback is on by default in its `"default"` form (`fallbacks: "default"` + beta `server-side-fallback-2026-07-01`): the API picks the fallback models by refusal category. `Options{Fallback: FallbackOff}` turns it off; do that for models that reject it (Claude Haiku 5.5) and on Bedrock/Vertex/Foundry.
- **Opaque blocks are spliced into the body byte for byte.** Verified: the SDK, like `encoding/json`, compacts and HTML-escapes raw JSON (`<` becomes `<`). The adapter marshals the SDK params with a placeholder string per Opaque block (`agentkit-opaque-<random nonce>-<n>`), replaces each placeholder (checked to occur exactly once) with the block's data, and sends the result with `option.WithRequestBody`. The SDK still appends `"stream":true` and sets the headers; retries resend the same bytes.
- **Opaque data is the block's wire JSON with its deltas applied**, built by the SDK's own `Accumulate` on a one-block message per opaque block, so unknown fields and key order survive. Not the whole-message accumulator: it appends text with `+=` per delta, which is quadratic on long replies.
- **Tool input is held back until the block ends**, then sent as `ToolUseStart`, the fragments in order, and `BlockDone`. A call cut off by `max_tokens` is only recognizable at its end (its input is not valid JSON) and must be dropped, and the llm stream contract has no way to take back a started block. Nothing is lost in practice: without `eager_input_streaming` (not set) the API buffers tool input and sends it in a burst anyway.
- **Interleaved deltas** of several open blocks (the SDK documents that this can happen) are queued, so llm sees one block at a time; text of the oldest open block streams live.
- **Empty text blocks are dropped** from replies: the API rejects empty text blocks in requests.
- **After a mid-reply fallback**, the API asks clients to leave out the declined attempt's thinking. In an assistant message with a `fallback` block, thinking and redacted-thinking blocks before the last fallback block are not sent; everything else is. This is the only place the adapter leaves out an own-provider block.
- **An assistant message that ends up empty** (only other providers' Opaque blocks) is left out of the request.
- **Stop reasons**: `end_turn`, `tool_use`, `max_tokens`, `refusal` (category and explanation from `stop_details`; a null category is ""); everything else (`stop_sequence`, `pause_turn`, `compaction`, `model_context_window_exceeded`, unknown values) is `StopOther` with `Raw`.
- **Usage**: `input_tokens` → `InputTokens` (Anthropic already excludes cache), `cache_read_input_tokens` → `CacheReadTokens`, `cache_creation_input_tokens` → `CacheWriteTokens`. With a fallback, the top-level usage covers only the attempt that produced the reply (API semantics).
- **Errors** are classified by the API error type first (an `error` event inside a stream arrives with HTTP status 200), then by status: `rate_limit_error`/429 → `ErrRateLimited`; `overloaded_error`, `api_error`, `timeout_error`, 408, 409, 5xx and anything unrecognized → `ErrOverloaded`; `authentication_error`, `permission_error`, `billing_error`/401, 402, 403 → `ErrAuth`; `request_too_large`/413 → `ErrContextTooLong`; `invalid_request_error`, `not_found_error`/other 4xx → `ErrInvalidRequest`, or `ErrContextTooLong` when the message says the prompt is too long (the API has no error type for it, so this one reads the message). Non-API errors (connection dropped, stream garbled, stream ended before `message_stop`) → `ErrOverloaded`. A done context always wins and is returned as is.
- **Options** (`Request.Provider` as `Options` or `*Options`; model defaults via `WithOptions`): every zero field means "default"; a request's fields win over the model's, which win over the built-in ones: effort `medium` (always sent explicitly, since it is Opus 5.5's default and lower than Opus 5's), thinking display left to the API, cache `[CacheConversation]` (top-level automatic `cache_control`), fallback `default`. `Cache: nil` means default, a non-nil empty slice or `NoCache` means none. `Betas` add up. Invalid values are `ErrInvalidRequest` before anything is sent.
- **Prompt caching is on by default** (automatic breakpoint on the last cacheable block): every agent turn re-reads the previous turn's prefix. `CacheTools` and `CacheSystem` add explicit breakpoints.
- **Thinking** is always sent as `{"type":"adaptive"}` (plus `display` if set): equivalent on Opus 5.5 and required on older models. `tool_choice` is `{"type":"auto"}`, sent only with tools.
- **Max tokens**: `DefaultMaxTokens` = 64000 when the request leaves it zero (the claude-api guidance for streaming; thinking counts toward it). `WithMaxTokens` changes it.
- **Retries**: the SDK default (2 retries, honoring `retry-after`) stays on. The conformance and error tests turn them off (`option.WithMaxRetries(0)`) so each error fixture is served once; `TestRetriesStayOn` checks the default retries a 529.
- The SDK honors `ANTHROPIC_BASE_URL`; `WithBaseURL` overrides it, and every unit test sets it.

### For T022 and T024

- Construct with `m, err := anthropic.New()`: Claude Opus 5.5, effort `medium`, adaptive thinking, automatic prompt caching, refusal fallback on. Credentials from the SDK chain: `ANTHROPIC_API_KEY`, or `ANTHROPIC_AUTH_TOKEN`, an `ant auth login` profile, or the workload-identity env vars. Options: `WithModel`, `WithMaxTokens`, `WithOptions`, `WithBaseURL`, `WithHTTPClient`, `WithClientOptions(option...)` (API key, retries, middleware).
- Per request: `Provider: anthropic.Options{Effort: anthropic.EffortHigh, ThinkingDisplay: anthropic.ThinkingSummarized}`. Thinking text is empty unless `ThinkingSummarized` (or `ThinkingUpdates` with its beta) is set; a CLI that shows reasoning wants `ThinkingSummarized`.
- **Keep history append-only and the system prompt and tools stable within a run.** Send `Response.Message` back exactly as returned. On Opus 5.5 a thinking block is bound to the conversation prefix: editing an earlier message, the system prompt or the tool list makes the next request a 400 (enforced for accounts created on or after 2026-08-31). It also keeps the prompt cache warm.
- Tool-call input arrives in one burst at the end of the block, not progressively.
- A reply cut off in the middle of a tool call comes back without that call and with `StopMaxTokens`; the harness should treat it like any max-tokens stop.
- A refusal is `StopRefusal` with `Category` (`cyber`, `bio`, ...). With the fallback on, a reply can switch models mid-way; the message then holds an `Opaque` block of type `fallback`, which must be sent back like any other.
- `make test-live` (or the command above) runs `TestLiveToolCall`; T024's own live test needs the same credentials.

### Mutation check

Each bug planted alone, `go test ./pkg/llm/anthropic/` run, change reverted. Every one makes a test fail (no compile errors):

| Planted bug | Caught by |
|---|---|
| Thinking block re-encoded in the next request (SDK raw override instead of the splice) | `TestThinkingBlocksRoundTrip` |
| Thinking block dropped from the next request | `TestThinkingBlocksRoundTrip`, `TestRequestBodyGolden`, `TestFallbackLeavesOutDeclinedThinking` |
| Other providers' Opaque blocks sent to Anthropic | `TestRequestBodyGolden` |
| Tool input deltas concatenated out of order | `TestConformance/tool_call`, `TestInterleavedBlocksStreamOneAtATime` |
| Truncated tool call kept (with `{}` input, as the SDK accumulator does) instead of dropped with `StopMaxTokens` | `TestTruncatedToolCallIsDropped`, `FuzzStream` (seed) |
| Cached tokens counted in `InputTokens` | `TestConformance/text` |
| 529 (`overloaded_error`) mapped to `ErrInvalidRequest` | `TestErrorMapping/529`, `TestErrorMapping/error_event_mid-stream`, `TestConformance/error_overloaded`, `TestConformance/error_mid_stream` |
| Refusal mapped to `StopEndTurn` | `TestRefusalStopReason`, `TestConformance/refusal` |
| `is_error` not set on failed tool results | `TestRequestBodyGolden` |
| Forced `tool_choice` (`any`) sent | `TestRequestBodyGolden` |

Also run: `go test -race -shuffle=on -count=20 ./pkg/llm/...` (clean), `make fuzz FUZZTIME=60s` (clean), `make check` in a fresh clone.

### Deferred

- The live criterion above.
- After a mid-reply fallback the API also wants unpaired `tool_use` (and server-tool) blocks before the switch left out. Dropping a `tool_use` means dropping its `tool_result` in the next user message too, so it is not done; it needs a complete tool call in the declined part of a reply, which should be rare.
- No way to omit `effort` for models that reject it (Claude Haiku 4.5); add an explicit "unset" when a caller needs such a model.
- 1-hour cache TTL, `strict` tool definitions, `eager_input_streaming` (which would also need a way to retract a started tool block), a retry-after hint on `ErrRateLimited`.
