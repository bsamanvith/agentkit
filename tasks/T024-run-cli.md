---
id: T024
title: "agentkit run: real agent and flow against Claude"
epic: E04
status: done
depends_on: [T021, T022, T023, T026]
---

# T024: `agentkit run`

## Context

The first real end-to-end run: our harness, our tools, the real Claude API, and a two-step flow with an approval pause. It proves the pieces fit before storage and containers arrive. It also finishes T021: the adapter is in `review` until its live test has run against the API, and this task is where that happens.

Read first: `tasks/T022-harness-loop.md` (sections "Pluggability" and "Contract for the store layer"), `tasks/T023-flow.md` Notes, `tasks/T021-llm-anthropic.md` Notes, and `docs/ENGINEERING.md` §2.6.

## Scope

- `harness.Step` (in a package that may import both `harness` and `flow`, not in either of them): wraps a harness run as a `flow.Step`.
  - Input from state; transcript and answer into state under an **explicit output key**, so two agents in one `flow.Parallel` write different keys (the same key in two branches is `flow.ErrConflict`).
  - **Saves the transcript after every turn** by appending the messages of each `EventTurnDone`, through a small `TranscriptSaver` interface keyed by run id plus step path (memory implementation here; the E05 store implements it later). This is needed because flow only checkpoints finished steps.
  - **On re-run, resumes** with `harness.Input{Transcript: saved}` and no message when the saved transcript ends on a user message (the harness contract: a resumed run sends exactly the requests an uninterrupted one would).
- `cmd/agentkit run`: runs one prompt against Claude (`pkg/llm/anthropic`) with tools from the `demo` toolset (`pkg/toolset`), and streams events to the terminal (text deltas, tool calls with timings, per-turn usage).
- `cmd/agentkit flow demo`: a two-step flow (research → `Approve` → write) that can be stopped at the approval and resumed in a new process (in-memory checkpoints persisted to a JSON file for this demo only).

## Acceptance criteria

