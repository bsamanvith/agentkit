---
id: T065
title: "A2UI in the harness: ui_render tool, catalog validation, ui events, ui_action messages"
epic: E13
status: todo
depends_on: [T062]
---

# T065: A2UI in the harness

## Context

Agents should be able to show UI (forms, choices, cards), not only text, streamed to the client. A2UI is the declarative protocol for it. Decisions: [E13](E13-agent-protocols.md).

## Scope

- `pkg/a2ui`: the message types of the current stable A2UI spec (read the spec at a2ui.org; record the version), the basic-catalog subset (E13), and `Validate(msg) error` (unknown component types and properties, broken ID references, cycles, size limits).
- The `ui_render` tool (opt-in per agent: `agents[].ui: "a2ui"`): input is an A2UI message; valid ones become a `ui` event on the run's stream (`{"v":1,"type":"ui","surface_id":...,"message":{...}}`), invalid ones a tool error describing the problem so the model can fix it.
- `ui_action`: `POST /v1/runs` accepts a message with a `ui_action` part (surface ID, action, values); it is appended to the conversation and shown to the model as structured text.
- The agent API's OpenAPI spec (T062) and `pkg/agentclient` (T053) gain the `ui` event and `ui_action`.

## References

- [A2UI specification](https://a2ui.org/) (current stable version, message reference, basic catalog, actions, data binding)
- [A2UI A2A extension](https://a2ui.org/) (how surfaces travel over A2A, for T067)
- [Introducing A2UI](https://developers.googleblog.com/introducing-a2ui-an-open-project-for-agent-driven-interfaces/) (design goals: no code, client catalog, streaming)

## Acceptance criteria

- [ ] Golden examples from the spec validate; each invalid case (unknown component, unknown property, dangling child ID, cycle, oversize) fails with a specific error.
- [ ] Fake model calls `ui_render`: the stream carries the `ui` event in order between turns; an invalid surface gives a tool error and the model's corrected retry succeeds.
- [ ] A `ui_action` continues the conversation; the transcript stays valid and append-only.
- [ ] An agent without `ui: "a2ui"` has no `ui_render` tool.
- [ ] Live (`-tags live`): real Claude, asked to let the user pick between options, produces a valid surface on the first or second try.
- [ ] `FuzzValidate` never panics; mutation list recorded.
