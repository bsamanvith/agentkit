---
id: T053
title: "pkg/agentclient: the Go client for the agent API"
epic: E11
status: todo
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

## Acceptance criteria

- [ ] Against a real `agentapi.Server` (in-process `httptest`, fake model, demo tools): a streamed run yields exactly the server's events in order, ending with `run_done`; a follow-up in the same conversation works; JSON mode returns the same `Run`.
- [ ] T036's golden SSE files (`pkg/agentapi/testdata/*.sse`) parse to the expected events (the client and server agree on the wire, by the same fixtures).
- [ ] Every error code in T036's table maps to its sentinel, with `RetryAfter` set for 503s (table test against the real server where it can produce the error, a stub otherwise).
- [ ] `Close()` mid-stream cancels the run on the server (the server's run ends `cancelled`); a stream cut without a terminal event gives `ErrStreamTruncated`.
- [ ] Unknown event types and fields pass through (the additive rule), checked with T036's `TestEventJSONIsAdditive` fixtures.
- [ ] `FuzzSSE`: the parser never panics, never reads past its line limit, and never invents events.
- [ ] Mutation list recorded.
