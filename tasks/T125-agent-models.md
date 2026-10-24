---
id: T125
title: "Supported models and per-model variants in agent definitions; model on runs"
epic: E24
status: todo
depends_on: [T075]
---

# T125: supported models

## Context

An agent runs one model today (`model` in its definition). The owner wants each agent to list the models it supports and the person to pick one when using it, with optional per-model prompt changes. Decisions: [E24](E24-agent-models.md).

## Scope

- Definition: `models: {allow, allow_patterns, variants}` (config file and stored agents); validated at save with field-level errors (`models.allow[1]`, `models.allow_patterns[0]`, `models.variants[2].match`): unknown provider, bad or over-long regex, the default model not allowed, `system` with `system_append`.
- A model-resolution package in `pkg/` (pure: names, patterns, variants; no provider code), used by serve, the runner and the console's API.
- `model` on `wire.RunRequest`, tasks and conversations; `400 model_not_allowed`; `409 model_provider_mismatch`; the run records it (`store.Run.Model`, migrations for SQLite and Postgres) and `GET /v1/runs/{id}` shows it; resume keeps it. Crosses `runnerrpc` to a split runner (`runner.Placement.Model`).
- Built agents keyed by model as well.
- `GET /v1/agents/{name}` shows `models` (allow, patterns, default; variants to admins only, as the system prompt is).
- `GET /v1/models`: providers and known ids. `agentkit run --model`, `agentkit ask --model`.
- OpenAPI, `pkg/agentclient`.

## Out of scope

- The console (T077, T127). Tool description overrides (T126; variants carry the map, T126 applies it).
- New providers (T128, T025).

## Acceptance criteria

- [ ] An agent allowing `anthropic/claude-haiku-5-5` and pattern `^anthropic/claude-opus-.*` runs either when asked, and refuses `anthropic/claude-sonnet-5-5` with `model_not_allowed`.
- [ ] No `model` runs the default; continuing a conversation without one keeps its last model.
- [ ] A variant's `system` (or `system_append`) is what the model receives for a matching model and not for another (fake model records requests).
- [ ] Switching provider mid-conversation is `409 model_provider_mismatch`; switching model within a provider works.
- [ ] A run's model is stored, shown, kept on resume and on a split runner.
- [ ] Invalid `models` sections are refused with field errors and change nothing.
- [ ] An agent with no `models` behaves exactly as before (existing suites unchanged).
- [ ] Two models of one agent version build once each (counting builder).
- [ ] Fuzz: pattern matching never panics and never matches beyond the anchors.
- [ ] Mutation list recorded.

## Notes

-
