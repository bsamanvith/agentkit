---
id: T024
title: "agentkit run: real agent and flow against Claude"
epic: E04
status: todo
depends_on: [T021, T022, T023]
---

# T024: `agentkit run`

## Context

The first real end-to-end run: our harness, our tools, the real Claude API, and a two-step flow with an approval pause. It proves the pieces fit before storage and containers arrive.

## Scope

- `harness.Step`: wraps a harness run as a `flow.Step` (input from state, transcript and answer into state). It takes an explicit output key, so two agents in one `flow.Parallel` write different keys (the same key in two branches is `flow.ErrConflict`).
- `cmd/agentkit run`: runs one prompt with a few demo tools (for example read a file from a sandbox directory, current time, arithmetic) and streams events to the terminal.
- `cmd/agentkit flow demo`: a two-step flow (research → `Approve` → write) that can be stopped at the approval and resumed in a new process (in-memory checkpoints persisted to a JSON file for this demo only).

## Acceptance criteria

- [ ] `harness.Step` passes a flow test with `llmtest.Fake`: the answer and transcript land in state, and resume does not re-run a finished agent step.
- [ ] `-tags live`: `agentkit run` with a question that needs a tool completes against the real API and uses the tool.
- [ ] Manual: the flow demo stops at approval, and a second process resumes it to completion. Recorded in Notes.

## Notes

Contracts from `pkg/flow` (T023 handoff) that shape `harness.Step`:
- **flow only checkpoints when a step completes**, and a step that crashes is re-run from the state it started with. An agent step can run for many turns, so on its own a crash would lose every turn and repay for them. `harness.Step` therefore saves its own transcript after every turn, keyed by run id plus step path, through a small interface (memory implementation here; the SQLite store later), and on re-run resumes from it instead of starting over. Test: a step killed after turn 2 of 3 resumes and makes exactly one more model call.
- **Steps must be safe to re-run.** Tool side effects are not undone on resume: document that tools with side effects should be idempotent (or guarded by a key derived from the tool-use ID).
- **The step tree passed to `flow.Resume` must match the one that checkpointed**; this is not detected yet (a shape fingerprint is a candidate follow-up before checkpoints outlive deployments with the SQLite store).
