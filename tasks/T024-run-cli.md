---
id: T024
title: "agentkit run: real agent and flow against Claude"
epic: E04
status: todo
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

- [ ] `harness.Step` passes a flow test with `llmtest.Fake`: the answer and transcript land in state, and resume does not re-run a finished agent step.
- [ ] A step interrupted after turn 2 of 3 resumes from its saved transcript and makes exactly one more model call; its requests equal an uninterrupted run's.
- [ ] Two agent steps in one `flow.Parallel` with different output keys merge; with the same key they fail with `flow.ErrConflict`.
- [ ] Invariants named and checked after every test run through one helper (ENGINEERING §2.6): every saved transcript is a prefix of the next, the final transcript equals the harness `Result`'s, and flow state holds exactly the declared output keys.
- [ ] Mutation list recorded in Notes (at least: save skipped on one turn, resume sends the prompt again, output key ignored, resume restarts from scratch), every planted bug caught.
- [ ] `-tags live`: `agentkit run` with a question that needs a tool completes against the real API and uses the tool. This also runs T021's live test; when both pass, T021 moves to `done`.
- [ ] Live check of the T022 `Validate` concern: a real Claude conversation with a user message that mixes tool results and text is either accepted by `Validate` or the restriction is documented as intended (see T022 handoff). Recorded in Notes.
- [ ] Manual: the flow demo stops at approval, and a second process resumes it to completion. Recorded in Notes.

## Notes

Contracts from `pkg/flow` (T023 handoff) that shape `harness.Step`:
- **flow only checkpoints when a step completes**, and a step that crashes is re-run from the state it started with. An agent step can run for many turns, so on its own a crash would lose every turn and repay for them. `harness.Step` therefore saves its own transcript after every turn, keyed by run id plus step path, through a small interface (memory implementation here; the SQLite store later), and on re-run resumes from it instead of starting over. Test: a step killed after turn 2 of 3 resumes and makes exactly one more model call.
- **Steps must be safe to re-run.** Tool side effects are not undone on resume: document that tools with side effects should be idempotent (or guarded by a key derived from the tool-use ID).
- **The step tree passed to `flow.Resume` must match the one that checkpointed**; this is not detected yet (a shape fingerprint is a candidate follow-up before checkpoints outlive deployments with the SQLite store).
