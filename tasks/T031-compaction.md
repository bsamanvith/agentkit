---
id: T031
title: Compaction in the runner (reactive and proactive)
epic: E05
status: done
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

- [x] Reactive path: a `Fake` that returns `ErrContextTooLong` once leads to one compaction entry and a successful resumed run whose first request starts with the summary.
- [x] Proactive path triggers at the threshold and not below it (table test on token counts).
- [x] `History` still returns every original entry, byte-identical, after any number of compactions; `Context` starts at the latest one.
- [x] A compaction that fails (model error) leaves the log unchanged and the run's error is returned.
- [x] Mutation list recorded.

## Notes

- Done 2026-10-17. Decisions:
  - **The agent compacts, not the runner.** `runner.Compactor` is an `Agent` that also has `Compact(ctx, history)` and `ShouldCompact(usage)`. `*harness.Harness` is one: `Compact` asks its own model with the fixed `harness.CompactionPrompt`, and `ShouldCompact` uses `harness.WithContextWindow`. In the split deployment the model lives in the runner component, so `runnerrpc` gained a `Compact` call and its client agent takes the window. An agent that is not a Compactor is never compacted.
  - **The summary request is text.** The history goes as one user message rendered by `harness.RenderHistory`: roles as headings, tool calls and results as bracketed lines cut at 4000 bytes, opaque blocks left out. No tools are sent. This keeps it smaller than the history that did not fit, and valid whatever the history's shape. A summary cut at max tokens, or empty, fails.
  - **What a compaction entry holds.** A user message with `runner.SummaryPrefix` + the summary, then whatever must follow verbatim: the last assistant message and its tool results when the context ends on results (so the turn in progress continues), the last user message when it ends on one (after the fixed reply `runner.CompactedText`, to keep roles alternating), or just that reply when it ends on the assistant. `Covers` is the whole log before it. Opaque blocks before the compaction are not sent again, except in that verbatim tail (the provider needs a tool turn's thinking block to continue it).
  - **Reactive**: on `ErrContextTooLong` the run's turns are stored, the conversation compacted, and the agent runs again from the compacted context, once per execution, without counting as an interactive retry. If the summary fails, or nothing precedes the tail, the run fails with the model's error (the compaction's appended as text, not wrapped, so retries follow the model's error).
  - **Proactive**: before a new run's message is stored, when the last turn's prompt (input + cache read + cache write tokens) reached the share of the window. A failure there is skipped and the run goes ahead. The entry is labelled with the previous run, so a crash before the message is stored still reads as "not started" on resume.
  - **People still see everything.** `GET /v1/conversations/{id}` leaves compaction entries out, and `store.Conversation.Messages` no longer counts them (all three backends, and the suite checks it).
  - **Usage**: the summary's tokens are in the compaction entry's record (`CompactionRecord.Usage`) and on `EventCompacted`, not in the run's usage.
  - Config: `model.context_window` (default 200000) and `model.compact_at` (default 0.8) per agent.
- Follow-ups: the console does not show that a conversation was compacted; a run resumed after a crash that follows a reactive compaction uses the runner's flush batch size, not its own (the prompt entry's record is before the compaction).
- Mutations (46, all killed): compacting twice, on any failure, never; a compaction counted as a retry; resuming without the compacted transcript; proactive always, never, aborting on failure, before the cancel marker; the compaction's error wrapped or dropped; labelling with the run; no fixed reply; a one-message tail on tool results; no tail; summarizing the tail too; an empty head allowed; no `EventCompacted`, no `EventSaved` after it; the wrong reason; the window not reset by a compaction; turn usage not tracked; the API's turn count not rebased; compactions listed in a conversation; compaction messages counted (memory, and SQLite's count at creation); `Full` with `>`, without cache tokens, with output tokens, a share past 1 not defaulted, an unknown window full; a cut or empty summary accepted; no trim; no request options; empty history accepted; opaque blocks rendered; no clip, clip off by one, clip through a rune; tool names lost in results; the window's share unchecked; `ErrCannotCompact` as `ErrNoAgent`; the remote window ignored; `serve` not passing the window or not checking `compact_at`.
