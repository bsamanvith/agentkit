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

The harness takes a transcript in and gives turns out; storing and reloading them is the layer above. Agreed shape:

- **One append-only log, two read paths.** `messages(conversation_id, seq, run_id, turn, role, created_at, text, content_json, usage and timing columns)`. `content_json` is the exact `llm.Message` and is the only column read back into a request; the rest is for display and queries. `tool_calls` holds timing and status only (input and output are already in the messages). `UNIQUE(conversation_id, seq)`: appends carry the expected head, so a stale or racing writer fails instead of interleaving.
- **History (UI)**: every message, newest first, keyset-paged on `seq`, with timestamps; tool calls collapsed; opaque blocks never shown. Pages below the head never change and can be cached as immutable.
- **Context (agent)**: the latest compaction's summary plus the messages after it. Each compaction is self-contained (it summarizes the previous summary plus the messages up to its cut), lives in its own `compactions` row (not in `messages`), and is stored as a user/assistant pair so roles still alternate. Cuts fall just before a user prompt, never between a tool call and its result. The conversation row points at the current compaction, so loading context is one primary-key read plus one range scan, bounded by the context size, not the history.
- **When to compact**: after a run, if its last prompt (`InputTokens + CacheReadTokens + CacheWriteTokens` of the last turn, from the record) passes a threshold; or when a run fails with `llm.ErrContextTooLong` (its transcript ends on a whole turn, so it can be compacted and resumed). Compaction never touches `messages`, so it cannot race with new messages; a compare-and-set on the conversation row settles two compactions. Compaction inside a run is out of scope.
- **Caching active chats**: an in-process LRU of decoded context per conversation, checked against the conversation row (head and compaction) before use. After each turn the delta is appended to the entry (the log is append-only, so nothing is invalidated); a new compaction replaces it. Route a conversation to the same replica so the cache hits. No shared cache until measurements ask for one.
- Append-only history keeps each request's prefix identical, so provider prompt caching works across turns; a compaction resets it once.

Candidates to promote into `pkg/llm` once a second caller appears: `Usage.Add`, `Message.ToolUses()` (private helpers here).
