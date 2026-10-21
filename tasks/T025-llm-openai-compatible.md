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

- Adapter on the official OpenAI Go SDK, with base URL and model configurable. (Changed in review, planner decision: a plain `net/http` client of the one endpoint instead, since the SDK added 8.0 MB to every binary that links it; see Notes.)
- Added in review: wired into `cmd/agentkit` as `model.provider: "openai"`.
- Mapping for messages, tools, tool results, streaming, stop reasons and usage. `llm.Opaque` blocks from other providers are dropped when sending.

## Acceptance criteria

- [x] Passes `llmtest.RunModel` against a replay server with recorded responses. — `TestConformance` (all 17 scenarios, `stall` included)
- [x] The request body for a representative request matches a golden file. — `TestRequestBodyGolden` (`testdata/request.golden.json`)
- [ ] `-tags live`: one request with a tool call completes against a configured endpoint (Ollama locally, or OpenAI with a key); skips without configuration. — `TestLiveToolCall` is written and skips cleanly here; **not run**: this session has no OpenAI key and no Ollama. Run it with `OPENAI_API_KEY=... go test -tags live -run Live ./pkg/llm/openaicompat/`, or with `OPENAI_BASE_URL=http://localhost:11434/v1 OPENAICOMPAT_TEST_MODEL=llama3.2` against Ollama.

## Notes

- **No SDK** (planner decision in review). The first version was on `github.com/openai/openai-go/v3` v3.71.0, which added 8.0 MB to any binary linking it (its root package holds every OpenAI service, kept alive through reflection), putting agentkit over 35 MB and the runner over 20 MB. It is now a `net/http` client of `POST {base_url}/chat/completions` (`openaicompat.go`), an SSE reader (`sse.go`) and plain JSON structs for the body and the chunks (`request.go`, `stream.go`). The SDK, its `golang.org/x/text`/`gjson` bumps and `coder/websocket` are gone from go.mod (`go mod tidy`: identical to main), and so is its archtest allowlist entry. Every earlier test, the golden file and the fuzz target pass unchanged except the plumbing that named SDK types.
- Public API changes from the SDK version: `WithClientOptions(option.RequestOption...)` is replaced by `WithAPIKey`, `WithHeader` and `WithMaxRetries`; errors from the server are `*openaicompat.Error` (StatusCode, Code, Type, Message, RequestID, InStream) instead of `*openai.Error` or `*ssestream.StreamError`, reachable with `errors.As` for every HTTP error and in-stream error (the SDK had none for in-stream errors).
- **Wired** (`cmd/agentkit/models_openai.go`, behind `!nomodels` like Anthropic, with a stub in `models_none.go`): `model.provider: "openai"` with `model.id` (required: `check` refuses an empty one; Anthropic's default ID now applies only to `anthropic`, through `agentModelConfig.id()`), `model.base_url` (default `$OPENAI_BASE_URL`, else OpenAI) and `model.api_key_env` (default `OPENAI_API_KEY`, read on the process that builds the model, the runner when split; unset sends no key). `base_url` or `api_key_env` on another provider fails startup, and so does `memory.native_tool` on `openai` (Claude's native memory tool type). `model.effort` goes as `reasoning_effort`. `make deps-check` now also forbids `pkg/llm/openaicompat` in the api and toolsets images. QUICKSTART section 5 documents it; COMPONENTS names the key variable. Size: +81,920 bytes on agentkit, +77,824 on the runner, +4,096 on api, 0 on toolsets (docs/SIZES.md row).
- `pkg/llm` and `llmtest` are unchanged.
- **Fixtures are hand-written** in the documented Chat Completions SSE form, stored as raw HTTP responses like the Anthropic adapter's (`curl -i` form, `: replay stall` marker). The conformance fixtures must stay hand-written (the suite compares against its own constants); the `errors/` ones imitate OpenAI, vLLM and Ollama error bodies and should be checked against real servers when one is at hand.
- Extra tests beyond the criteria, each for a real edge: `TestToolCalls` (interleaved parallel calls, text after a call, several calls at index 0 told apart by ID, a call without an ID and `finish_reason: "stop"` as older Ollama sends, a call without arguments), `TestTruncatedToolCallIsDropped`, `TestGarbledToolCallIsOverloaded`, `TestReasoningRoundTrip`, `TestReasoningSentBack`, `TestUsage`, `TestStopReasons`, `TestOtherChoicesIgnored`, `TestStreamCutShortIsOverloaded`, `TestErrorMapping` (17 fixtures, with the `*Error` fields), `TestKeyOverPlainHTTPIsSent`, `TestNoKeyNoAuthorization`, `TestKeyAndBaseURLFromEnvironment`, `TestConnectionRefusedIsOverloaded`, `TestRetriesStayOn`, `TestRetries`, `TestRetryWaitEndsWithContext`, `TestEvents*` (the SSE reader), `TestStreamExtras`, `TestAssistantContent`, `TestOptions`, `TestInvalidRequests`, `TestNewRejectsInvalidSettings`, `FuzzStream` and `ExampleNew`.

