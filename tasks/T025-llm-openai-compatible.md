---
id: T025
title: "pkg/llm/openaicompat: OpenAI-compatible adapter"
epic: E08
status: review
depends_on: [T020]
---

# T025: `pkg/llm/openaicompat`

## Context

One adapter for every OpenAI-compatible endpoint: OpenAI, Grok, Groq, vLLM, Ollama, and gateways such as the LiteLLM proxy or OpenRouter ([PLAN.md decisions](../docs/PLAN.md#decisions): gateways are supported, never required). Moved from E04 to E08: build it when a second provider is needed.

## Scope

- Adapter on the official OpenAI Go SDK, with base URL and model configurable.
- Mapping for messages, tools, tool results, streaming, stop reasons and usage. `llm.Opaque` blocks from other providers are dropped when sending.

## Acceptance criteria

- [x] Passes `llmtest.RunModel` against a replay server with recorded responses. — `TestConformance` (all 17 scenarios, `stall` included)
- [x] The request body for a representative request matches a golden file. — `TestRequestBodyGolden` (`testdata/request.golden.json`)
- [ ] `-tags live`: one request with a tool call completes against a configured endpoint (Ollama locally, or OpenAI with a key); skips without configuration. — `TestLiveToolCall` is written and skips cleanly here; **not run**: this session has no OpenAI key and no Ollama. Run it with `OPENAI_API_KEY=... go test -tags live -run Live ./pkg/llm/openaicompat/`, or with `OPENAI_BASE_URL=http://localhost:11434/v1 OPENAICOMPAT_TEST_MODEL=llama3.2` against Ollama.

## Notes

- New dependency: `github.com/openai/openai-go/v3` v3.71.0 (released 2026-10-02; the newest releases were days old). It also moved `golang.org/x/text` to v0.41.0 and `tidwall/gjson` to v1.19.0 for the whole module, and added `coder/websocket` (the SDK's realtime client). archtest allowlist: `pkg/llm/openaicompat` → the SDK root, `option`, `packages/param`, `packages/respjson`, `packages/ssestream`, `shared`.
- **Not wired into `cmd/agentkit`** (out of scope), and the size is why it should not be wired as is: linking the adapter adds **8.0 MB** to the binary (docs/SIZES.md row). The SDK's root package carries every OpenAI service. See "For the wiring task" below.
- `pkg/llm` and `llmtest` are unchanged.
- **Fixtures are hand-written** in the documented Chat Completions SSE form, stored as raw HTTP responses like the Anthropic adapter's (`curl -i` form, `: replay stall` marker). The conformance fixtures must stay hand-written (the suite compares against its own constants); the `errors/` ones imitate OpenAI, vLLM and Ollama error bodies and should be checked against real servers when one is at hand.
- Extra tests beyond the criteria, each for a real edge: `TestToolCalls` (interleaved parallel calls, text after a call, several calls at index 0 told apart by ID, a call without an ID and `finish_reason: "stop"` as older Ollama sends, a call without arguments), `TestTruncatedToolCallIsDropped`, `TestGarbledToolCallIsOverloaded`, `TestReasoningRoundTrip`, `TestReasoningSentBack`, `TestUsage`, `TestStopReasons`, `TestOtherChoicesIgnored`, `TestStreamCutShortIsOverloaded`, `TestErrorMapping` (16 fixtures), `TestKeyOverPlainHTTPIsInvalid`, `TestConnectionRefusedIsOverloaded`, `TestRetriesStayOn`, `TestOptions`, `TestInvalidRequests`, `TestNewRejectsInvalidSettings`, `FuzzStream` and `ExampleNew`.

### Decisions

- **Chat Completions, not the Responses API**: it is what every compatible server and gateway speaks. Every request streams with `stream_options.include_usage`.
- **No default model**: `New` fails with `ErrInvalidRequest` without `WithModel`, since no model ID exists on every server. Base URL from `WithBaseURL`, else `OPENAI_BASE_URL`, else OpenAI; key from `OPENAI_API_KEY` or `WithClientOptions(option.WithAPIKey(...))`. Without a key no `Authorization` header is sent (local servers).
- **The SDK refuses to send a key over plain HTTP** except to a loopback address with `option.WithUnsafeAllowHTTP()` (which also bypasses a custom HTTP client for those requests). So a keyed vLLM at `http://vllm:8000` in a cluster cannot be reached with a key; without a key it works. The refusal is not retryable: an error before any HTTP response that is not a transport error (`*url.Error`) maps to `ErrInvalidRequest` (`TestKeyOverPlainHTTPIsInvalid`).
- **Messages**: system prompt → `system` message. A user message's tool results → `tool` messages, sent first (the API wants them right after the assistant's tool calls), then its text as one `user` message (a string, or text parts when there are several blocks). A failed result's content gets the prefix `Error: ` (the API has no error flag). An assistant message's text blocks are joined into its content; tool uses become `tool_calls` (arguments `{}` when empty). An assistant message left with nothing (only other providers' blocks) is left out. Tool uses in user messages, tool results in assistant messages, `Unknown` blocks and this adapter's reasoning in a user message are `ErrInvalidRequest` before sending.
- **Tools**: `function` tools; the schema's top-level members go as raw JSON, so numbers and nested key order are kept. A tool without a schema is sent without `parameters`. `tool_choice` and `parallel_tool_calls` are not sent (server default: auto, parallel).
- **Output limit**: none by default (OpenAI does not require one); `WithMaxTokens` or `Request.MaxTokens` sends `max_completion_tokens`, or `max_tokens` with `Options.LegacyMaxTokens` for servers that only know that one (OpenAI's reasoning models reject it).
- **Reasoning** streamed in `reasoning_content` (DeepSeek, vLLM, llama.cpp) or `reasoning` (Ollama, OpenRouter, Groq) becomes one `llm.Opaque` of provider `openaicompat` per run of deltas, with data `{"<field>":"<text>"}` (encoded without HTML escaping). It is sent back in the same field of the assistant message (blocks of the same field joined): DeepSeek's thinking mode needs it in tool loops and OpenRouter recommends it. `Options.DropReasoning` leaves it out, for servers that reject the field (possibly Groq; unverified). OpenRouter's structured `reasoning_details` is not handled (deferred).
- **Streaming**: text deltas are passed on live. Tool calls are held until the reply ends, then streamed in order of first appearance, each as its fragments: parallel calls may interleave fragments, and only the end says if one was truncated. A new ID at a known index starts a new call (some servers give every parallel call index 0). A call without an ID gets `call_<n>` (its position among the reply's calls). A call cut off by `length` (invalid JSON arguments) is dropped and the reply is `StopMaxTokens`; a finished call with invalid arguments or no name fails with `ErrOverloaded` (garbled; a retry samples again). Only choice 0 is read.
- **Stop reasons**: `stop` → `EndTurn`, or `ToolUse` when the reply has tool calls (older Ollama says `stop`); `tool_calls`, `function_call` → `ToolUse`; `length` → `MaxTokens`; `content_filter`, or any refusal text, → `Refusal` with `Explanation` = the refusal text and `Category` = the filtered categories of an Azure-style `content_filter_results`, sorted and comma-joined (empty if none); anything else → `StopOther` with `Raw`. A stream that ends without a `finish_reason` (with or without `[DONE]`) is `ErrOverloaded`.
- **Usage**: `prompt_tokens` includes cached tokens. `CacheReadTokens` = `prompt_tokens_details.cached_tokens`; `CacheWriteTokens` = `prompt_tokens_details.cache_write_tokens` (OpenAI, OpenRouter) or LiteLLM's `cache_creation_input_tokens`; `InputTokens` = prompt minus both (never negative). No usage chunk (servers that ignore `include_usage`) gives zero usage.
- **Errors**: by OpenAI code first where it says more than the status (`context_length_exceeded` → `ErrContextTooLong`, `insufficient_quota`/`invalid_api_key` → `ErrAuth`, `rate_limit_exceeded` → `ErrRateLimited`, `model_not_found` → `ErrInvalidRequest`), then by status (429 → rate limited; 401, 402, 403 → auth; 413 → context too long; other 4xx → invalid request, or context too long when the message says so, read from vLLM's top-level body too; 408, 409, 5xx → overloaded). Errors inside a stream (`data: {"error":...}`) have no status and map by type (vLLM's numeric `code` is used as the status). A body the SDK cannot read (`{"error":"..."}`) maps by the status seen by a per-request middleware. Transport failures and garbled streams → `ErrOverloaded`. A done context wins.
- **Idle timeout**: own watchdog counting chunks, default 10 minutes (reasoning models on Chat Completions can be silent that long; the SDK hides SSE keep-alive comments). The conformance test uses 1 s: at 100 ms the first chunk sometimes missed it under `-race` with the suite's parallel scenarios.
- **Retries**: the SDK default (2, honoring `retry-after`/`retry-after-ms`) stays on (`TestRetriesStayOn`); tests turn it off.

### For the wiring task (cmd/agentkit, a `model.provider: "openai"`)

- Construct with `openaicompat.New(openaicompat.WithModel(id), openaicompat.WithBaseURL(url))`; the key from the environment (`OPENAI_API_KEY`) to the runner only, as for Anthropic. Add `option.WithUnsafeAllowHTTP()` only for a loopback `http://` base URL.
- **Size first**: linking it adds 8.0 MB (agentkit 29.1 → 37.1 MB binary, over the 35 MB image budget; runner 14.4 → 22.4 MB, over 20 MB). Put it behind its own tag (`noopenai`, with a `_none.go` twin, out of the default and runner images), or replace the SDK with a `net/http` client of the one endpoint: the request mapping, the reader and every test here carry over; only `openaicompat.go`'s transport and `errors.go`'s SDK error types change. That choice is the owner's (the task asked for the SDK).
- Send `Response.Message` back unchanged, including `Opaque` reasoning blocks; set `DropReasoning` for servers that reject the field.

### Mutation check

Each bug planted alone, `go test ./pkg/llm/openaicompat/` run, change reverted. Every one makes a test fail (no compile errors):

| Planted bug | Caught by |
|---|---|
| Cached tokens counted in `InputTokens` | `TestConformance/text`, `TestUsage` |
| Argument fragments joined in reverse order | `TestConformance/tool_call`, `TestToolCalls/interleaved` |
| Truncated tool call kept (reported as garbled) instead of dropped | `TestTruncatedToolCallIsDropped` |
| Other providers' Opaque blocks sent | `TestRequestBodyGolden` |
| Failed tool result sent without the `Error: ` prefix | `TestRequestBodyGolden` |
| Tool results sent after the user's text | `TestRequestBodyGolden` |
| `content_filter` mapped to `end_turn` | `TestConformance/refusal`, `TestStopReasons` |
| 429 mapped to `ErrOverloaded` | `TestErrorMapping/errors/plain_429` (added for this: every other 429 fixture had a code) |
| `insufficient_quota` treated as a rate limit | `TestErrorMapping/errors/insufficient_quota` |
| Reasoning not sent back | `TestReasoningRoundTrip`, `TestReasoningSentBack`, `TestRequestBodyGolden` |
| `stop` with tool calls kept as `end_turn` | `TestToolCalls/no_ID,_stop` |
| A new ID at a known index continues the old call | `TestToolCalls/same_index,_new_ID` |
| Idle watchdog never armed | `TestConformance/stall` |
| A request the SDK refused reported as retryable | `TestKeyOverPlainHTTPIsInvalid` |
| vLLM's top-level error message ignored | `TestErrorMapping/errors/vllm_context` |
| `include_usage` not requested | `TestOptions/defaults`, `TestRequestBodyGolden` |
| A stream without `finish_reason` accepted as `stop` | `TestStreamCutShortIsOverloaded`, `TestErrorMapping/errors/no_finish` |

Also run: `go test -race -shuffle=on -count=10 ./pkg/llm/openaicompat/` (clean), `go test -fuzz=FuzzStream -fuzztime=60s` (clean, 145k execs), `make check`.

### Deferred

- The live criterion (no key or local server here).
- OpenRouter's `reasoning_details` (structured, with signatures for some providers), Gemini's `extra_content` thought signatures, and DeepSeek's `prompt_cache_hit_tokens`.
- `tool_choice`, `parallel_tool_calls`, `strict` tools, temperature and other sampling settings, extra body fields for vendor options (Ollama's `options`, vLLM's `chat_template_kwargs`).
- A way to turn off `stream_options` for servers that reject it (none known among the targets).
- Images and documents in tool results (llm has text only).
