---
id: T115
title: "Console: the tool picker in the chat composer"
epic: E22
status: done
depends_on: [T114]
---

# T115: Tool picker in the chat

## Context

[E22](E22-connectors-and-tool-picker.md): in a chat, a person picks which tools the agent may use, and whether they are offered directly or through tool search. The run API takes the choice ([T114](T114-run-tool-selection.md)).

## Scope

- A **Tools** button in the composer (icon and a count: "Tools 3/5"), opening a popover:
  - one checkbox per toolset of the chat's agent, with its name, tool count and status; a toolset whose credential the person has not connected says "Not connected", with a link to the Connectors page;
  - **Agent's default / Direct / Search** ("Search: the agent finds tools when it needs them; fewer in its context");
  - "Reset to agent defaults".
- The choice is kept per conversation in the browser (a new chat: per agent until it gets an id), and sent as `tools` with every run of that chat.
- Keyboard and screen reader: the popover is a dialog with labelled checkboxes; Escape closes it.

## Out of scope

- Picking single tools. Saving choices on the server.

## Acceptance criteria

- [x] The popover lists the agent's toolsets and the run request carries the checked ones and the mode — test: `chat.test.tsx` "the tool picker" › "sends the chosen toolsets and mode"
- [x] A toolset that is not connected says so and links to Connectors — test: `chat.test.tsx` "marks toolsets that aren't connected and links to Connectors"
- [x] The choice survives a reload of the conversation and a new chat starts from the defaults — test: `chat.test.tsx` "remembers the choice per conversation; a new chat starts from the agent's"
- [x] With every toolset and the agent's mode chosen, the run sends no `tools` at all — test: `chat.test.tsx` "sends no tools when nothing was changed"
- [x] The console e2e (fake model) runs a chat with Search and sees the `tool_search` step — test: `e2e/chat.spec.ts` "tool search in a chat: the agent finds the echo tool, then calls it"

## Notes

- Not "unchecked by default" for unconnected toolsets, as first planned: the default sends no `tools` at all (the agent's own), and a call to an unconnected toolset tells the model to say what to connect. The menu marks them instead.
- The picker is a Radix dropdown menu (checkbox and radio items), like the agent picker: no popover component in `@agentkit/ui`, and menus keep CSP-safe. `Composer` takes an `accessory` slot left of Send.
- The choice is in localStorage (`agentkit.tools.<conversation>`, `agentkit.tools.new:<agent>` for a new chat, moved to the conversation on its first send), cleared with drafts on sign-out.
