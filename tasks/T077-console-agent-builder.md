---
id: T077
title: "Console agent builder: tools with description overrides, models, instructions, limits; save, publish, versions"
epic: E15
status: todo
depends_on: [T075, T125, T126]
---

# T077: console agent builder

## Context

Agents are stored and versioned (T075) but only the API edits them. The owner wants to build agents in the console (2026-10-24): which tools, any tool description override, which models the agent supports (E24). The layout is UI.md "Agent builder" and "Versions" (T087's mockup v4, still waiting for the owner's review: this task builds the parts listed below and follows the review when it lands).

## Scope

- `/agents/new` and `/agents/{name}/edit`, full width: header with the version state, **Save draft** (`Mod+S`), **Publish…**; section nav; form. Edit on the agent detail; config agents open read-only with **Duplicate to edit**.
- **Sections:** Name and description; **Models** (the default; allowed models as chips with suggestions from `GET /v1/models`; patterns with a live "matches: …" against the known ids; variants: match, system replace or append, effort, tool description overrides); **Instructions** (system prompt, mono, character count); **Tools** (one block per toolset with a checkbox, using E22's `ToolsetChecklist` if it is merged, else our own; per tool an **Override description** field showing the original greyed); **Interaction**; **Limits** (turns, tokens). **Memory** when E23 exposes it for stored agents.
- Errors: inline checks; the server's `error.fields` land on their fields, with a summary that links to each.
- Saving with `if_latest`; `409 conflict` offers reload or save as next; Publish confirms.
- **Versions** page: the list (who, when), publish, roll back, delete draft. A text diff against the published version.
- Leaving with unsaved changes asks first.

## Out of scope (follow-ups, T087's design)

- The tool policy editor with Try it (needs an evaluate endpoint), the per-tool defer switches, skills picker (T076), the test chat beside the form (needs running a draft version).

## Acceptance criteria

- [ ] Creating an agent with two toolsets, one description override, two allowed models and a pattern saves v1 with exactly that definition (component test against a fake API, request body checked).
- [ ] Field errors from a `400 invalid_definition` land on their fields and the summary links to them.
- [ ] A conflict does not overwrite: it offers reload and save-as-next.
- [ ] Config agents are read-only; Duplicate opens a new agent with the same definition.
- [ ] Versions: publish and roll back call the API and update the state; a published version has no delete.
- [ ] Unsaved-changes guard on in-app navigation.
- [ ] Playwright against `make smoke`: build an agent in the UI, publish, chat with it on the second model.
- [ ] Axe passes on the builder and versions pages; first-load budget holds (the builder is a lazy route).

## Notes

-
