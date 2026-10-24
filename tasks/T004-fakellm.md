---
id: T004
title: fakellm scripted model server
epic: E01
status: done
depends_on: [T003]
---

# T004: fakellm scripted model server

## Context

Local runs and e2e tests must be deterministic and must not need an API key. `fakellm` answers model requests from a script, so the harness can be exercised end to end, including tool calls and streaming.

## Scope

**Decision (planner, after T024):** `fakellm` is still needed, because Compose and the smoke suite need a model endpoint that runs as its own container with no key, and T024's replay server exists only inside a test. It is not a second implementation. Start from what exists: T024's replay end-to-end tests (`cmd/agentkit/e2e_test.go`) already run a local server that replays recorded Claude responses (`testdata/e2e/<scenario>/NN.http`). Extract that server into `cmd/fakellm` rather than writing a second one, so recorded fixtures and scripted replies share one format where possible.


- `cmd/fakellm`: HTTP server implementing a subset of the Anthropic Messages API: `POST /v1/messages`, non-streaming and SSE streaming, `tool_use` content blocks.
- Script format: JSONL in `deploy/local/fakellm/`, each entry = optional match (substring of last user message or tool result) + response (text and/or tool calls). First match wins; a default fallback response.
- Select a script per request with an `X-Fakellm-Script` header, else the server default (`FAKELLM_SCRIPT`).
- Logs each request so e2e failures are debuggable.

## Out of scope

- OpenAI-compatible mode (open question in ENGINEERING.md §5).
- Token accounting beyond fixed fake usage numbers.

## Acceptance criteria

- [x] A matching entry returns its text response in Anthropic response shape. — `internal/fakellm.TestTextEntry`
- [x] A tool-call entry returns a `tool_use` block; the follow-up request with the `tool_result` matches the next entry. — `internal/fakellm.TestToolCallRoundTrip`
- [x] `stream: true` returns a valid SSE event sequence (`message_start` ... `message_stop`) — checked with a golden file. — `internal/fakellm.TestStreamGolden` (`internal/fakellm/testdata/stream.golden`)
- [x] No match and no fallback returns a 4xx with a clear error body. — `internal/fakellm.TestNoAnswerIsAClientError`
- [x] The official Anthropic Go SDK, pointed at `fakellm`, can complete a request (proves wire compatibility). — `internal/fakellm.TestOfficialSDK` (a streamed tool call accumulated by the SDK, its result sent back with SDK params, a non-streamed answer)
- [x] The `fakellm` image builds, runs as non-root and its size is recorded (T005: "T004 checks its image the same way"). *Not run here: no Docker in this session. CI's `image` job builds it and checks it against its own 12 MB budget (`IMAGE_BUDGET_MB_fakellm`) on every push; the binary is measured in docs/SIZES.md. By hand: `docker run --rm fakellm version` and the healthcheck.* **Ticked 2026-10-24** from CI on bsamanvith/agentkit#113 (run 40099895001): the `image` job builds it with `make image BIN=fakellm` and passes `make image-size BIN=fakellm` (its 12 MB budget, which records the size in the job log); it runs as uid 65532 (`USER 65532:65532` on distroless `nonroot`, the Dockerfile every image shares).

## Notes

### What was built

- **`internal/fakellm`** (the library: an `http.Handler`, no listener): `ParseScript(name, r, fsys)`, `LoadDir(dir)`, `New(scripts, WithDefault, WithLogger)`, `ServeFixture(w, r, raw)`, `ErrScript`, `ScriptHeader`. Standard library only; archtest pins its module imports to `internal/testkit` (tests) and allows the Anthropic SDK in its tests only.
- **`cmd/fakellm`**: `fakellm [serve]`, `fakellm healthcheck`, `fakellm version`, so the shared Dockerfile's `CMD ["serve"]`, `HEALTHCHECK /app healthcheck` and `-X main.version` work unchanged. Logs with `log/slog` text to stderr: one line at start (address, directory, default script, scripts) and one per request (script, model, stream, message count, an excerpt of the last user text, the script line that answered or the error, status, stop reason).
- **`deploy/local/fakellm/default.jsonl`** (+ `recorded.http`, a copy of `cmd/agentkit/testdata/e2e/run/02.http`): "multiply" calls the `calculate` tool, its `{"result":42}` is answered, "recorded" serves the recorded fixture, anything else gets a hello. Checked by `TestShippedScripts`, and by hand: `agentkit run "please multiply 6 by 7"` against `fakellm` ran two turns (tool call, `{"result":42}`, answer) and exited 0.
- **Extraction from the replay test:** the part of `cmd/agentkit/e2e_test.go`'s replay server that serves a recorded `NN.http` is now `fakellm.ServeFixture`, and a script entry can name such a fixture (`{"fixture": "x.http"}`), so recorded and scripted replies share one format. The rest of that server (recording from the real API, holding and stalling requests, grouping requests by process) is test machinery and stays in the test.

