---
id: T022
title: "pkg/harness: the agent loop"
epic: E04
status: done
depends_on: [T020]
---

# T022: `pkg/harness`

## Context

The agent loop from [PLAN.md `harness`](../docs/PLAN.md#harness): model → tools → results → model, until the model is done, a budget runs out, or the context is cancelled. It depends only on `llm.Model` and `tool.Registry`.

The harness is the **core**: it holds the behavior of the loop and nothing with a backend or a policy that varies. Storage, history, compaction, caching, providers, tool backends, tracing and retries live in other packages and reach the loop only through the two interfaces, the options, the hooks and the events (see "Pluggability" below).

A run's memory is a value the harness takes in and hands out. Two things come out of a run:

- the **transcript**: the `llm.Message`s the model sees, exact and append-only, resumable;
- the **record**: what happened around them (timestamps, model and tool timings, usage, stop reasons), for the UI, billing and traces.

They are separate types because `llm.Message` is strict JSON sent to the provider: a timestamp inside it would change `pkg/llm` and make "the same message" depend on a clock.

## Design

### API

- `harness.New(model llm.Model, tools tool.Registry, opts...) (*Harness, error)`. Options: `WithSystem`, `WithRequest(RequestOptions{MaxTokens, Provider})` (what every model request carries besides system prompt, messages and tools), `WithMaxTurns` (model calls per `Run`, default 25), `WithMaxTotalTokens` (0 = unlimited), `WithMaxParallelTools` (default 8), `WithBeforeTool` (a non-nil error denies the call), `WithAfterTool` (can replace the result; its `ToolUseID` is kept), `WithNow` (the clock, `func() time.Time`; `time.Now` by default; `pkg/clock` does not exist yet).
- `Run(ctx, Input) iter.Seq2[Event, error]`, and `harness.Collect(seq) (Result, error)`.
- `Input{Message llm.Message; Transcript Transcript; At time.Time}`: the new user message, the prior transcript (for follow-ups and resume), and when the user sent the message (default: now).
- `Transcript` is `[]llm.Message`, JSON through `llm.Message`'s own encoding (Opaque blocks byte-exact), with `Clone` and `Validate`.
- `Result{Transcript, Final llm.Message, Usage, Turns, Record}`.

### Events

One flat `Event` struct; every event has `Kind`, `Turn` and `At` (from the clock).

| Kind | Carries |
|---|---|
| `EventRunStart` | `Prompt`: the user message the run added (nil when it resumed without one) |
| `EventTextDelta` | `Text`, and `Block`, the index of the text block of the reply |
| `EventToolStart` | `Call` (index in the turn's tool calls), a copy of the `llm.ToolUse` |
| `EventToolDone` | `Call`, the `llm.ToolResult` (`IsError`), `Duration` |
| `EventTurnDone` | a `TurnRecord`: the messages this turn appended, each with its time; request, first-event and done times (`RequestedAt`, `FirstEventAt`, `DoneAt`); `Usage`; `StopReason`; per tool `{ToolUseID, Name, StartedAt, Duration, IsError}` |
| `EventRunDone` | the `Result` |

Grammar: `RunStart ( TextDelta* [ToolStart … ToolDone] TurnDone )* RunDone`. Each call's `ToolStart` comes before its `ToolDone`; a call that never began because the run ended while it waited for a slot has neither. Every started call is finished before its turn's `TurnDone`. Events are always delivered on the goroutine that iterates `Run`; events of parallel calls arrive in the order the calls finish, and say which call they are for.

`EventTurnDone` carries the **delta**, not the whole transcript: a store writes one turn per transaction, and `harness.Step` (T024) saves after every turn by appending the delta to the transcript it started from. Deltas carry no index; the store numbers messages. Every message in the transcript is announced exactly once (the prompt in `EventRunStart`, the rest in `EventTurnDone`), including the reply of a turn that ends the run with an error.

Message times: a user message is stamped when the run receives it (or `Input.At`), an assistant message when its stream finishes, a tool-results message when all the turn's tools have finished.

Everything in an event, `Result` or `RunError` is a copy the consumer may keep or write to. The harness never hands out memory that belongs to the transcript.

### Loop

1. Validate the input. A transcript ending on a user message may be resumed without a new message; a transcript ending on a user message plus a new message is rejected (`ErrInvalidInput`). The new message is validated as part of the whole transcript. `Validate` checks roles, that every message is well formed (blocks of the right kind for the role, non-empty IDs and names, tool input and opaque data that are JSON), and that tool results answer the preceding tool calls in order. It does not require the first message to be a real prompt, so a compaction summary can start a transcript.
2. `tools.List` once per `Run` (stable tool definitions across turns, and identical requests on resume).
3. Before each model call: check `ctx`, then the budgets.
4. Stream the reply. Forward text deltas as events; take the message only from `EventMessageDone`. A reply the transcript could not hold (it fails the same per-message check as `Validate`) is `ErrUnexpectedStop` and is never committed.
5. `StopToolUse`: run every `ToolUse`, bounded by a semaphore. A small dispatcher hands out the slots in the order of the calls (with one slot the calls run one after the other, in order) and each call runs on its own goroutine. Each worker writes its own slot of the results, so results go back in call order whatever the finish order. Workers send their events through a channel with room for every event they will send (never blocks).
6. Append the assistant message and one user message of `ToolResult`s together, emit `EventTurnDone`, repeat. `StopEndTurn` appends and finishes.

**Turns are atomic.** The transcript only ever grows by whole turns, so it always ends on a clean boundary and can be resumed. A run stopped mid-turn (error, cancellation, consumer stopping) returns the transcript of its completed turns. A turn whose calls all finished is complete even if the context is cancelled just after: its tools have run, and it is kept. A final `max_tokens`, refusal or other-stop reply is appended, since it ends the run.

**Budgets are checked before each model call**, per `Run` (not over the prior transcript). The tools of the last permitted turn still run, and the token budget can be overshot by one turn; in return `ErrBudgetExceeded` leaves a transcript that resumes with a larger budget. Tokens per turn = `InputTokens + CacheReadTokens + CacheWriteTokens + OutputTokens`.

### Tool results

- Output JSON becomes `ToolResult.Content` as text.
- `tool.ErrInvalidInput`, `tool.ErrToolFailed` (including panics), `tool.ErrNotFound`, a `BeforeTool` denial, and `context.DeadlineExceeded` while the run's own context is still live (a `tool.Timeout`) become `IsError` results whose `Content` is the error message, never a stack. The run continues.
- If the run's context is done, the run ends with the context's error.
- Any other error (a remote registry down) ends the run, wrapped with the tool's name, and cancels the other calls of the turn instead of waiting for them. The cause is reported, not the cancellation of a sibling.
- A hook (or a registry that does not protect itself) that panics ends the run with a `*tool.PanicError` (`errors.Is(err, tool.ErrPanic)`; the value, not the stack, is in the message) instead of crashing the process.
- `AfterTool` sees every result, including errors. Hooks run on tool goroutines and must be safe for concurrent use.
- Hooks, the event consumer and the registry each get their own copy of a call's input.

### Errors

Every error from `Run` is a `*RunError` with `Transcript()`, `Usage()`, `Turns()`, `Record()` and `Unwrap()`, so `errors.Is` and `llm.IsRetryable` work through it. `Usage()` includes a turn that got a reply but did not complete, which is in no `TurnRecord`: billing from events alone undercounts such a run. Causes:

- `ErrBudgetExceeded` (turns or tokens)
- `ErrMaxTokens` (the model hit its output limit)
- `ErrRefused`, matched by a `*RefusalError{Category, Explanation}`
- `ErrUnexpectedStop` (`StopToolUse` with no tool calls, `StopOther`, a malformed reply, a stream without a final message)
- `ErrInvalidInput`
- `ErrInvalidConfig` (from `New`, not a run)
- model errors, wrapped and never retried here (retries are a wrapper around the model)
- the context's error
- `tool.ErrPanic` (a panic in a hook)

### Pluggability

| Concern | Where it plugs in | Needs a harness change? |
|---|---|---|
| Model providers | `llm.Model` (adapters in `pkg/llm/<name>`) | no |
| Tool backends (local, MCP, remote) | `tool.Registry` | no |
| Storage, history, compaction, caching | consumers of `EventRunStart` / `EventTurnDone` / `RunError` / `Result`; the transcript in `Input` | no (`pkg/store`, E05) |
| Tracing, metrics | wrappers around `llm.Model` and `tool.Registry`, and the event stream (timings are in the `Record`) | no |
| Retries | a wrapper around `llm.Model` (retry only before the first event) | no |
| Provider options, output limit | `WithRequest` | done here |
| Approval, redaction, truncation of tool output | `WithBeforeTool` / `WithAfterTool` | no |
| Serial-only tools | `WithMaxParallelTools(1)`, or a registry wrapper with a lock | no |
| Clock | `WithNow` | no |

Not possible without changing the harness, and deferred until a caller needs it: compaction inside a run (before a model call), a replaceable `Validate`, a hook between turns that ends the run with a `RunError`, per-turn tool filtering, and continuing after a `pause_turn` stop.

## Acceptance criteria

All tests use `llmtest.Fake` and `tooltest.Fixtures` (or small `tool.Func`s), and are driven through a helper (`rig.drive`) that checks the invariants listed in the Notes after every run.

- [x] A run with no tool calls returns the model's answer and one turn. (`TestRunWithoutTools`)
- [x] Two tool calls in one turn run concurrently (both start before either finishes) and their results come back in one message in call order. (`TestParallelToolsRunConcurrentlyResultsInCallOrder`, `TestMaxParallelToolsBoundsConcurrency`, `TestOneSlotRunsCallsInOrder`)
- [x] A failing tool and an unknown tool name become `IsError` results; the run continues. (`TestToolFailuresBecomeErrorResults`)
- [x] Every request the model receives extends the previous one; earlier messages are never changed (checked on every turn of a multi-turn run). (`TestHistoryIsAppendOnly`, and the request invariants on every run)
- [x] Max turns and max tokens stop the run with `ErrBudgetExceeded` and keep the transcript. (`TestBudgetExceeded`)
- [x] Cancelling the context mid-tool stops the run with the context error and no further model calls. (`TestCancelMidToolStopsRun`, `TestCancelBetweenTurnsMakesNoModelCall`, `TestCancelWithQueuedToolCalls`)
- [x] `MaxTokens` and `Refusal` stop reasons end the run with their typed errors. (`TestStopReasonsAreTypedErrors`, which also covers malformed replies)
- [x] `BeforeTool` denial and `AfterTool` replacement are visible in what the model receives. (`TestHooksShapeWhatTheModelSees`)
- [x] Events arrive in order, and text deltas concatenate to the final text. (`TestEventOrder`, and the event grammar on every run)
- [x] A run resumed from its serialized transcript continues with the same next request as an uninterrupted run. (`TestResumeFromSerializedTranscript`, `TestFollowUpContinuesTheConversation`)
- [x] Each turn reports its appended messages with timestamps, its model and tool timings, usage and stop reason; applying the turn deltas to the input transcript gives the final transcript. (`TestTurnRecordsAndDeltas`, and the record invariants on every run)
- [x] Stopping the iteration early, or a panic in the consumer, cancels the model stream and running tools, `Run` returns only after they have, and no goroutine outlives the run. (`TestConsumerStopCancelsEverything`, `TestSimulatedRuns`)
- [x] Model errors are returned wrapped (`llm.IsRetryable` still works) and not retried. (`TestModelErrorIsWrappedAndNotRetried`)
- [x] A transcript that fails `Validate` is rejected before any model call, and `Validate` agrees with an independent statement of the rules on random and fuzzed conversations. (`TestInvalidInputIsRejectedBeforeAnyModelCall`, `TestValidateAgreesWithTheRules`, `FuzzTranscript`)
- [x] A turn whose tools all finished is kept if the context is cancelled just after; a tool failure that ends the run does not wait for the other calls. (`TestFinishedTurnIsKeptWhenCancelledJustAfter`, `TestFatalToolErrorCancelsSiblings`)
- [x] Model requests carry the options given with `WithRequest` on every turn. (`TestRequestOptionsReachEveryRequest`)
- [x] Nothing the harness hands out shares memory with the transcript: a consumer, hook and registry that scribble over every value they receive change no transcript, request or outcome. (`TestSimulatedRuns`, `FuzzSimulatedRun`, under `-race`)
- [x] Whatever the script (hostile model replies, failing tools, hooks, budgets, a prior transcript, a consumer that cancels, stops or panics), every run satisfies the invariants. (`TestSimulatedRuns`, `FuzzSimulatedRun`)

## Notes

- archtest: `pkg/harness` imports `pkg/llm` and `pkg/tool` only (never an adapter); no allowlist entry was needed.
- Tests are not parallel: each checks that the goroutine count returns to its baseline (`leakcheck`, with `testkit.Eventually`), which parallel tests would disturb.

### Decisions

- **Turns are atomic; budgets are checked before each model call.** The tools of the last permitted turn still run, so a token budget can be overshot by one turn; `ErrBudgetExceeded` leaves a transcript that validates and resumes with a bigger budget. Budgets count per `Run`, not over the prior transcript.
- **A reply that ends the run is appended only if it is well formed and calls no tools.** `max_tokens`, refusal and other-stop replies are kept and announced by an `EventTurnDone` before the error. A reply with calls nothing will answer (for example cut off by the output limit) is not appended, because no model accepts a transcript with unanswered tool calls. Likewise `end_turn` with calls, `tool_use` without calls, and any reply that fails the per-message check are `ErrUnexpectedStop` and not appended.
- **Tool errors:** see "Tool results". Anything the model can react to is an error result; infrastructure faults end the run.
- **`tools.List` is called once per `Run`.**
- **Events are yielded from the goroutine that iterates `Run` only.** When the consumer stops, panics, or the context is cancelled, the tool context is cancelled and `Run` returns (or unwinds) after every worker has.
- **The clock is `WithNow`**, not `pkg/clock` (not built yet). Durations are monotonic only if the clock's times carry a monotonic reading, as `time.Now` does.
- **`Validate` is structural.** It does not know what a provider accepts (an empty reply, a user message that mixes results and text); that is for the adapter. A replaceable `Validate` is deferred.
- **Added beyond the first design:** `EventRunStart`, `Event.Block`, `WithRequest`, `ErrInvalidConfig`, `Transcript.Validate`, `TimedMessage`, `Record.Prompt`.
- Private helpers `toolUses` and `addUsage` stay in this package; promote `Message.ToolUses()` and `Usage.Add` into `pkg/llm` when a second caller appears. No change to `pkg/llm` or `pkg/tool` was needed.

### Invariants checked after every run

`rig.drive` (`helpers_test.go`) runs the harness and then `rig.check` (`invariants_test.go`). Every test in the package goes through it.

- **Requests:** every request the model gets is a valid conversation; the first is the input; each extends the one before; none is changed after it was sent.
- **Events:** the grammar above; every started call finished before its turn ends; text deltas per block add up to the committed reply's text blocks; `run_done` only on success.
- **Transcript:** valid; the input is its prefix; saved and loaded without loss (Opaque bytes exact); equal to the input plus the messages events announced.
- **Record:** turn records equal those in events; usage adds up; model calls equal `Turns`; times only move forward; one tool record per call.
- **Budgets:** never exceeded except by the one turn allowed.
- **Outcome of stopping early** (simulation): no tool still running when `Run` returns; no model call after a cancellation.

### Review, and why the first tests missed what it found

An independent review of the first version (draft PR bsamanvith/agentkit#2, an Opus sub-agent using the `code-review` skill and the planned `review` checklist) found nine blocking problems and several smaller ones. All were reproduced and fixed:

| | Problem |
|---|---|
| B1 | A tool call's input was shared with the transcript, so an event consumer, a hook or a registry could change history (and race with the tool). |
| B2 | `Validate` accepted a tool result in an assistant message and a tool call in a user message. |
| B3 | A reply with an empty tool name was committed; the transcript then failed `Validate` and JSON encoding, so it could not be saved or resumed. |
| B4 | A new message with a nil block was sent to the model. |
| B5 | `EventToolDone` could arrive without its `EventToolStart` after a cancellation. |
| B6 | `TestParallelToolsRunConcurrentlyResultsInCallOrder` failed about 3.5% of the runs under `-race` (it released tool `a` before the harness had sent `b`'s event). The "`-count=20` clean" in the first version of these notes was luck. |
| B7 | The test for "stopping cancels the model stream" asserted nothing, so a harness that kept reading survived. |
| B8 | The parallelism-limit test missed a removed semaphore about 90% of the time. |
| B9 | A hook panic had no matchable error, and its test asserted on the message. |

Also fixed: `Result.Final` shared blocks with the transcript; a tool failure that ended the run waited for its siblings; a complete turn was dropped if the context was cancelled just after; a panic in the consumer left workers running; with a queue of calls and few slots, which call went first was up to the scheduler.

**Why the behavior tests did not catch them.** Each test ran one scenario and checked that scenario's outputs, so a bug on a path no criterion mentions (a failure while a call waits for a slot, a consumer that writes to what it is given, a model reply no real model sends) could not show. Nothing checked what must hold for *every* run: the model is only sent valid conversations, the saved transcript loads again, events are well formed. And the concurrency tests relied on which goroutine ran first. What changed (and is now a rule, ENGINEERING §2.6):

1. **Invariants checked after every run**, through the one helper every test uses (list above). All the existing tests gained these checks.
2. **A seeded simulation** (`sim_test.go`, 250 seeds in the unit tests plus `FuzzSimulatedRun`): scripts with hostile replies, mixed tool outcomes, hooks, budgets, prior transcripts, and consumers that read, stop, panic, cancel or scribble. A failure names its seed. A scribbling run must equal a clean run.
3. **`Validate` is compared with an independent statement of the rules** on 20,000 random conversations and in the fuzz target, instead of being its own oracle.
4. **Concurrency tests are driven by the consumer** (it opens a gate when it sees an event), not by scheduling. The two waits that remain (a 200 ms look for a call that must not begin, a 30 ms slow tool shutdown) can only let a broken harness pass, never fail a correct one.
5. **Every bug from the review was planted again** (below). The survivors of the first round (misplaced blocks in `Validate`, the late cancel, the fatal-sibling wait, the wait for tools when the consumer panics) each got a check that is general or a test of the property itself, not of the one case.

### Mutation checks

Each bug was planted alone, `go test -race -shuffle=on -count=2 ./pkg/harness/` was run, and the change was reverted (the tree was diffed against a backup afterwards). Every one makes a test fail, none by a compile error. The script is not kept in the repo; this list is the record.

| Planted bug | Caught by |
|---|---|
| tool results sent in finish order | `TestParallelToolsRunConcurrentlyResultsInCallOrder`, `TestSimulatedRuns`, `TestToolFailuresBecomeErrorResults` and more |
| tools run sequentially | `TestParallelToolsRunConcurrentlyResultsInCallOrder`, `TestMaxParallelToolsBoundsConcurrency` |
| one result per message | `TestHistoryIsAppendOnly`, the simulation, and most others |
| an earlier message edited | the request and transcript invariants (nearly every test) |
| an Opaque block dropped from the history | `TestResumeFromSerializedTranscript` |
| a tool failure ends the run | `TestToolFailuresBecomeErrorResults`, `TestSimulatedRuns` |
| max turns / max tokens not enforced | `TestBudgetExceeded`, `TestSimulatedRuns` |
| cached tokens left out of the token budget | `TestBudgetExceeded` |
| cancellation lets one more model call happen | `TestCancelBetweenTurnsMakesNoModelCall`, `TestSimulatedRuns` |
| `BeforeTool` denial / `AfterTool` replacement ignored | `TestHooksShapeWhatTheModelSees` |
| a stack trace placed in `Content` | `TestToolFailuresBecomeErrorResults`, `TestSimulatedRuns` |
| resume sends a different next request | `TestResumeFromSerializedTranscript` |
| event, hook or registry shares a call's input with the transcript (B1, three plants) | `TestSimulatedRuns`, `FuzzSimulatedRun` |
| `Validate` accepts a tool call in a user message / a result in an assistant message (B2) | `TestValidateAgreesWithTheRules`, `TestInvalidInputIsRejectedBeforeAnyModelCall` |
| a malformed reply is committed (B3) | `TestSimulatedRuns`, `TestStopReasonsAreTypedErrors` |
| a new message is not validated (B4) | `TestInvalidInputIsRejectedBeforeAnyModelCall` |
| `tool_done` for a call that never began (B5) | `TestCancelWithQueuedToolCalls`, `TestSimulatedRuns` |
| parallelism limit ignored (B8) | `TestMaxParallelToolsBoundsConcurrency`, `TestOneSlotRunsCallsInOrder` |
| harness keeps reading the model after the consumer stops (B7) | `TestConsumerStopCancelsEverything` |
| hook panic is not a `tool.PanicError` (B9) | `TestHookPanicEndsRunWithoutCrashing` |
| `Result.Final` shares blocks with the transcript | `TestSimulatedRuns` |
| a finished turn is dropped on a late cancel | `TestFinishedTurnIsKeptWhenCancelledJustAfter` |
| a fatal tool error does not cancel the other calls | `TestFatalToolErrorCancelsSiblings` |
| workers not awaited when the consumer panics | `TestConsumerStopCancelsEverything`, `TestSimulatedRuns` |

Also run: `go test -race -shuffle=on -count=20 ./pkg/harness/...` (clean), the timing-sensitive concurrency tests at `-count=300` under `-race` (clean; the old one failed 3.5%), `FuzzTranscript` for 20 s and `FuzzSimulatedRun` for 40 s (48,000 seeds, clean), `make check`.

### For T024 (`harness.Step`)

- Keep the transcript in state under the step's own key, and save it after every turn: append the messages of each `EventTurnDone` (`ev.TurnRecord.Messages`) to the transcript the step started from. flow only checkpoints finished steps, so this is what lets a crashed step continue instead of paying for its turns again.
- To continue, pass `Input{Transcript: saved}` with no `Message` when the saved transcript ends with a user message (a crash between turns), or with the new message for a follow-up. A transcript that ends with an assistant message is a finished conversation (or one that stopped on `ErrMaxTokens`, `ErrRefused`) and needs a new message.
- A step that returns `ErrBudgetExceeded` has a transcript that resumes with a bigger budget: `RunError.Transcript()` or `harness.Collect`'s partial `Result`.
- The run's context is the step's; cancellation arrives as the context's error. A turn whose tools all finished before the cancel is kept in the transcript.
- The tools of a turn that crashed before its `EventTurnDone` are not in the saved transcript, so on resume the model is asked again and may call them again (with new call IDs): tools with side effects should be idempotent.
- Two `Step`s in one `flow.Parallel` must use different keys; the harness itself has no shared state.

### Contract for the store layer (E05)

The harness stores nothing. What a store and the component that runs agents can rely on (design direction: [PLAN.md `store`](../docs/PLAN.md#store)):

- A run takes `Input{Transcript, Message}` and gives `EventRunStart` (the prompt), one `EventTurnDone` per finished turn (the messages it added, with times, usage, tool timings), and `Result` or `*RunError` (the transcript, usage, record). Write the prompt first and each turn as it completes; a crash loses at most the turn in flight.
- Message JSON is `llm.Message`'s own and round-trips Opaque blocks byte for byte; it is the only part of a stored conversation that is ever put back into a request.
- A transcript that starts with a compaction summary is valid (`Validate` does not require a real first prompt); so is one that ends after any whole turn.
- A run that fails with `llm.ErrContextTooLong` has a transcript that ends on a whole turn, so it can be compacted and resumed. `RunError.Usage()` is the cost of the run including a turn that did not complete.
- The store never imports `harness`.

### Deferred

- Retries around model calls (wrap the `llm.Model`; `llm.IsRetryable` works on the returned errors).
- Compaction inside a run; a replaceable `Validate`; a between-turns hook; per-turn tool re-listing; continuing after `pause_turn`.
- A "model call started" event, and `ToolUseStart`/input deltas from the model stream for UIs.
- Wire forms (JSON tags) for `Event` and `Record`, needed when the harness becomes a remote component (ENGINEERING §1.5).
- Tools that can ask the user (approval) belong to `flow.Approve` at step boundaries for now.
