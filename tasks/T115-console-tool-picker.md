---
id: T115
title: "Console: the tool picker in the chat composer"
epic: E22
status: todo
depends_on: [T114]
---

# T115: Tool picker in the chat

## Context

[E22](E22-connectors-and-tool-picker.md): in a chat, a person picks which tools the agent may use, and whether they are offered directly or through tool search. The run API takes the choice ([T114](T114-run-tool-selection.md)).

## Scope

- A **Tools** button in the composer (icon and a count: "Tools 3/5"), opening a popover:
  - one checkbox per toolset of the chat's agent, with its name, tool count and status; a toolset whose credential the person has not connected says "Not connected" with a link to the Connectors page, and is unchecked by default;
  - a segmented control **Direct / Search** ("Search: the agent finds tools when it needs them; fewer tools in its context");
  - "Reset to agent defaults".
- The choice is kept per conversation in the browser (a new chat: per agent until it gets an id), and sent as `tools` with every run of that chat.
- Keyboard and screen reader: the popover is a dialog with labelled checkboxes; Escape closes it.

## Out of scope

- Picking single tools. Saving choices on the server.

## Acceptance criteria

- [ ] The popover lists the agent's toolsets and the run request carries the checked ones and the mode — test: `chat.test.tsx` "tool picker sends the chosen toolsets and mode"
- [ ] A toolset that is not connected is unchecked by default and links to Connectors — test: `chat.test.tsx` "tool picker marks unconnected toolsets"
- [ ] The choice survives a reload of the conversation and a new chat starts from the defaults — test: `chat.test.tsx` "tool picker remembers per conversation"
- [ ] With every toolset and the agent's mode chosen, the run sends no `tools` at all — test: `chat.test.tsx` "tool picker sends nothing for defaults"
- [ ] The console e2e (fake model) runs a chat with Search and sees the `tool_search` step — test: `e2e/chat.spec.ts` "tool search in a chat"

## Notes

-