### Decisions (defaults picked here)

- **Script format.** One JSON object per line; blank lines and lines starting with `#` are skipped (JSONL has no comments, and the shipped scripts need them). Unknown fields are an error, so a typo (`"matc"`) fails at load, not as a silent fallback. An entry has exactly one of `response` (`text`, `tool_calls` [`id`?, `name`, `input` object], `stop_reason`?) and `fixture` (a path relative to the script directory).
- **Fallback is position-free.** The one entry without `match` answers only when no entry with a match does, wherever it sits; a second one is a load error. "First match wins" applies to entries with a match. An empty `match` is an error (it would match everything).
- **Match text** is the last `user` message: its text blocks and the text of its tool results (string or text-block content), joined by newlines. Case-sensitive substring. Matching is stateless (no per-conversation cursor), so concurrent clients and retries get the same answers; a multi-step script is written as entries that match each step's input, typically the previous tool result.
- **Ids are deterministic:** `msg_fake_<hash>` and `toolu_fake_<hash>_<n>`, the hash over the conversation's messages (not the `stream` flag), so a request gets the same answer streamed or not, every run, and different conversations get different tool ids. A scripted `id` is used as given.
- **Fixed usage:** input 10, output 5 (`message_start` reports output 1, like the API). The model is echoed from the request.
- **Streaming shape:** `message_start`, `ping`, per block `content_block_start` / two deltas (text and tool input are split in two at a rune boundary, so a client's accumulation is exercised) / `content_block_stop`, then `message_delta` and `message_stop`.
- **Errors are the API's shape** (`{"type":"error","error":{"type","message"}}`) and 4xx (400 `invalid_request_error`, 404 `not_found_error`, 405, 413), which the SDK does not retry. The message names the script, the last user text and the fix.
- **Script selection:** the `X-Fakellm-Script` header, else `FAKELLM_SCRIPT` (default `default`); `New` without a default uses the only script, if there is one. Script names are `[A-Za-z0-9][A-Za-z0-9_.-]*`, so the header never reaches the file system.
- **Scripts load once, at start,** from `FAKELLM_SCRIPT_DIR` (default `deploy/local/fakellm`, relative to the working directory; `/deploy/local/fakellm` in the image, whose working directory is `/`). A bad script, a missing directory or a default that does not exist stops startup. Fixtures are read through `os.Root`, so neither `..` nor a symbolic link reaches outside the directory.
- **Address:** `FAKELLM_ADDR`, else `AGENTKIT_ADDR` (the shared image sets `:8080`), else `127.0.0.1:8090`.
- **Place:** the handler is `internal/` (only this module's binaries and tests use it); `pkg/` can come later if users want it in their own tests.

### How fakellm relates to the other fakes

| Fake | Where it runs | Scripted by | Use |
|---|---|---|---|
| `pkg/llm/llmtest.Fake` | in process, behind `llm.Model` | Go (`llmtest.Reply`) | unit tests of the harness, runner, API; no wire format at all |
| `pkg/llm/anthropic` replay (`replay_test.go`) | `httptest`, in the adapter's tests | recorded `.http` fixtures in order, with a stall marker | the adapter and its conformance run |
| `cmd/agentkit` replay (`e2e_test.go`) | `httptest`, the binary as processes | recorded `.http` fixtures in order; records from the real API | replay end-to-end tests; serves fixtures with `fakellm.ServeFixture` |
| `web/apps/console/e2e/fake-model.ts` | Node process | TypeScript (echo, xss, stall) | the console's browser tests and `make smoke` |
| `fakellm` | its own process or image | JSONL scripts, entries may name `.http` fixtures | Compose/k8s, the smoke suite, local runs without a key |

`llmtest` is a test helper (non-test code may not import it), so fakellm does not render from `llmtest.Reply`; it has its own small renderer, held to the API's shape by the SDK test and the stream invariants.

### Invariants (checked by `exchange` in every test that sends a request)

Every request is sent twice plain and once streamed: the same request gets the same bytes; a 200 is a well-formed message (non-empty text blocks, `toolu_` ids unique within it, object inputs, a known stop reason, `tool_use` exactly when it calls tools, usage); the stream is a valid event sequence (`message_start` first and once with no content, blocks started in index order and one at a time, deltas of the right kind for the open block, `message_delta` then `message_stop` last) and accumulates to exactly the plain message; an error is a 4xx in the API's error shape, the same streamed or not. `FuzzServe` holds any request body to the same checks (no panic, no 5xx).

### Mutation check

Each bug was planted alone (the code compiled), `go test -count=1 ./internal/fakellm/ ./cmd/fakellm/` run, and the code restored. One mutant first survived (text split inside a rune: the golden text split on a boundary by luck); the golden text now has a multi-byte rune at its middle. One was caught only by the golden file (tool ids that repeat across conversations); `TestToolCallRoundTrip` now checks that two conversations get different ids.

| Planted bug | Failing tests |
|---|---|
| The last matching entry wins instead of the first | `TestTextEntry` |
| The fallback is kept in file order (a fallback above answers before later matches) | `TestTextEntry`, `TestParseScriptErrors` |
| The first user message is read instead of the last | `TestToolCallRoundTrip`, `TestOfficialSDK`, `TestShippedScripts` |
| Tool results are not read for matching | `TestToolCallRoundTrip`, `TestOfficialSDK`, `TestShippedScripts` |
| No match and no fallback answers 200 with an empty message | `TestNoAnswerIsAClientError`, `FuzzServe` |
| Tool ids repeat across conversations | `TestToolCallRoundTrip`, `TestStreamGolden` |
| `stop_reason` stays `end_turn` with tool calls | `TestToolCallRoundTrip`, `TestOfficialSDK`, `TestStreamGolden`, `TestConcurrentRequests`, `FuzzServe` and more |
| `X-Fakellm-Script` ignored | `TestScriptHeader`, `TestNoAnswerIsAClientError` |
| Stream without `message_stop` | `TestStreamGolden`, `TestTextEntry`, `TestToolCallRoundTrip`, `FuzzServe` and more |
| Stream without `content_block_stop` | the same |
| Ids derived from the whole body (the `stream` flag included) | `TestTextEntry`, `TestToolCallRoundTrip`, `TestStreamGolden`, `Example` and more |
| Text split inside a multi-byte rune | `TestStreamGolden` |
| Fixtures read through `os.DirFS` (a symbolic link escapes the directory) | `TestLoadDir` |
| Requests not logged | `TestServeAndHealthcheck` |
| `AGENTKIT_ADDR` (the image's address) ignored | `TestServeAndHealthcheck` |

### Size

`fakellm` links the standard library only: a 7,602,336-byte stripped binary (docs/SIZES.md). Its image is measured by CI's `image` job against a budget of its own, 12 MB (`IMAGE_BUDGET_MB_fakellm` in the Makefile): a test-only image that should stay the binary plus the base. No agentkit binary or image changes: `internal/fakellm` is imported only by `cmd/fakellm` and by `cmd/agentkit`'s replay test.

### For T005, T006 and T007

- **Image:** `make image BIN=fakellm` (no Dockerfile change). Mount the scripts at `/deploy/local/fakellm` or set `FAKELLM_SCRIPT_DIR`; without scripts it exits at start with an error naming the variable. It listens on `:8080` (the image's `AGENTKIT_ADDR`); `/app healthcheck` checks `/healthz`. Point agentkit at it with `ANTHROPIC_BASE_URL=http://fakellm:8080` and any `ANTHROPIC_API_KEY`.
- **Smoke suite:** `make smoke` runs on the console's `fake-model.ts`. fakellm can take its place once it has what that fake has and fakellm lacks: a stall (start an answer, never finish it; and a count of stalls whose request ended, `GET /stalls`) and answers that depend on the request's tool list (the echo tool's prefixed name). Neither was in this task's scope; a `"stall": true` response field and a `{tool:suffix}` name are small additions.
- agentkit sends no `X-Fakellm-Script` header, so a process under test uses the server's default script; pick per deployment with `FAKELLM_SCRIPT`.
