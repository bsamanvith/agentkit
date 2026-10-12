---
id: T066
title: "Console: render A2UI surfaces and send actions back"
epic: E13
status: todo
depends_on: [T055, T065]
---

# T066: A2UI renderer in the console

## Context

The console renders `ui` events (T065) inline in the chat with its own components, so agent-made UI looks like the rest of the console and can't run code. Design: [docs/UI.md](../docs/UI.md) and the mockup's "Narrow the news search" surface. Decisions: [E13](E13-agent-protocols.md).

## Scope

- A renderer in `web/packages/agent-ui` (shared with consumer apps) for the basic-catalog subset, data binding and updates to an existing surface (streamed, progressive), and surface deletion.
- Actions: buttons and submits send `ui_action` (T065) with the bound values; the surface shows "Sent" and is disabled until the agent replies.
- Safety: no HTML or style from the agent; image and link URLs through the allowlist; unknown components shown as a small "unsupported component" note, never as raw data.

## References

- [A2UI renderer development guide and component gallery](https://a2ui.org/)
- [CopilotKit: A2UI v0.9](https://www.copilotkit.ai/blog/a2ui-whats-new-in-google-generative-ui-spec) (catalog negotiation, rendering)
- T070's component inventory (A2UI components drawn with console components)

## Acceptance criteria

- [ ] Browser test (chromedp, replayed model): a streamed surface renders progressively; a later update changes it in place; submit sends `ui_action` with the right values and the agent's reply follows.
- [ ] XSS: component text containing markup renders as text; a `javascript:` link or a non-allowlisted image URL is dropped (test hook never fires).
- [ ] Renders correctly in light and dark themes at phone width.
- [ ] Mutation list recorded.
