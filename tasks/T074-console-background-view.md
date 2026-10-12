---
id: T074
title: "Console: background runs and agent threads"
epic: E14
status: todo
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

- [ ] **Watching:** a browser test (chromedp) starts a background run, closes the page and reopens it; the run is still listed and its detail replays and follows the stream.
- [ ] **Threads:** a thread between two agents shows both sides in order, with links to their runs.
- [ ] **Cancel:** cancel from the list ends the run (server status checked).
- [ ] **Mutation list** recorded.
