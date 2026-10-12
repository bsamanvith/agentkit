---
id: T053
title: "pkg/agentclient: the Go client for the agent API"
epic: E11
status: done
depends_on: [T036]
---

# T053: `pkg/agentclient`

## Context

Everything that talks to the agent API (the CLI, headless runs, e2e tests, evals) needs the same things: start a run, read the SSE stream as typed events, continue a conversation, handle the API's errors. One tested client does it once. Decisions: [E11](E11-clients.md).

## Scope

- `agentclient.New(baseURL, opts...)` with `WithToken`, `WithHTTPClient`, `WithUserAgent`.
- `c.Stream(ctx, RunRequest{Message, ConversationID}) (*Stream, error)`: `Next() (wire.Event, error)` until the terminal event; `Close()` cancels the run (closing the request, which the server treats as a cancel). Run and conversation IDs available from the headers and `run_start`.
- `c.Run(ctx, RunRequest) (wire.Run, error)` (JSON mode), `c.GetRun`, `c.GetConversation`.
- Errors: one `*APIError{Status, Code, Message, RetryAfter}` with `errors.Is` sentinels for every code in T036's table (`ErrConversationBusy`, `ErrConversationInterrupted`, `ErrOverCapacity`, `ErrUnauthorized`, `ErrNotFound`, ...). A stream that ends without a terminal event is `ErrStreamTruncated`.
- SSE parsing robust to what the spec allows (multi-line `data`, comments, `\r\n`, missing `id`), with a line-length limit; unknown event types and unknown fields are passed through, never errors (the API is additive).
- Wire types: shared with the server (see E11 "Wire types"; move to `pkg/agentapi/wire` if needed, T036's golden tests unchanged).
- Use it in `cmd/agentkit`'s e2e tests where they parse SSE by hand today.

## References

- [WHATWG HTML: Server-sent events](https://html.spec.whatwg.org/multipage/server-sent-events.html) (the parsing rules the client follows)
- T036's Notes and `pkg/agentapi/testdata/*.sse` (the wire contract)

## Acceptance criteria

Tests are in `pkg/agentclient` unless named otherwise. Tests against the real server go through one rig (`helpers_test.go`: `agentapi.Server` on `httptest`, a scripted model that can hold replies, the demo toolset, a token, and a copy of every byte the server wrote per run). Byte-level tests feed bodies through a fake transport (no network) and are judged against `referenceSSE`, an independent reading of the SSE standard.

- [x] Against a real `agentapi.Server` (in-process `httptest`, fake model, demo tools): a streamed run yields exactly the server's events in order, ending with `run_done`; a follow-up in the same conversation works; JSON mode returns the same `Run`. — `TestStreamedRunFollowUpAndJSON` (the client's events and raw data equal the reference reading of the bytes the server wrote; a tool run, then a follow-up whose model request carries the first run's messages, and whose conversation holds what both streams announced; `Run` in JSON mode equals `GetRun`, and the streamed run's `GetRun` agrees with its `run_done`; run and conversation IDs from the headers before the first event). `Example` does the same in short.
- [x] T036's golden SSE files (`pkg/agentapi/testdata/*.sse`) parse to the expected events (the client and server agree on the wire, by the same fixtures). — `TestGoldenStreams` (every event equals its `data:` line decoded, `RawData` is the line byte for byte, and each event re-encodes to the same line; 11 events ending `run_done`, and `run_error` `refused`).
- [x] Every error code in T036's table maps to its sentinel, with `RetryAfter` set for 503s (table test against the real server where it can produce the error, a stub otherwise). — `TestAPIErrors`: all 8 codes produced by the real server (400 empty message, 401 no and wrong token, 404 run / conversation / continuing an unknown conversation, 413, 409 busy, 409 interrupted after a `Close`, 503 `over_capacity` and `shutting_down` with `RetryAfter` 2 s), through `Stream`, `Run`, `GetRun` and `GetConversation`; each matches its sentinel and no other; a last subtest checks every code was produced. `TestErrorsFromElsewhere` (stubs): a proxy's HTML 502 and an unknown code are `*APIError` matching no sentinel; `Retry-After` as an HTTP date (relative to the response's `Date`), in the past, not a number, and huge (no overflow).
- [x] `Close()` mid-stream cancels the run on the server (the server's run ends `cancelled`); a stream cut without a terminal event gives `ErrStreamTruncated`. — `TestCloseCancelsTheRun` (`Close` after turn 1, `Close` before any event, and the stream's context cancelled: each time the run ends `failed`/`cancelled` and the held model call is the last one; `Next` then says `ErrClosed` or `context.Canceled`, and nothing else); `TestNextAfterClose` (events already received are not returned after `Close`); `TestStreamCutBeforeItsEnd` (a response that ends, and a connection that breaks, after two events: `ErrStreamTruncated`; the IDs come from `run_start` when the headers are missing); `TestSSEFraming` (an event cut off at the end).
- [x] Unknown event types and fields pass through (the additive rule), checked with T036's `TestEventJSONIsAdditive` fixtures. — `TestUnknownEventsAndFieldsPassThrough` (the golden streams with unknown fields added at the levels T036's test adds them, plus run_start's prompt, unknown SSE fields, and a `thinking_delta` event before every event: the same known events, the unknown ones returned with their raw data, the stream ends only at `run_done`/`run_error`). The additive rule stops at a message; see "Finding" below.
- [x] `FuzzSSE`: the parser never panics, never reads past its line limit, and never invents events. — `FuzzSSE` (any body reads exactly as `referenceSSE` plus the API's rules say: no event invented, dropped or reordered, the same end; the same body followed by an endless line, with a 256-byte limit, reads a prefix of those events, stops with `ErrResponseTooLarge` (never waits for the line's end) and reads at most the input, the limit and the read buffer); `TestLineLimit` (an endless line, endless data lines, an endless comment).
- [x] Mutation list recorded. — below, no survivors.

Beyond the criteria: `TestSSEFraming` (what the standard allows: LF, CRLF, CR, mixed line ends, data over several lines with LF and CRLF, comments, no space after the colon, no `id` or `event`, a BOM, blank lines, unknown fields; data that is not an event (not JSON, no type, `null`) is `ErrBadResponse`; nothing is read after the terminal event), `TestRequests` (method, path under a base URL with a prefix, IDs path-escaped, `Accept`, body, token, User-Agent), `TestNotTheAPI` (a 200 that is not a stream or JSON is `ErrBadResponse`; `New` refuses unusable base URLs), `TestClientDoesNotImportTheServer` (`go list -deps`: no `pkg/agentapi`, no `pkg/harness`), `cmd/agentkit.TestE2EServeAgent` (now through the client: streams, the 401, the conversation), `pkg/agentapi/wire.Example`.

## Notes

### Files

- **`pkg/agentapi/wire`** (new): the wire types, moved verbatim from `pkg/agentapi/wire.go` (`Event` and its `MarshalJSON`, `TimedMessage`, `TurnRecord`, `ToolRecord`, `Record`, `Error`, `Run`, `Conversation`, `RunRequest`, `ErrorResponse`, the type, code and status constants; `WireVersion` is `wire.Version`). Imports `pkg/llm` only. `example_test.go`.
- **`pkg/agentapi/wire.go`**: aliases (`type Event = wire.Event`, `const TypeRunStart = wire.TypeRunStart`, ...), so the server's API and every caller are unchanged, and the conversions from `harness` types, which stay here. T036's golden files are byte-identical (no `testdata` change; `TestEventJSONGolden` passes as it was).
- **`pkg/agentclient`** (new): `doc.go`, `client.go` (`Client`, options, `Stream`, `Run`, `GetRun`, `GetConversation`), `stream.go` (`Stream`, the SSE decoder), `errors.go` (`APIError`, sentinels, `Retry-After`); tests `helpers_test.go` (rig, fake transport, `referenceSSE`, `expectedRead`, `endsAs`), `client_test.go`, `fuzz_test.go`, `example_test.go`.
- **`cmd/agentkit/agent_e2e_test.go`**: `agentClient` now wraps `agentclient` (the hand-written SSE scanner `sseEvents` and the raw `do` are gone); the 401 check is `errors.Is(err, agentclient.ErrUnauthorized)`. `live_test.go` and `tavily_live_test.go` use it unchanged (vetted with `-tags live`).
- **`internal/archtest`**: a new rule field `SharedChildren` (`archtest.go`, one case in `archtest_test.go`): the subpackages a parent may import despite "a package never imports its own subpackages", for shared types split out for clients, never implementations. `rules.go`: `pkg/agentapi` → `pkg/agentapi/wire`; `ModuleImports` for `pkg/agentapi/wire` (`pkg/llm`) and `pkg/agentclient` (wire and `llm`; the server, harness, demo toolsets and fakes for its tests). `ModuleImports` cannot tell tests from code, so `TestClientDoesNotImportTheServer` checks the client's own dependencies. **`archtest.go` and `archtest_test.go` were outside this task's file list; the owner approved the change (2026-10-12) when the move hit the rule.**
- **Not changed:** the server's behavior, `pkg/llm`, `pkg/harness`. No new third-party dependency.

### Did the wire types move? Yes

`go list -deps ./pkg/agentapi` includes `pkg/harness` (and `component`, `clock`), so a client importing `pkg/agentapi` would compile the agent loop. `go list -deps ./pkg/agentapi/wire` is `pkg/tool`, `pkg/llm` and the standard library (plus `jsonschema-go` through `tool`); `pkg/agentclient` adds nothing of the module.

### Public API

```go
func New(baseURL string, opts ...Option) (*Client, error)    // ErrInvalidConfig
func WithToken(token string) Option
func WithHTTPClient(hc *http.Client) Option                    // default: no timeout
func WithUserAgent(ua string) Option                           // default "agentkit-agentclient"
func WithMaxResponseBytes(n int) Option                        // one SSE line, event or JSON body; default 16 MiB

type RunRequest = wire.RunRequest                              // {Message, ConversationID}
func (c *Client) Stream(ctx, RunRequest) (*Stream, error)      // POST /v1/runs, Accept: text/event-stream
func (c *Client) Run(ctx, RunRequest) (wire.Run, error)        // Accept: application/json, waits
func (c *Client) GetRun(ctx, id string) (wire.Run, error)
func (c *Client) GetConversation(ctx, id string) (wire.Conversation, error)

func (s *Stream) Next() (wire.Event, error)   // io.EOF after run_done/run_error
func (s *Stream) RawData() json.RawMessage    // the last event's JSON as sent (unknown fields kept)
func (s *Stream) RunID() string               // from Agentkit-Run-Id, else run_start
func (s *Stream) ConversationID() string      // from Agentkit-Conversation-Id, else run_start
func (s *Stream) Close() error                // before the end: cancels the run; idempotent; safe concurrently with Next

type APIError struct{ Status int; Code, Message string; Retryable bool; RetryAfter time.Duration }
// errors.Is(err, X) by code: ErrInvalidRequest 400, ErrUnauthorized 401, ErrNotFound 404, ErrRequestTooLarge 413,
// ErrConversationBusy 409, ErrConversationInterrupted 409, ErrOverCapacity 503, ErrShuttingDown 503.
// The client's own: ErrStreamTruncated, ErrBadResponse, ErrResponseTooLarge, ErrClosed.
```

### Decisions

- **A run's outcome is not a Go error.** `run_error` is an event `Next` returns with a nil error, then `io.EOF`; a failed JSON run is a `wire.Run` with `Status` `failed` and `Error`. Go errors are for the request (`*APIError`) and the transport or format (`ErrStreamTruncated`, `ErrBadResponse`, `ErrResponseTooLarge`, `ErrClosed`, the context's error). Each error `Next` returns matches exactly one of those (checked by `endsAs` in every byte-level test).
- **Unknown is not malformed.** An event of an unknown type is returned and never ends the stream; unknown fields are ignored (`RawData` keeps them). Data that is not a JSON object with a `type` is `ErrBadResponse`: the API promises JSON, so that is a broken stream, not a newer one. The JSON's `type` is authoritative; the SSE `event:` name, `id:` and `retry:` are not used (the API has no resume by `Last-Event-ID`, so there is nothing to reconnect to).
- **SSE parsing** follows the HTML standard's "Interpreting an event stream": a leading BOM is dropped; lines end with CRLF, LF or CR (a line ending in CR is returned at once; an LF that follows is skipped on the next read, so a live stream is never held waiting for the next byte); one space after the colon is dropped; data lines are joined with LF; a blank line dispatches only if there is data; an event cut off by the end is not dispatched. One limit (`WithMaxResponseBytes`) bounds a line, an event's data and a JSON body; the read buffer is 32 KiB, so the client reads at most the limit and the buffer past what it has used.
- **`Retry-After`** is read on every error response (the server sends it on 503s), in seconds or as an HTTP date taken relative to the response's `Date` (no clock skew; now if absent); past or unreadable is 0.
- **IDs are path-escaped** (`/` in an ID is `%2F`), so an ID never reaches another endpoint.
- **No timeout by default**: a stream lasts as long as its run. Bound requests with the context; a `Timeout` on the HTTP client would also cut streams.
- **A non-API error body** (a proxy's page) is an `*APIError` with an empty `Code` and the start of the body as `Message`; it matches no sentinel.

### Finding (reported, not fixed here): the additive rule stops at a message

`llm.Message.UnmarshalJSON` is strict by design (`pkg/llm/message.go`: "Unknown roles, block types and fields are errors ... content is never dropped silently"). Every event or resource that carries a message (`run_start`'s prompt, `turn_done`'s record, `run_done`'s final, `GET /v1/conversations/{id}`, a run's record) therefore fails to decode in today's client if a later server adds a field to a message or a new block type (images, citations, server tool use). The client then reports `ErrBadResponse` and stops, against the doc's "clients must ignore unknown fields, event types". T036's `TestEventJSONIsAdditive` grows only the event, `record`, `error` and `usage`, so it does not see this; `TestUnknownEventsAndFieldsPassThrough` here does the same, plus the prompt's wrapper, and stops short of messages for the same reason. Adding a field to `final` makes `run_done` fail (seen while writing that test).

Proposed fix, for the owner of `pkg/llm` and E09: give the wire a lenient decoding of messages, either an `llm.Message` decode mode (or a wire-only message type) that ignores unknown fields and keeps an unknown block as an opaque block with its raw JSON, and grow messages in both additive tests. Until then, T054 and T055 must treat a message-level change as a breaking change (a new `v`).

### Invariants (in every test that reads a stream)

- The events and raw data equal what an independent reading of the bytes gives (`referenceSSE` + `expectedRead`): against the real server (`rig.serverWrote`), the golden files, the framing cases and the fuzz target.
- A stream ends one way only (`endsAs`): `io.EOF` exactly after `run_done`/`run_error`, and no other end class at the same time (a cancelled stream is not also truncated).
- After the end, `Next` keeps returning the same end.
- Reading never goes past the limit and the read buffer (`TestLineLimit`, `FuzzSSE`).

### Mutation check

Each mutant was planted in `pkg/agentclient` alone, and `go test -race -count=2 ./pkg/agentclient/` (`FuzzSSE` on its seed corpus) was run against it; the tests that failed are listed. 26 mutants, 26 caught; the driver restored the package after each one (a `diff -r` against a copy before the run is clean). The four the task names come first.

| Mutant | Caught by |
|---|---|
| truncated stream reported as a clean end (io.EOF) | `FuzzSSE`, `TestSSEFraming`, `TestStreamCutBeforeItsEnd` |
| unknown event type treated as an error | `FuzzSSE`, `TestUnknownEventsAndFieldsPassThrough` |
| unknown event type treated as terminal | `FuzzSSE`, `TestUnknownEventsAndFieldsPassThrough` |
| unknown fields rejected | `TestUnknownEventsAndFieldsPassThrough` |
| Retry-After ignored | `TestAPIErrors`, `TestErrorsFromElsewhere` |
| Retry-After as an HTTP date ignored | `TestErrorsFromElsewhere` |
| Close not cancelling (only marks the stream closed) | `TestAPIErrors`, `TestCloseCancelsTheRun` |
| data: value loses all leading spaces, not one | `TestSSEFraming` |
| CRLF read as two line ends | `TestSSEFraming` |
| CR alone not a line end | `FuzzSSE`, `TestSSEFraming` |
| data lines joined with a space | `FuzzSSE`, `TestSSEFraming` |
| BOM not stripped | `TestSSEFraming` |
| event dispatched without its blank line at EOF | `TestSSEFraming` |
| line limit 1000x too lax | `FuzzSSE`, `TestLineLimit` |
| event data limit 1000x too lax | `TestLineLimit` |
| events read after the terminal one | `Example`, `FuzzSSE`, `TestGoldenStreams`, `TestSSEFraming`, `TestStreamedRunFollowUpAndJSON`, `TestUnknownEventsAndFieldsPassThrough` |
| run ID header not exposed | `TestCloseCancelsTheRun`, `TestStreamedRunFollowUpAndJSON` |
| IDs not taken from run_start without headers | `TestStreamCutBeforeItsEnd` |
| a cancelled context reported as truncation | `TestCloseCancelsTheRun` |
| Next after Close keeps reading | `TestNextAfterClose` |
| Accept header not set | `Example`, `TestRequests`, `TestStreamedRunFollowUpAndJSON` |
| token not sent | `TestAPIErrors`, `TestCloseCancelsTheRun`, `TestRequests`, `TestStreamedRunFollowUpAndJSON` |
| busy and interrupted sentinels swapped | `TestAPIErrors` |
| a proxy's error page parsed as an API error | `TestErrorsFromElsewhere` |
| content type of a stream not checked | `TestNotTheAPI` |
| a run ID not path-escaped | `TestRequests` |

One mutant was equivalent and is not counted: reading a comment line as a field (its name is empty, which no field matches, so it is skipped either way).

Two survived the first run and led to tests: a cancelled context reported as truncation (`endsAs` now requires exactly one end class) and `Next` after `Close` returning buffered events (`TestNextAfterClose`).

### For T054 (`agentkit chat`, `agentkit ask`)

- `agentkit ask --json`: print `s.RawData()` for each event, one per line: that is the SSE `data` exactly, unknown fields included (re-encoding a `wire.Event` would drop them). Exit 0 when the last event is `run_done`, 1 on `run_error`, 2 when `Stream` returns an error (`*APIError`, a transport error) or `Next` returns anything but `io.EOF` (`ErrStreamTruncated`, `ErrBadResponse`, ...).
- Ctrl-C: call `s.Close()` (safe from a signal goroutine while `Next` blocks); the server cancels the run. If the run stopped before answering, the conversation is now `interrupted`: the next message gets `ErrConversationInterrupted`; `chat` should offer to resume with an empty `RunRequest.Message` first.
- Retries: `ErrOverCapacity` and `ErrShuttingDown` carry `APIError.RetryAfter`; `ErrConversationBusy` means another run of the conversation is going on. Do not retry `ErrUnauthorized` or `ErrInvalidRequest`.
- `New` validates `--server`/`AGENTKIT_SERVER` (absolute http(s), path prefix allowed); `WithToken(os.Getenv("AGENTKIT_API_TOKEN"))`; `WithUserAgent("agentkit-cli/<version>")`.
- Use the context, not an HTTP client `Timeout`, for an overall limit.
- Messages are not additive yet (Finding above); a server newer than the CLI that changes message JSON will make `Next` fail with `ErrBadResponse`.

### For T055 (console chat)

- The browser cannot use `EventSource` for runs: it only does GET (and cannot set `Authorization`, whatever T061's login uses), while a run is a POST. Use `fetch` with a `ReadableStream` reader and port the framing rules above. `TestSSEFraming`'s cases and `referenceSSE` are the spec to port (CR, CRLF, multi-line data, one space after the colon, comments, BOM, an event cut off at the end is dropped); add a line limit there too.
- Cancel with an `AbortController`: like `Close`, the server ends the run `cancelled`, and the conversation may be `interrupted` (offer "resume", an empty message).
- Unknown event types: render nothing (or a neutral line), never stop the stream; only `run_done`/`run_error` end it. Parse messages leniently in TypeScript (ignore unknown fields and block types), which avoids the Go client's limit from the Finding.
- Errors: the HTTP error body is `{"v":1,"error":{"code","message","retryable"}}`; branch on `code`, show `message`, honour `Retry-After` on 503.
- The Go-side browser tests can seed and inspect state with this client (`Run`, `GetConversation`) against the same server the page talks to.
