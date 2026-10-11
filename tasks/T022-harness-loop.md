---
id: T022
title: "pkg/harness: the agent loop"
epic: E04
status: todo
depends_on: [T020]
---

# T022: `pkg/harness`

## Context

The agent loop from [PLAN.md `harness`](../docs/PLAN.md#harness): model → tools → results → model, until the model is done, a budget runs out, or the context is cancelled. It depends only on `llm.Model` and `tool.Registry`.

The harness keeps a run's memory in process and hands it out; it never stores anything. Two things come out of a run:

- the **transcript**: the `llm.Message`s the model sees, exact and append-only, resumable;
- the **record**: what happened around them (timestamps, model and tool timings, usage, stop reasons), for the UI, billing and traces.

They are separate types because `llm.Message` is strict JSON sent to the provider: a timestamp inside it would change `pkg/llm` and make "the same message" depend on a clock. Persisting both, showing history and compacting long conversations belong to the layer above (see "For E05" in Notes).

## Design

### API

- `harness.New(model llm.Model, tools tool.Registry, opts...) (*Harness, error)`. Options: `WithSystem`, `WithMaxTurns` (model calls per `Run`, default 25), `WithMaxTotalTokens` (0 = unlimited), `WithMaxParallelTools` (default 8), `BeforeTool` (a non-nil error denies the call), `AfterTool` (can replace the result; its `ToolUseID` is kept), `WithNow` (the clock, `func() time.Time`; `time.Now` by default; `pkg/clock` does not exist yet).
- `Run(ctx, Input) iter.Seq2[Event, error]`, and `harness.Collect(seq) (Result, error)`.
- `Input{Message llm.Message; Transcript Transcript; At time.Time}`: the new user message, the prior transcript (for follow-ups and resume), and when the user sent the message (default: now).
- `Transcript` is `[]llm.Message`, JSON through `llm.Message`'s own encoding (Opaque blocks byte-exact), with `Clone` and `Validate`.
- `Result{Transcript, Final llm.Message, Usage, Turns, Record}`.

### Events

One flat `Event` struct; every event has `Kind`, `Turn` and `At` (from the clock).

| Kind | Carries |
|---|---|
| `EventTextDelta` | `Text` |
| `EventToolStart` | `Call` (index in the turn's tool calls), the `llm.ToolUse` |
| `EventToolDone` | `Call`, the `llm.ToolResult` (`IsError`), `Duration` |
| `EventTurnDone` | a `TurnRecord`: the messages this turn appended, each with its time; request, first-token and done times; `Usage`; `StopReason`; per tool `{ToolUseID, Name, StartedAt, Duration, IsError}` |
| `EventRunDone` | the `Result` |

`EventTurnDone` carries the **delta**, not the whole transcript: a store writes one turn per transaction, and `harness.Step` (T024) saves after every turn by appending the delta to the transcript it started from. Deltas carry no index; the store numbers messages.

Message times: a user message is stamped when the run receives it (or `Input.At`), an assistant message when its stream finishes, a tool-results message when the last tool finishes. Durations use the monotonic clock.

### Loop

1. Validate the input. A transcript ending on a user message may be resumed without a new message; a transcript ending on a user message plus a new message is rejected (`ErrInvalidInput`). `Validate` checks roles and that tool results answer the preceding tool calls in order; it does not require the first message to be a real prompt, so a compaction summary can start a transcript.
2. `tools.List` once per `Run` (stable tool definitions across turns, and identical requests on resume).
3. Before each model call: check `ctx`, then the budgets.
4. Stream the reply. Forward text deltas as events; take the message only from `EventMessageDone`.
5. `StopToolUse`: run every `ToolUse`, one goroutine each, bounded by a semaphore channel and a `sync.WaitGroup`. Each worker writes `results[i]`, so results go back in call order whatever the finish order. Workers send their events through a channel buffered to 2 per call (never blocks); only the `Run` goroutine yields.
6. Append the assistant message and one user message of `ToolResult`s together, emit `EventTurnDone`, repeat. `StopEndTurn` appends and finishes.

**Turns are atomic.** The transcript only ever grows by whole turns, so it always ends on a clean boundary and can be resumed. A run stopped mid-turn (error, cancellation, consumer stopping) returns the transcript of its completed turns. A final `max_tokens`, refusal or other-stop reply is appended, since it ends the run.

**Budgets are checked before each model call**, per `Run` (not over the prior transcript). The tools of the last permitted turn still run, and the token budget can be overshot by one turn; in return `ErrBudgetExceeded` leaves a transcript that resumes with a larger budget. Tokens per turn = `InputTokens + CacheReadTokens + CacheWriteTokens + OutputTokens`.

### Tool results

- Output JSON becomes `ToolResult.Content` as text.
- `tool.ErrInvalidInput`, `tool.ErrToolFailed` (including panics), `tool.ErrNotFound`, a `BeforeTool` denial, and `context.DeadlineExceeded` while the run's own context is still live (a `tool.Timeout`) become `IsError` results whose `Content` is the error message, never a stack. The run continues.
- If the run's context is done, the run ends with the context's error.
- Any other error is an infrastructure fault (a remote registry down) and ends the run.
- `AfterTool` sees every result, including errors. Hooks run on tool goroutines and must be safe for concurrent use; a panicking hook ends the run with an error instead of crashing the process.

### Errors

Every error from `Run` is a `*RunError` with `Transcript()`, `Usage()`, `Turns()`, `Record()` and `Unwrap()`, so `errors.Is` and `llm.IsRetryable` work through it. Causes:

- `ErrBudgetExceeded` (turns or tokens)
- `ErrMaxTokens` (the model hit its output limit)
- `ErrRefused`, matched by a `*RefusalError{Category, Explanation}`
- `ErrUnexpectedStop` (`StopToolUse` with no tool calls, `StopOther`, duplicate tool-use IDs, a stream without a final message)
- `ErrInvalidInput`
- model errors, wrapped and never retried here (retries are a later wrapper)
- the context's error

## Acceptance criteria

All tests use `llmtest.Fake` and `tooltest.Fixtures` (or small `tool.Func`s).

- [ ] A run with no tool calls returns the model's answer and one turn.
- [ ] Two tool calls in one turn run concurrently (both start before either finishes) and their results come back in one message in call order.
- [ ] A failing tool and an unknown tool name become `IsError` results; the run continues.
- [ ] Every request the model receives extends the previous one; earlier messages are never changed (checked on every turn of a multi-turn run).
- [ ] Max turns and max tokens stop the run with `ErrBudgetExceeded` and keep the transcript.
- [ ] Cancelling the context mid-tool stops the run with the context error and no further model calls.
- [ ] `MaxTokens` and `Refusal` stop reasons end the run with their typed errors.
- [ ] `BeforeTool` denial and `AfterTool` replacement are visible in what the model receives.
- [ ] Events arrive in order, and text deltas concatenate to the final text.
- [ ] A run resumed from its serialized transcript continues with the same next request as an uninterrupted run.
- [ ] Each turn reports its appended messages with timestamps, its model and tool timings, usage and stop reason; applying the turn deltas to the input transcript gives the final transcript.
- [ ] Stopping the iteration early cancels the model stream and running tools, and no goroutine outlives the run.
- [ ] Model errors are returned wrapped (`llm.IsRetryable` still works) and not retried.
- [ ] A transcript that fails `Validate` is rejected before any model call (fuzzed).

## Notes

- archtest: `pkg/harness` imports `pkg/llm` and `pkg/tool` only (never an adapter).
- Tests are not parallel: each checks that the goroutine count returns to its baseline (`testkit.Eventually`), which parallel tests would disturb.

### For E05 (store, conversation memory)

The harness takes a transcript in and gives turns out; storing and reloading them is the layer above. It must work on any backend (SQLite, Postgres, MongoDB, Cassandra/Scylla, S3, a local directory), so it rests on the one primitive all of them have: **create a key only if it is absent**.

- **Streams of immutable entries.** A conversation is append-only streams of entries numbered densely from 1, each written once and never updated: `messages` (entries: a user prompt, then one per completed turn holding its messages, record and timestamps, then a run-end entry with status and usage) and `compactions`. One entry per turn makes a turn atomic on every backend without transactions.
- **Backend contract** (sketch; the interface lives in `pkg/store`, implementations in subpackages, one conformance suite in `storetest`):

  ```go
  Create(ctx, stream string, n uint64, e Entry) error        // ErrExists if n is taken
  Read(ctx, stream string, from uint64, limit int) ([]Entry, error) // ascending, stops at a gap
  Head(ctx, stream string) (uint64, error)                   // highest n, 0 if empty
  ```

  `Entry` is typed (messages as `llm.Message` JSON, timestamps, usage), so a SQL backend can map it to columns and a key-value backend can store it as one JSON document. The store defines its own entry type and never imports `harness`; the service that runs agents converts.
- **Create-if-absent per backend**: SQLite/Postgres `INSERT` on primary key `(stream, n)`; MongoDB `insertOne` with `_id = stream/n` (duplicate key); Cassandra/Scylla `INSERT … IF NOT EXISTS` (lightweight transaction, once per turn), partition bucketed by `n / 1000` so partitions stay small; S3 `PutObject` with `If-None-Match: *` (also GCS `ifGenerationMatch=0`, Azure `If-None-Match`; check S3-compatible stores support it), keys zero-padded so they list in order; local directory: write a temp file, fsync, `link` it to the final name (fails if it exists; `rename` would overwrite).
- **No mutable pointers.** The head is the highest entry, the current compaction is the highest compaction entry. Racing writers both try `n = head+1` and one gets `ErrExists`, reloads and retries; two compactions race the same way. Nothing needs a compare-and-set on a mutable row.
- **History (UI)**: entries `head-49 … head`, flattened to messages, newest first; dense numbers turn every page into one ascending `Read`. Entries never change, so pages below the head can be cached as immutable. Opaque blocks are never shown.
- **Context (agent)**: the latest compaction's summary (a user/assistant pair, so roles alternate) plus the message entries after its cut. Each compaction is self-contained (previous summary plus the messages up to its cut). Cuts fall just before a user prompt, never between a tool call and its result. Loading context costs one `Head` plus one `Read` of the tail: bounded by the context, not the history.
- **When to compact**: after a run, if its last prompt (`InputTokens + CacheReadTokens + CacheWriteTokens` of the last turn) passes a threshold; or when a run fails with `llm.ErrContextTooLong` (its transcript ends on a whole turn, so it can be compacted and resumed). Compaction never writes message entries, so it cannot race with new messages. Compaction inside a run is out of scope.
- **Caching active chats**: an in-process LRU of decoded context per conversation, keyed by the message head and compaction number it was built from. Before use, check that entry `head+1` and compaction `k+1` do not exist (two point reads, cheap on every backend); after each turn, append the delta (nothing is ever invalidated). Route a conversation to the same replica so the cache hits. No shared cache until measurements ask for one.
- Append-only history keeps each request's prefix identical, so provider prompt caching works across turns; a compaction resets it once.
- **Not covered by the contract**: listing a user's conversations, search, analytics. Those are separate, optional interfaces (or an export to a warehouse), so a backend that only has keys can still hold conversations.

Candidates to promote into `pkg/llm` once a second caller appears: `Usage.Add`, `Message.ToolUses()` (private helpers here).
