# Anthropic adapter fixtures

Each `.http` file is one HTTP response as `curl -i` prints it, served by the
replay server in `replay_test.go`. `<name>.2.http`, `<name>.3.http` and so on
are the replies to the next requests of the same exchange.

## Recorded from the real API

Re-record them with one command (needs `ANTHROPIC_API_KEY` or
`ANTHROPIC_KEY`):

```sh
make record-anthropic
```

It runs `TestRecorded` with `AGENTKIT_ANTHROPIC_RECORD=1`: the replay server
forwards each request the adapter sends to the API, writes the response, and
the test's checks run on the live replies. A recording keeps only the status
line, `Content-Type`, a placeholder `Request-Id: req_replay` and the body, with
the API's request IDs replaced. `TestFixturesHoldNoAccountData` fails if any
fixture here holds a key, an account header or ID, or a real request ID.
Review the diff before committing.

| Fixture | What it records |
|---|---|
| `recorded/text` | A plain text reply |
| `recorded/tool_call` (+ `.2`) | A tool call, then the answer to its result |
| `recorded/tool_call_no_input` | A call of a tool that takes no input (`{}`) |
| `recorded/max_tokens` | A reply cut off at `max_tokens` |
| `recorded/thinking` (+ `.2`) | Summarized thinking with a real signature, sent back and accepted |
| `recorded/tool_truncated` | A tool call cut off by `max_tokens` (the adapter drops it) |
| `recorded/cache` (+ `.2`) | A cached system prompt: written, then read |
| `conformance/error_auth` | 401 for an invalid key |
| `conformance/error_invalid_request` | 400 for `max_tokens` over the model's limit |
| `conformance/error_context_too_long` | 400 "prompt is too long" |
| `errors/not_found` | 404 for an unknown model |

The conformance suite only checks which `llm` error its error scenarios map
to, so those three are recorded into its own fixtures.

## Written by hand

These stay hand-written: the API cannot produce them on demand, or the test
needs exact values.

| Fixture | Why |
|---|---|
| `conformance/text`, `tool_call`, `tool_call_no_input`, `opaque`, `max_tokens`, `stop_other`, `refusal` | `llmtest.RunModel` compares the reply with its own constants (text, IDs, usage), which no recording matches. The `recorded/` fixtures cover the same scenarios with real replies. |
| `conformance/cancel` | Uses the replay server's `: replay stall` marker (a stream that goes silent), which is not API output. Also serves `stop_early` and `stall`. |
| `conformance/error_rate_limited`, `error_overloaded` | A 429 or 529 cannot be asked for. |
| `conformance/error_mid_stream`, `errors/mid_stream_rate_limit` | An `error` event in the middle of a stream cannot be asked for. |
| `errors/api_error`, `errors/unavailable` | A 500, or a 503 without a body, cannot be asked for. |
| `errors/permission` | A 403 needs a key without access to a resource; there is none to record with. |
| `errors/too_large` | A 413 needs a request body over 32 MB. |
| `thinking` (+ `.2`) | Has a `redacted_thinking` block and a field the SDK does not model, neither of which the API produces on demand. `recorded/thinking` is the real counterpart. |
| `refusal_cyber`, `fallback` | A safety-classifier refusal, and a server-side fallback after one, cannot be asked for. |
| `interleaved` | Deltas of several open blocks interleaved: documented as possible, not producible on demand. |
| `retry` (+ `.2`) | A 529 followed by a success. |
| `cut_short` | A stream that ends before `message_stop`, as a dropped connection does. |
| `request.golden.json` | Not a response: the golden request body. |
| `fuzz/` | Fuzzer reproducers. |