- [x] `harness.Step` passes a flow test with `llmtest.Fake`: the answer and transcript land in state, and resume does not re-run a finished agent step. — `agentstep.TestStepWritesAnswerAndTranscript`, `agentstep.TestResumeDoesNotRerunFinishedAgentStep` (and `TestRerunAfterAnswerMakesNoModelCall` for the narrower case: the agent answered but flow's checkpoint was lost)
- [x] A step interrupted after turn 2 of 3 resumes from its saved transcript and makes exactly one more model call; its requests equal an uninterrupted run's. — `agentstep.TestInterruptedStepResumesWithOneMoreCall`
- [x] Two agent steps in one `flow.Parallel` with different output keys merge; with the same key they fail with `flow.ErrConflict`. — `agentstep.TestParallelAgentsNeedDifferentKeys`
- [x] Invariants named and checked after every test run through one helper (ENGINEERING §2.6): every saved transcript is a prefix of the next, the final transcript equals the harness `Result`'s, and flow state holds exactly the declared output keys. — `rig.check` in `pkg/agentstep/helpers_test.go`, registered with `t.Cleanup` by `newRig`, which every test in the package uses (list below)
- [x] Mutation list recorded in Notes (at least: save skipped on one turn, resume sends the prompt again, output key ignored, resume restarts from scratch), every planted bug caught. — 11 plants, all caught (table below)
- [x] `-tags live`: `agentkit run` with a question that needs a tool completes against the real API and uses the tool. This also runs T021's live test; when both pass, T021 moves to `done`. — `cmd/agentkit.TestLiveRunUsesTool` and `pkg/llm/anthropic.TestLiveToolCall`, both passed on 2026-10-11 (results below); T021 is `done`
- [x] Live check of the T022 `Validate` concern: a real Claude conversation with a user message that mixes tool results and text is either accepted by `Validate` or the restriction is documented as intended (see T022 handoff). Recorded in Notes. — `cmd/agentkit.TestLiveMixedToolResultsAndText`: Claude accepts it, `Validate` rejects it; the restriction is kept as intended (below)
- [x] Manual: the flow demo stops at approval, and a second process resumes it to completion. Recorded in Notes. — run on 2026-10-11 against Claude (below); now also automated with the built binary as separate processes, replaying recorded Claude responses, in `cmd/agentkit.TestE2EFlowKilledResumedApproved` (and with the fake model in `TestFlowDemoStopsAndResumesInAnotherProcess`)

## Notes

### What was built

- **`pkg/agentstep`** (the `harness.Step` of the plan; imports `harness` and `flow`, in neither of them):
  - `New(name, *harness.Harness, TranscriptSaver, Keys{Input, Output}, ...Option) (*Step, error)`; `Step` implements `flow.Step` and `flow.Namer`. `WithObserver(func(path string, ev harness.Event))` forwards every harness event (the CLI streams them).
  - Reads the prompt (a string) from `Keys.Input`; writes `Output{Answer, Transcript}` under `Keys.Output`.
  - `TranscriptSaver{Load(ctx, runID, path), Append(ctx, runID, path, msgs)}`, `NewMemorySaver()`, and the conformance suite `agentsteptest.RunSaver`.
  - Errors: `ErrInvalidConfig` (from `New`), `ErrNoRun` (run outside `flow.Run`/`flow.Resume`).
  - The package doc says that tools with side effects must be idempotent, and why.
- **`cmd/agentkit`** (did not exist before; T003 adds the component runtime later):
  - `agentkit run [-model -effort -max-turns -system] "<prompt>"`: Claude through `pkg/llm/anthropic`, the `demo` toolset through `toolset.Build`, and events printed as they come: text deltas, `→ tool input`, `← tool (duration): result`, and a usage line per turn with model time.
  - `agentkit flow demo [-state file -id -approve | -reject reason] ["<topic>"]`: `Sequence("demo", research, Approve("approve"), brief, write)`. `brief` is a `flow.Func` that builds the writer's prompt from the topic and the research answer.
  - Checkpoints and transcripts go to one JSON file (`fileStore`, demo only: rewritten via a temp file and rename on every save, one process at a time). The file store passes `flowtest.RunCheckpointer` and `agentsteptest.RunSaver`.
- **`pkg/flow`: one function added, `flow.StepPath(ctx) (runID, path string, ok bool)`**, plus `TestStepPathNamesTheRunningStep`. Why: the saver key must be the run id plus the step path. A step only gets `Run(ctx, st)`, flow kept both values in an unexported context value, and the step cannot compute its path itself (`Parallel` and `Loop` add indices). The other option was keys fixed by the caller, but then one step inside a `Loop` or in two `Parallel` branches would share one saved transcript. The change only adds a function: no behavior changed, and every existing flow test passes unchanged. No change to `pkg/harness`, `pkg/llm` or `pkg/toolset`.
- No new dependencies; no archtest change.

### Decisions

- **What is saved**: the prompt when the run starts (`EventRunStart.Prompt`), then each `EventTurnDone`'s messages in one `Append`, but only for turns that stopped with `tool_use` or `end_turn`. A final reply that ends the run with an error (`max_tokens`, a refusal, an unexpected stop) is not saved, so a re-run asks the model that question again. This also means a saved transcript that ends with an assistant message always means "answered". `TestFailedFinalReplyIsAskedAgain` checks it: the retried request equals the failed one.
- **Three ways to start**, chosen from the saved transcript:
  - nothing saved → a new run with the prompt from state;
  - ends with a user message → `harness.Input{Transcript: saved}` with no message;
  - ends with an assistant message → write the answer without calling the model (the process stopped between the last save and flow's checkpoint).
- **Save before observe**: when an observer sees `EventTurnDone`, that turn is durable. A failed `Append` ends the run (stopping the iteration cancels the harness) and fails the step (`TestSaveFailureStopsTheRun`).
- `Append` must be atomic (the whole turn or nothing). Otherwise a crash could leave a reply without its tool results, which does not validate.
- The prompt is read from state only when the step starts fresh, so a resumed step does not depend on the input key.
- `Output` holds the whole transcript, so it ends up in flow's checkpoint state. That suits the demo, but long agent runs make checkpoints big. E05 may want to keep a reference in state and the transcript in the store.
- Saved transcripts are never deleted. Removing them when a run completes is the store's job (E05).
- Input and output keys must differ (`ErrInvalidConfig`). The step re-runs from its start state, so writing over the input would only confuse.

### Invariants (`rig.check`, after every test)

1. Every transcript the saver held is valid (whole turns only) and a prefix of the next one saved for the same step.
2. A harness run that finished has a `Result` whose transcript equals the saver's final transcript for that step.
3. Every agent `Output` in a final state holds a transcript the saver ended with, and its `Answer` is that transcript's last text.
4. A flow that finished holds exactly the declared state keys.

Transcripts are compared by their JSON, which is exact for opaque blocks. All tests are `t.Parallel()`; nothing waits on timing. Each test drives the order of events itself: a crash is a `context.CancelFunc` called by the saver right after the Nth `Append`, and a lost checkpoint is a `Checkpointer` that fails one save.

Also run: `go test -race -shuffle=on -count=20 ./pkg/agentstep/... ./cmd/agentkit/ ./pkg/flow/` (clean), `make check`.

### Mutation checks

Each bug was planted alone in `pkg/agentstep`, then `go test -race -shuffle=on -count=2 ./pkg/agentstep/ ./cmd/agentkit/` was run and the file restored (diffed against a backup). Every plant compiled, and every one failed tests:

| Planted bug | Caught by |
|---|---|
| save skipped on one turn (turn 2) | `TestStepWritesAnswerAndTranscript`, `TestInterruptedStepResumesWithOneMoreCall`, `TestResumeDoesNotRerunFinishedAgentStep`, `TestRerunAfterAnswerMakesNoModelCall`, `TestFlowDemoStopsAndResumesInAnotherProcess` (invariant 2) |
| resume sends the prompt again | `TestInterruptedStepResumesWithOneMoreCall`, `TestFailedFinalReplyIsAskedAgain` |
| output key ignored (written under the step name) | `TestStepWritesAnswerAndTranscript`, `TestParallelAgentsNeedDifferentKeys` and 5 more (invariant 4) |
| resume restarts from scratch (saved transcript ignored) | `TestInterruptedStepResumesWithOneMoreCall`, `TestRerunAfterAnswerMakesNoModelCall`, `TestFailedFinalReplyIsAskedAgain` |
| transcript saved before its turn is complete (the reply appended before its tool results) | `TestStepWritesAnswerAndTranscript`, `TestInterruptedStepResumesWithOneMoreCall` and 3 more (invariant 1: a saved transcript did not validate) |
| an answered transcript sent to the harness again | `TestRerunAfterAnswerMakesNoModelCall` |
| a failed final reply (`max_tokens`) saved | `TestFailedFinalReplyIsAskedAgain` |
| answer taken from the wrong message | 7 tests (invariant 3) |
| saver key ignores the step path | `TestParallelAgentsNeedDifferentKeys`, `TestFlowDemoStopsAndResumesInAnotherProcess` |
| `MemorySaver.Load` returns its own slice, not a copy | `TestMemorySaver/StoresCopies` |
| `MemorySaver.Append` keeps the caller's messages | `TestMemorySaver/StoresCopies` |

### Live results (2026-10-11, Claude Opus 5.5, `-tags live`)

- `pkg/llm/anthropic.TestLiveToolCall` (T021): passed. Turn 1 `tool_use` (418 in / 49 out), turn 2 `end_turn` ("The echo tool returned \"pineapple\"."), 4.5 s.
- `cmd/agentkit.TestLiveRunUsesTool`: passed. `agentkit run -effort low "Use the calculate tool to multiply 48271 by 9733 ..."` → `→ calculate {"op": "mul", "a": 48271, "b": 9733}`, `{"result":469821643}`, 2 turns. The second turn read 640 tokens from the prompt cache.
- Run with `ANTHROPIC_API_KEY=... go test -tags live -count=1 -run TestLive -v ./cmd/agentkit/ ./pkg/llm/anthropic/`.

### `Validate` and mixed user messages (the T022 concern)

`TestLiveMixedToolResultsAndText` sends Claude a real tool call, then a user message holding the tool result **and** a text block ("Now say what the tool returned, in French.").
- **Claude accepts it** (`end_turn`, answered in French).
- **`harness.Transcript.Validate` rejects it** (`ErrInvalidInput`: "message 2 has 2 blocks, want 1 tool results").

Decision: **the restriction stays, as intended for now.**
- The harness never builds such a message itself: tool results always go back alone, in one message.
- `Validate` only matters for transcripts given back to the harness, and every transcript the harness and `agentstep` save passes it.
- Mixing in text would mean a user steering an agent mid-turn. That needs the deferred between-turns hook (T022 "Deferred") and a looser `Validate`, which T022 already lists as a follow-up ("a replaceable `Validate`").
- The live test asserts both sides, so a change on either side is noticed.

### Manual: the flow demo across processes (real Claude)

```
$ agentkit flow demo -effort low -state demo.json "How many days are left until the end of this year, and what is Go's latest major feature?"
── demo/research ──
→ current_time {}                     ← current_time: {"time":"2026-10-11T19:00:56Z"}
→ calculate {"op":"add","a":20,"b":61} ← calculate: {"result":81}
… research answer …
· done: 3 turns
Waiting for approval at demo/approve. Continue with: agentkit flow demo -state demo.json -id demo -approve   (exit 0)

$ agentkit flow demo -effort low -state demo.json -approve      # a new process
── demo/write ──
… one paragraph …
=== draft ===  (exit 0; checkpoint status "completed"; transcripts: research 6 messages, write 2)
```

Also run by hand: a research agent **killed with SIGINT right after turn 1** (`agentkit: demo/research: harness: context canceled`, exit 1).
- The file held 3 messages: the prompt; a reply with a thinking block, text and a tool call; and the tool result.
- Running the same command again resumed it. The new process made only the remaining two model calls: it did not call `current_time` again and went straight to `calculate`.
- Claude accepted the thinking block replayed from the file (read 676 cached tokens) and stopped at the approval as before.

### End-to-end replay tests (`cmd/agentkit/e2e_test.go`)

These tests build the binary and run it as separate OS processes. They talk to a local server that replays Claude responses recorded from the real API (`testdata/e2e/<scenario>/NN.http`, the raw-HTTP form the adapter tests use). They run with the unit tests in about 1 s, need no network or key, and skip under `-short`.
- `TestE2ERun`: `agentkit run` makes a tool call (`calculate`) and answers in two turns.
- `TestE2EFlowKilledResumedApproved`: three processes share one state file.
  - **Process A** starts the demo flow. The server holds A's second model call until the test sends SIGINT, so A is killed while research turn 1 (with a thinking block) is saved. A exits 1, and the file holds 3 messages with status `running`.
  - **Process B** resumes the research. Its first request is byte-for-byte the one A was killed waiting for, and it never sends turn 1 again. B stops at the approval.
  - **Process C** approves. Only the writer runs, and the flow completes.
- After every test, `checkConversation` checks all requests across processes: each agent's request extends its previous one exactly, byte for byte (so a thinking block read back from the file is unchanged), and ends with a user message. The server also checks that every fixture was used, so a change in the number of model calls fails the test.
- **To record again**: `AGENTKIT_E2E_RECORD=1 ANTHROPIC_API_KEY=... go test -count=1 -run TestE2E ./cmd/agentkit/`. The server forwards to the API with the real key (the binary only ever gets a dummy key) and rewrites the fixtures, keeping only status, Content-Type and the event stream. Recorded on 2026-10-11 with Claude Opus 5.5. The flow scenario runs at default effort so turn 1 has a thinking block; recording can take a second try to get one.
- Bugs planted to check them: agentstep resuming from scratch, and the file store not writing transcripts to disk. `TestE2EFlowKilledResumedApproved` caught both.

### For E05 / T030 (the store's `TranscriptSaver`)

- Implement `Load(ctx, runID, path)` (empty when nothing is saved) and `Append(ctx, runID, path, msgs)` on the store. Run `agentsteptest.RunSaver` against it.
- `Append` is one turn: the prompt alone, or the reply plus its results message. Write it in one transaction, numbering the messages in the store (the deltas carry no index).
- Store message JSON with `llm.Message`'s own encoding: opaque blocks must round-trip byte for byte (the suite checks).
- Key is (run id, step path), the path from `flow.StepPath`. Steps in a `Parallel` append concurrently under different paths; one path has one writer at a time (flow's one-writer-per-run rule).
- The runner can also write `TurnRecord`s (times, usage, tool timings) from the observer. `agentstep` saves only the transcript.
- To decide: deleting transcripts when a run completes or is rejected, and keeping a reference instead of the whole transcript in `Output`.

### Follow-ups (not done here)

- The step tree given to `flow.Resume` must match the one that checkpointed; detected since T032 (`ErrShapeChanged`).
- `README.md` and `docs/PLAN.md` still say T024 is next and the adapter's live test is pending; they were outside this task's files.
- The printer interleaves the text of agents running in parallel; the demo is sequential.

### Contracts from `pkg/flow` (T023 handoff) that shaped `agentstep`

- **flow only checkpoints when a step completes**, and a step that crashes is re-run from the state it started with. An agent step can run for many turns, so on its own a crash would lose every turn and repay for them. `harness.Step` therefore saves its own transcript after every turn, keyed by run id plus step path, through a small interface (memory implementation here; the SQLite store later), and on re-run resumes from it instead of starting over. Test: a step killed after turn 2 of 3 resumes and makes exactly one more model call.
- **Steps must be safe to re-run.** Tool side effects are not undone on resume: document that tools with side effects should be idempotent (or guarded by a key derived from the tool-use ID). Correction, as documented in `pkg/agentstep`: a turn lost in a crash is asked again and its calls get new tool-use IDs, so the key must come from the run, the step path and the input, not the tool-use ID alone.
- **The step tree passed to `flow.Resume` must match the one that checkpointed**; since T032 a changed tree is detected (`flow.Fingerprint`, `ErrShapeChanged`, opt-out `flow.WithAllowShapeChange`).
