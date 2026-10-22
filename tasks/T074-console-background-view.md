---
id: T074
title: "Console: background runs and agent threads"
epic: E14
status: done
depends_on: [T071, T073, T055]
---

# T074: console view for background agents

## Context

People need to see what is running without them, and how agents are talking to each other. Layout comes from T070. Decisions: [E14](E14-background-agents-messaging.md) ("Visibility").

## Scope

- **Background runs:** a list (running and recent: agent, started by, trigger, elapsed, cost so far, status) and a detail view that attaches to the run's event stream (T071) with the same tool-step rendering as chat. Cancel from both.
- **Threads:** a list of agent-to-agent threads (participants, hops, last message, status) and a detail view showing the messages in order, each linking to the run that produced it.
- **Notifications:** a background run finishing, failing or waiting for input shows a quiet notification and a badge.
- **Triggers:** the list (admin), with next firing time, last outcome and pause/resume (T072).

## References

- [docs/UI.md](../docs/UI.md) and T070's mockup v3.
- GitHub Actions run lists and Temporal's web UI (workflow list and detail) for run lists and detail.
- Claude.ai and Claude Code background tasks lists for status and notification patterns.

## Acceptance criteria

- [x] **Watching:** a browser test (chromedp) starts a background run, closes the page and reopens it; the run is still listed and its detail replays and follows the stream.
- [x] **Threads:** a thread between two agents shows both sides in order, with links to their runs.
- [x] **Cancel:** cancel from the list ends the run (server status checked).
- [x] **Mutation list** recorded.

## Notes

### What was built

Named **Tasks** after T087's docs/UI.md (it renamed Activity). In `web/apps/console/src/tasks/`, lazy-loaded except the store and toasts:

- **Store** (`store.ts`, `context.tsx`): polls `GET /v1/runs?background=true&limit=50` every 5 s while the tab is visible. Each change between loads is news: finished, failed, was canceled, is waiting for you. News shows as a quiet toast (`role=status`, at most three, 8 s, paused on hover or focus, Open link). The rail badge counts the tasks waiting for you (`Tasks, N waiting for you`). Runs not opened yet carry a dot, kept in `localStorage` (`agentkit.tasks.unread`).
- **List** (`list.tsx`): tabs Tasks, Threads, Triggers (admin only). Tasks are grouped Waiting for you / Running and paused / Finished, with an agent filter. A row shows agent, status, started, tokens, and a Cancel button on active runs, which asks first.
- **Task page** (`pages.tsx` RunPage, `follow.ts`): replays and follows `GET /v1/runs/{id}/events`, reconnecting with `Last-Event-ID` (backoff 0.5 to 5 s). When the stream ends early (another process runs the run), it checks the run and stops if it is final. The transcript is chat's `Thread` component, so tool steps render as in chat. The title is the first line of `run_start`'s prompt. The page also shows About this task (agent and version, how it runs, started, time, tokens, run id), Cancel…, and a Result section with Copy when the task is done.
- **Threads** (`/tasks/threads`, `/tasks/threads/{id}`): rows show `a ⇄ b`, hops and the last message. The detail shows messages in hop order, each with its sender, a verb (asked, replied to, notice to), "Written in" (the run that wrote it) and "Handled in" (the run that handled it) links to `/tasks/{id}`.
- **Triggers** (`/tasks/triggers`): a table with agent, when, next firing, last outcome (with its run link) and Pause/Resume. A 403 explains that only admins see triggers.
- `@agentkit/agent-ui`:
  - client: `events`, `listRuns`, `cancelRun`, `startTask`, `listTriggers`, `setTriggerPaused`, `listThreads`, `getThread`. `run()` shares the SSE reader with `events`.
  - types: Run gains `background`, `pool` and `agent_version`. New types `Thread`, `ThreadMessage`, `Trigger`, `Firing` and `TaskRequest`.
  - thread.ts: `promptOf`.

### Decisions