### Decisions

- **Chat Completions, not the Responses API**: it is what every compatible server and gateway speaks. Every request streams with `stream_options.include_usage`.
- **No default model**: `New` fails with `ErrInvalidRequest` without `WithModel`, since no model ID exists on every server. Base URL from `WithBaseURL`, else `OPENAI_BASE_URL`, else OpenAI; it must be an `http` or `https` URL with a host and no user info (`New` fails otherwise), and a query on it is kept (`u.JoinPath`). Key from `WithAPIKey`, else `OPENAI_API_KEY`; `WithAPIKey("")` sends none even with the variable set. Without a key no `Authorization` header is sent (local servers). `WithHeader` headers cannot replace `Authorization`, `Content-Type` or `Accept`.
- **Plain HTTP with a key is allowed** (the SDK sent a key over `http://` only to loopback, with `WithUnsafeAllowHTTP`): a keyed vLLM at `http://vllm:8000/v1` inside a cluster works. Documented in the package doc, on `WithBaseURL` and in QUICKSTART (the key crosses the network in the clear; use https anywhere else). `TestKeyOverPlainHTTPIsSent` replaces `TestKeyOverPlainHTTPIsInvalid`.
- **Messages**: system prompt → `system` message. A user message's tool results → `tool` messages, sent first (the API wants them right after the assistant's tool calls), then its text as one `user` message (a string, or text parts when there are several blocks). A failed result's content gets the prefix `Error: ` (the API has no error flag). An assistant message's text blocks are joined into its content; tool uses become `tool_calls` (arguments `{}` when empty). An assistant message left with nothing (only other providers' blocks) is left out. Tool uses in user messages, tool results in assistant messages, `Unknown` blocks and this adapter's reasoning in a user message are `ErrInvalidRequest` before sending.
- **Tools**: `function` tools; the schema's top-level members go as raw JSON, so numbers and nested key order are kept. A tool without a schema is sent without `parameters`. `tool_choice` and `parallel_tool_calls` are not sent (server default: auto, parallel).
- **Output limit**: none by default (OpenAI does not require one); `WithMaxTokens` or `Request.MaxTokens` sends `max_completion_tokens`, or `max_tokens` with `Options.LegacyMaxTokens` for servers that only know that one (OpenAI's reasoning models reject it).
- **Reasoning** streamed in `reasoning_content` (DeepSeek, vLLM, llama.cpp) or `reasoning` (Ollama, OpenRouter, Groq) becomes one `llm.Opaque` of provider `openaicompat` per run of deltas, with data `{"<field>":"<text>"}` (encoded without HTML escaping). It is sent back in the same field of the assistant message (blocks of the same field joined): DeepSeek's thinking mode needs it in tool loops and OpenRouter recommends it. `Options.DropReasoning` leaves it out, for servers that reject the field (possibly Groq; unverified). OpenRouter's structured `reasoning_details` is not handled (deferred).
- **Streaming**: text deltas are passed on live. Tool calls are held until the reply ends, then streamed in order of first appearance, each as its fragments: parallel calls may interleave fragments, and only the end says if one was truncated. A new ID at a known index starts a new call (some servers give every parallel call index 0). A call without an ID gets `call_<n>` (its position among the reply's calls). A call cut off by `length` (invalid JSON arguments) is dropped and the reply is `StopMaxTokens`; a finished call with invalid arguments or no name fails with `ErrOverloaded` (garbled; a retry samples again). Only choice 0 is read.
- **Stop reasons**: `stop` → `EndTurn`, or `ToolUse` when the reply has tool calls (older Ollama says `stop`); `tool_calls`, `function_call` → `ToolUse`; `length` → `MaxTokens`; `content_filter`, or any refusal text, → `Refusal` with `Explanation` = the refusal text and `Category` = the filtered categories of an Azure-style `content_filter_results`, sorted and comma-joined (empty if none); anything else → `StopOther` with `Raw`. A stream that ends without a `finish_reason` (with or without `[DONE]`) is `ErrOverloaded`.
- **Usage**: `prompt_tokens` includes cached tokens. `CacheReadTokens` = `prompt_tokens_details.cached_tokens`; `CacheWriteTokens` = `prompt_tokens_details.cache_write_tokens` (OpenAI, OpenRouter) or LiteLLM's `cache_creation_input_tokens`; `InputTokens` = prompt minus both (never negative). No usage chunk (servers that ignore `include_usage`) gives zero usage.
- **Errors**: by OpenAI code first where it says more than the status (`context_length_exceeded` → `ErrContextTooLong`, `insufficient_quota`/`invalid_api_key` → `ErrAuth`, `rate_limit_exceeded` → `ErrRateLimited`, `model_not_found` → `ErrInvalidRequest`), then by status (429 → rate limited; 401, 402, 403 → auth; 413 → context too long; other 4xx → invalid request, or context too long when the message says so, read from vLLM's top-level body too; 408, 409, 5xx → overloaded). Errors inside a stream (`data: {"error":...}` with a non-null `error`) have no status and map by type (vLLM's numeric `code` is used as the status). Error bodies are read up to 64 KiB: `{"error":{...}}`, `{"error":"..."}` (a message only) or vLLM's top-level `{"object":"error",...}`; a body of another kind (HTML from a proxy) maps by status. Transport failures, unreadable chunks and garbled streams → `ErrOverloaded`. A done context wins.
- **Idle timeout**: own watchdog counting data events, default 10 minutes (reasoning models on Chat Completions can be silent that long). Keep-alive comments still do not count, as with the SDK: a server can send them while its model is stuck. The watchdog also covers connecting and retry waits. The conformance test uses 1 s: at 100 ms the first chunk sometimes missed it under `-race` with the suite's parallel scenarios.
- **Retries**: the SDK's policy, reimplemented (`send`, `retryAfter`, `backoff`): 2 by default (`WithMaxRetries`), before the stream starts only, for connection failures and 408, 409, 429 and 5xx, or as `X-Should-Retry` says; the wait is `Retry-After-Ms`, else `Retry-After` (seconds or an HTTP date), giving up instead when that is over 2 minutes, else 0.5 s doubling to 8 s minus up to 25 % jitter. A cancelled context ends the wait. `TestRetriesStayOn`, `TestRetries`, `TestRetryWaitEndsWithContext`; other tests turn retries off.
- **SSE reader**: line ends `\n`, `\r\n` or a lone `\r`; `data:` lines joined with `\n`, one leading space dropped; comments, `event:`, `id:`, `retry:` and events without data skipped; an event cut off at EOF is still returned (it then fails to parse: garbled, `ErrOverloaded`); a line or event over 8 MiB is an error. `[DONE]` ends the stream; a stream that ends without it is fine if it had a `finish_reason`. A lone `\r` at the end of a read waits for the next byte (to tell it from `\r\n`), so a server using bare-CR line ends would see each event held until the next one arrives; none is known to.

### Wiring (done in review)

- `openaicompat.New(WithModel(id), WithBaseURL(url), WithAPIKey(key), WithOptions(Options{ReasoningEffort: effort}))` in `cmd/agentkit/models_openai.go`. `TestOpenAIProvider` builds it from an agent config against a stand-in server on plain http and checks the named key variable, the default one, and that Anthropic models never get the OpenAI key; `TestServe` cases cover a missing `id` and `base_url` on `anthropic`.
- `Response.Message` goes back unchanged through the harness, including `Opaque` reasoning blocks. No config switch for `DropReasoning` or `LegacyMaxTokens` yet (deferred: a `model` option when a server needs one).

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
| vLLM's top-level error message ignored | `TestErrorMapping/errors/vllm_context` |
| `include_usage` not requested | `TestOptions/defaults`, `TestRequestBodyGolden` |
| A stream without `finish_reason` accepted as `stop` | `TestStreamCutShortIsOverloaded`, `TestErrorMapping/errors/no_finish` |

The table above was run on the SDK version; the reader and the request mapping it covers carried over. For the `net/http` client, each of these was planted alone the same way (a scratch script applying one replacement, `go test -timeout=90s`, revert). All are caught, none by a compile error:

| Planted bug | Caught by |
|---|---|
| A lone `\r` not a line end until EOF | `TestEventsArriveLive/CR`, `TestEvents/multi-line_data,_CR` |
| `\r\n` split as two line ends | `TestEvents/multi-line_data,_CRLF` (added for this: one-line events hid it) |
| Space after `data:` kept | `TestConformance`, `ExampleNew` |
| Data lines joined without `\n` | `TestEvents/multi-line_data` |
| Comments read as data | `TestEvents/comments_and_fields`, `TestStreamExtras` |
| An event cut off at EOF dropped | `TestEvents/cut_off` |
| A read error taken for the end of the stream | `TestEventsReadError` |
| No bound on an event's data lines | `TestEventsTooLong` |
| `[DONE]` not recognised | `TestConformance`, `ExampleNew` |
| Key not sent; sent when empty; `OPENAI_API_KEY` winning over `WithAPIKey("")` | `TestRequestBodyGolden`, `TestKeyOverPlainHTTPIsSent`; `TestNoKeyNoAuthorization` |
| `WithHeader` replacing `Authorization` | `TestKeyOverPlainHTTPIsSent` |
| Base URL path dropped | `TestRequestBodyGolden`, `TestKeyAndBaseURLFromEnvironment` |
| Non-http scheme accepted | `TestNewRejectsInvalidSettings` |
| Retries never stop; one retry too many | `TestConformance/error_*`, `TestErrorMapping` |
| Connection failures not retried | `TestRetries/connection_failure` |
| 4xx retried; `X-Should-Retry: false` ignored | `TestRetries/400_not_retried`, `TestRetries/server_says_no` |
| `Retry-After-Ms` ignored | `TestRetries/429_then_reply` (a maximum wait added for this) |
| `Retry-After` read as milliseconds | `TestRetries/retry-after_seconds`, `TestRetries/wait_too_long` |
| An over-long `Retry-After` waited for; the retry wait ignoring the context | the test timeout (`TestRetries/wait_too_long`, `TestRetryWaitEndsWithContext`) |
| Idle watchdog not re-armed after a chunk | `TestConformance/stall` |
| In-stream errors skipped | `TestErrorMapping/*mid_stream*` |
| vLLM's top-level message, the `{"error":"..."}` message, vLLM's in-stream numeric code ignored | `TestErrorMapping/errors/vllm_*`, `string_error`, `mid_stream_vllm` |
| HTTP status ignored in the mapping | `TestErrorMapping/errors/plain_429` |
| `"error": null` in a chunk taken as an error | `TestStreamExtras` |
| `"usage": null` after the usage wiping it | `TestUsage/null_after_usage` (added for this) |
| Reasoning fields swapped, reading or sending | `TestReasoningRoundTrip`, `TestReasoningSentBack`, `TestRequestBodyGolden` |
| HTML escaped in the request body | `TestRequestBodyGolden` |
| `stream` not requested | `TestOptions/defaults`, `TestRequestBodyGolden` |
| Empty assistant text left out | `TestAssistantContent/empty_text` (added for this) |

Also run: `go test -race -shuffle=on -count=10 ./pkg/llm/openaicompat/` (clean), `go test -fuzz=FuzzStream -fuzztime=60s` (clean, 566k execs), `go vet -tags live`, `make check`.

### Deferred

- The live criterion (no key or local server here).
- OpenRouter's `reasoning_details` (structured, with signatures for some providers), Gemini's `extra_content` thought signatures, and DeepSeek's `prompt_cache_hit_tokens`.
- `tool_choice`, `parallel_tool_calls`, `strict` tools, temperature and other sampling settings, extra body fields for vendor options (Ollama's `options`, vLLM's `chat_template_kwargs`).
- A way to turn off `stream_options` for servers that reject it (none known among the targets).
- Images and documents in tool results (llm has text only).
