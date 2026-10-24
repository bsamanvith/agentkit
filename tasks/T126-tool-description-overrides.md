---
id: T126
title: "Tool description overrides per agent and per model variant"
epic: E24
status: done
depends_on: [T125]
---

# T126: tool description overrides

## Context

A connector's description is written for every agent; an agent builder wants to say how this agent should use a tool ("use web_search only for prices"), and a model may need different wording. Decisions: [E24](E24-agent-models.md).

## Scope

- `tool_descriptions` in the definition and in each `models.variants[]`; the variant's map goes over the agent's.
- Applied where the agent's tool definitions are listed to the model (native and emulated deferred loading, tool_search results, MCP-served agents' own tools untouched). Policy, kind, risk and schema unchanged.
- Validation at save: every name must be a tool of the agent (with `prefix_tool_names` applied); empty descriptions refused; 4 KiB cap each.
- `GET /v1/agents/{name}` (admins) shows the overrides as `tool_descriptions`; the console marks overridden tools from that map (T077).

## Out of scope

- Overriding input schemas or names. Per-chat tool choice (E22).

## Acceptance criteria

- [x] The model receives the overridden description for an agent with an override, the original for another agent using the same toolset.
- [x] A variant's override wins for its model only.
- [x] Overrides reach deferred tools when they load (native and emulated).
- [x] An override for a tool the agent lacks is refused with `tool_descriptions.<name>`.
- [x] Mutation list recorded.

## Notes

- `cmd/agentkit/tooldesc.go`: `describeTools` wraps the agent's registry so `List` gives the overridden descriptions; calls go through unchanged. It wraps the registry the harness gets, so native and emulated deferred loading and tool_search all see the same definitions. `onModel` merges a variant's map over the agent's.
- Validation runs when the agent is built (config file at startup, stored agent at save): a name must be a tool of the agent as its model sees it, or start with a runtime toolset's `<rt>__` prefix (those tools are known only in a run's tenant, so they are taken on trust). Every variant's map is checked in the default build, so a bad override on a pattern-only model fails at save, not at its first run. The first bad name (sorted) is reported as `tool_descriptions.<name>` or `models.variants[i].tool_descriptions.<name>`.
- Criteria to tests (cmd/agentkit/tooldesc_test.go): `TestToolDescriptionOverrides` covers the override, another agent keeping the original, the variant winning on its model only, the schema unchanged and the admin view. `TestToolDescriptionsDeferred` covers native and emulated loading. `TestToolDescriptionsRefused` covers unknown, unprefixed, empty, too long, the variant's unknown tool and nothing saved. `TestRuntimeToolDescriptions` covers the runtime prefix.
- Mutation self-check (all killed): skip the replacement in `List`; drop the name check; drop the runtime-prefix trust; empty check without TrimSpace; cap off by one; skip the variant checks; skip the variant merge in `onModel`; drop the admin view; drop `ToolDescriptions` on the config agent; `||` to `&&` in the guard before `describeTools`. A variant agent's own `ToolDescriptions` copy survived as dead code (nothing reads it) and was removed.