- Our own toast, not Sonner: the CSP forbids inline styles.
- The list polls rather than streams: there is no list stream in the API, and 5 s is enough for a background view. The first load is a baseline (no news), so opening the console doesn't announce old runs. A run canceled from this tab, or the one being viewed, makes no news.
- "Written in" comes from the request's `run_id` for a reply, or the latest earlier message to the sender's conversation. The API doesn't record the sender's run (see gaps).
- Admin navigation (the Agents and Toolsets rail sections, the Triggers tab) shows only once `GET /v1/auth/me` says the caller is an admin, holds `agent:admin` or `*`, or the server has no credentials; unknown is no (docs/UI.md "Rules"). The API provider now reads `/v1/auth/me` once per sign-in (it used to wait for the account menu). The API still answers 403 regardless.
- Cancel asks with `window.confirm` (UI.md: "Cancel… asks first").
- The browser test is Playwright, not chromedp: the console's e2e suite (T066) is Playwright. The tasks tests run against a second server and fake model (ports 18480/18481, `e2e/tasks.json`: boss may message worker), so their held answers don't disturb the chat tests.

### Tests per criterion

- Watching: `e2e/tasks.spec.ts` "a background run outlives the page" (start a task, page closed, a new page lists it as Running, replays the stored step, follows the held answer live to Done; server status checked).
- Threads: `e2e/tasks.spec.ts` "a thread between two agents shows both sides in order" (hop order, sender, links; the worker's run page has the Result). Unit: thread order and links in `tasks.test.tsx`.
- Cancel: `e2e/tasks.spec.ts` "Cancel in the list ends the run on the server" (confirm accepted, row Canceled, server `canceled`). Unit: the list's confirm and cancel, and the task page's Cancel….
- Also in `tasks.test.tsx` (30) and agent-ui `client.test.ts`/`thread.test.ts`: news rules, baseline, unread persistence, toasts (at most three), storage blocked or garbled, Cancel of a run that had already ended (409 `run_final`: shows how it ended, no error), no load while the tab is hidden (and no extra load from a wait left by `stop()`), badge, follow's reconnect and early end, triggers pause and resume.

### Mutation list

Each planted alone, then the named tests run (vitest); all were killed.

1. `follow` reconnects without `after` (Last-Event-ID): follow tests.
2. `follow` stops at an early end without checking the run: follow tests.
3. The first load counts as news: store baseline test (a baseline run with a future `created_at`).
4. A run canceled from this tab counts as news: store cancel test (cancel answers `running`, then the load sees it canceled).
5. A reply credited to the run that handled it: `writtenBy` test and thread page links.
6. Cancel shown on every run: list test.
7. `events` sends no `Last-Event-ID` header: agent-ui client test.
8. Cancel posts to `/stop`: agent-ui client test and list test.
9. "Waiting for you" not active (no Cancel): list and status tests.
10. Pause always resumes: triggers test.
11. Thread messages reversed: thread page test.
12. Badge counts nothing: badge test.
13. `promptOf` ignores the prompt: thread.test `promptOf`.
14. The list polls while the tab is hidden: store hidden-tab test.
15. Reading or writing `localStorage` unguarded: store storage test.
16. Four toasts kept: store toast test.
17. A 409 `run_final` on Cancel rethrown: store cancel-409 test.

Not killed, and why: `stop()` not removing the hidden tab's listener changes no behavior (the next load removes it, and a stopped store ignores it); it was a leak only.

### Sizes

- Console first load 221,698 → 224,554 B (budget 240,000), measured after the pre-gzipped dist (#93). The tasks pages are a lazy chunk.
- api binary (`nomodels noconnectors nomcp nosqlite`, `-s -w -trimpath`) 19,611,808 → 19,644,576 B (+32,768 B, the embedded dist). Under 100 KB and within the api budget: no SIZES row.

### API gaps (for T086/T087/T077)

- Run has no title or input; the list shows the agent and the page reads `run_start`'s prompt. There's no origin either (started by whom, or by which trigger), so the list doesn't show "started by" or "trigger".
- There's no cost, only tokens.
- `GET /v1/threads` lacks participants and the last message, so the list fetches each thread (N+1). Thread hop and budget limits aren't in the API.
- A request message doesn't record its sender's run, so "Written in" is derived.
- Not built yet: steering, pause and resume (T086 merged its API after this was built; the console still hides them), Start a task, Run again, New trigger, recent firings, folding the transcript under "How it got there", and browser notifications (open in T087).
