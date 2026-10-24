---
id: T126
title: "Tool description overrides per agent and per model variant"
epic: E24
status: todo
depends_on: [T125]
---

# T126: tool description overrides

## Context

A connector's description is written for every agent; an agent builder wants to say how this agent should use a tool ("use web_search only for prices"), and a model may need different wording. Decisions: [E24](E24-agent-models.md).

## Scope

- `tool_descriptions` in the definition and in each `models.variants[]`; the variant's map goes over the agent's.
- Applied where the agent's tool definitions are listed to the model (native and emulated deferred loading, tool_search results, MCP-served agents' own tools untouched). Policy, kind, risk and schema unchanged.
- Validation at save: every name must be a tool of the agent (with `prefix_tool_names` applied); empty descriptions refused; 4 KiB cap each.
- `GET /v1/agents/{name}` (admins) shows the overrides; the tool catalog for the agent marks overridden tools.

## Out of scope

- Overriding input schemas or names. Per-chat tool choice (E22).

## Acceptance criteria

- [ ] The model receives the overridden description for an agent with an override, the original for another agent using the same toolset.
- [ ] A variant's override wins for its model only.
- [ ] Overrides reach deferred tools when they load (native and emulated).
- [ ] An override for a tool the agent lacks is refused with `tool_descriptions.<name>`.
- [ ] Mutation list recorded.

## Notes

-
