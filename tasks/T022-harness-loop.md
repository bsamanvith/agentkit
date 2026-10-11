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

## Design

- `harness.New(model llm.Model, tools tool.Registry, opts...)`.
- `Run(ctx, Input) iter.Seq2[Event, error]` streams run events (text deltas, tool call started/finished, turn finished, run finished); `harness.Collect` for callers that only want the result.
- Tool calls from one model turn run in parallel (bounded concurrency). All results go back in **one** message, in the order of the tool calls.
- Tool failures (`tool.ErrInvalidInput`, `tool.ErrToolFailed`, `tool.ErrNotFound`) become `IsError` tool results the model can react to; context cancellation stops the run.
- Budgets: max turns, max total tokens → `ErrBudgetExceeded` with the partial transcript available.
- Stop reasons: `MaxTokens` and `Refusal` end the run with a typed error; the transcript is kept.
- Hooks: `BeforeTool` (can deny a call, which becomes an error result) and `AfterTool` (can replace the result).
- History is append-only: each request is the previous request's messages plus new ones. Nothing earlier is edited.
- A `Transcript()` that is JSON-serializable, so `flow` and `store` can checkpoint and resume a run.

## Acceptance criteria

All tests use `llmtest.Fake` and `tooltest.Fixtures`.

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

## Notes

- archtest: `pkg/harness` imports `pkg/llm` and `pkg/tool` only (never an adapter).
