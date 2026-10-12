---
id: T069
title: "AG-UI adapter (later)"
epic: E13
status: todo
depends_on: [T065]
---

# T069: AG-UI adapter

## Context

AG-UI (CopilotKit) is an event protocol between agent backends and frontends. An adapter lets AG-UI frontends (CopilotKit's React components, for example) use our agents, carrying A2UI surfaces as AG-UI events. Only when such a frontend is wanted; the console uses our own API. Decisions: [E13](E13-agent-protocols.md).

## Scope

- Map our run events to AG-UI events (run started/finished, text message start/content/end, tool call start/args/end/result, state snapshots, A2UI surfaces as the spec's custom or UI events) at `/agui/{agent}`; map AG-UI input (messages, tool results for frontend tools, interrupts) back.
- Verify against the current AG-UI spec and its reference client.

## References

- [AG-UI docs](https://docs.ag-ui.com/) (event types, state, interrupts) and the [AG-UI repo](https://github.com/ag-ui-protocol/ag-ui)
- [A2UI: how it compares and transports](https://a2ui.org/introduction/agent-ui-ecosystem/) (A2UI over AG-UI)

## Acceptance criteria

- [ ] The reference AG-UI client completes a run with a tool call and an A2UI surface against our server (fake model).
- [ ] Mutation list recorded.
