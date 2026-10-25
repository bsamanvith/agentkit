---
id: T077
title: "Console agent builder: tools with description overrides, models, instructions, limits; save, publish, versions"
epic: E15
status: done
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

- [x] Creating an agent with two toolsets, one description override, two allowed models and a pattern saves v1 with exactly that definition (component test against a fake API, request body checked).
- [x] Field errors from a `400 invalid_definition` land on their fields and the summary links to them.
- [x] A conflict does not overwrite: it offers reload and save-as-next.
- [x] Config agents are read-only; Duplicate opens a new agent with the same definition.
- [x] Versions: publish and roll back call the API and update the state; a published version has no delete.
- [x] Unsaved-changes guard on in-app navigation.
- [x] Playwright (`make web-smoke`, the server that keeps stored agents): build an agent in the UI, publish, chat with it on the second model.
- [ ] Axe passes on the builder and versions pages (not run: axe isn't a dependency yet, see Notes). First-load budget holds (the builder is a lazy route): done.

## Notes

- Pages: `pages/agent-builder.tsx` (`/agents/new`, `/agents/{name}/edit`, `?from=` a version to edit from, or on `/agents/new` an agent to duplicate), `pages/agent-form.tsx` (the sections), `pages/agent-versions.tsx` (`/agents/{name}/versions[/{v}]`), `pages/definition-diff.tsx`, and `pages/agent-def.ts` (the draft model, checks, where errors land, pattern matches, the text diff). They are one lazy chunk (`pages/builder.ts`, 47 kB raw); the first load is unchanged (228,253 of 240,000 bytes). The embedded dist grows by 16 KB (gzip), under the size log's threshold.
- The draft keeps what the form doesn't edit (skills, triggers, tool policy, memory, notify, defer_loading, a model's base_url or tool_search, unknown variant keys) and writes it back as it was. Empty values are left out of the definition.
- Every field's id is its definition path (`fieldId("models.allow[1]")`); a server field error lands on the field at its path or the nearest above it ("model.id" lands on the default model, "models.allow[3]" on Also allowed). One with no field shows in the summary only, linked to its section.
- A tool's override name is the name the model calls it: `<toolset>__<tool>` for runtime toolsets and agents with `prefix_tool_names`. Overrides for tools no used toolset lists stay visible under "Other description overrides", so nothing is dropped silently.
- Patterns are checked with JavaScript's RegExp, anchored like the server's RE2; the few syntaxes that differ are caught by the server's 400 on save.
- Duplicate: a stored agent copies its newest definition exactly. A config agent copies what `GET /v1/agents/{name}` shows an admin (description, model and effort, allowed models and patterns, system prompt, toolsets, tool descriptions); its variants, skills, policy and memory aren't in the API, and the page says so.
- Not yet: the test chat, policy editor, skills picker and defer switches (out of scope above); Memory (E23 has no builder field yet); a list of draft-only agents (`GET /v1/agents` lists published ones, so a new agent appears in the list once published; the builder says it runs once published).
- Axe isn't a dependency of the console; the pages use labelled fields, `aria-invalid` with `aria-describedby` errors, a focused `role="alert"` summary, and hidden Added/Removed words in the diff. Adding `@axe-core/playwright` is a follow-up.
- Criteria to tests: `pages/builder.test.tsx` "the agent builder" (the exact v1 body with two toolsets, one override, two models and a pattern; inline checks and the server's field errors on their fields with the summary's links and the section dot; conflict with "Save mine as v4" and "Discard mine, load v5", nothing overwritten and unedited fields kept; config agent read-only and its Duplicate's body; Publish after confirming, with and without changes; the unsaved-changes guard; a variant's fields), "the versions page" (publish, roll back, delete a draft, no delete for the published one; the agent page's Edit, Versions, Duplicate and New agent), and "agent definitions" (round trip with unknown fields, where errors land, pattern matches, checks, the diff). `e2e/builder.spec.ts` builds an agent, publishes it and chats with it on its second model.
- Mutation self-check (all killed): the draft's unedited fields dropped; `if_latest` always 0; no unsaved-changes guard; errors never landing; Delete draft on the published version; saving without the inline checks.
