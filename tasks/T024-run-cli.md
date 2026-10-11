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

- `harness.Step`: wraps a harness run as a `flow.Step` (input from state, transcript and answer into state).
- `cmd/agentkit run`: runs one prompt with a few demo tools (for example read a file from a sandbox directory, current time, arithmetic) and streams events to the terminal.
- `cmd/agentkit flow demo`: a two-step flow (research → `Approve` → write) that can be stopped at the approval and resumed in a new process (in-memory checkpoints persisted to a JSON file for this demo only).

## Acceptance criteria

- [ ] `harness.Step` passes a flow test with `llmtest.Fake`: the answer and transcript land in state, and resume does not re-run a finished agent step.
- [ ] `-tags live`: `agentkit run` with a question that needs a tool completes against the real API and uses the tool.
- [ ] Manual: the flow demo stops at approval, and a second process resumes it to completion. Recorded in Notes.

## Notes

-
