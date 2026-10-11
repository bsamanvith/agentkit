---
id: T031
title: Compaction in the runner (reactive and proactive)
epic: E05
status: todo
depends_on: [T030]
---

# T031: Compaction

## Context

Long conversations outgrow the model's context window. Compaction summarizes earlier history into a `compaction` entry; the log is never edited ([E05 decisions](E05-store-runner.md)). The harness accepts a transcript that starts with a compaction summary (T022 contract).

## Scope

- `runner.Compactor` interface plus a default that asks the model for a summary with a fixed prompt, and writes a `compaction` entry (summary message, `Covers` = last summarized sequence).
- **Reactive**: a run failing with `llm.ErrContextTooLong` (its transcript ends on a whole turn) is compacted, then resumed once; a second failure is returned.
- **Proactive**: after a run, if the last turn's prompt size (input + cache read + cache write tokens) passed a configured share of the context window, compact before the next run.
- Opaque blocks before the compaction point are not sent again (they live only in `History`).

## Acceptance criteria

- [ ] Reactive path: a `Fake` that returns `ErrContextTooLong` once leads to one compaction entry and a successful resumed run whose first request starts with the summary.
- [ ] Proactive path triggers at the threshold and not below it (table test on token counts).
- [ ] `History` still returns every original entry, byte-identical, after any number of compactions; `Context` starts at the latest one.
- [ ] A compaction that fails (model error) leaves the log unchanged and the run's error is returned.
- [ ] Mutation list recorded.
