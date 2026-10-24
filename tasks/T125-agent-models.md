---
id: T125
title: "Supported models and per-model variants in agent definitions; model on runs"
epic: E24
status: done
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

- [x] An agent allowing `anthropic/claude-haiku-5-5` and pattern `^anthropic/claude-opus-.*` runs either when asked, and refuses `anthropic/claude-sonnet-5-5` with `model_not_allowed`.
- [x] No `model` runs the default; continuing a conversation without one keeps its last model.
- [x] A variant's `system` (or `system_append`) is what the model receives for a matching model and not for another (fake model records requests).
- [x] Switching provider mid-conversation is `409 model_provider_mismatch`; switching model within a provider works.
- [x] A run's model is stored, shown, kept on resume and on a split runner.
- [x] Invalid `models` sections are refused with field errors and change nothing.
- [x] An agent with no `models` behaves exactly as before (existing suites unchanged).
- [x] Two models of one agent version build once each (counting builder).
- [x] Fuzz: pattern matching never panics and never matches beyond the anchors.
- [x] Mutation list recorded.

## Notes

Status `done`: every criterion has a test, below; all run in `make check`.

**Criteria and their tests.**
- Names and patterns, a model refused: `pkg/agentapi` `TestRunsPickAnAllowedModel`; `pkg/llm/modelset` `TestAllowsNamesAndPatterns`, `TestPatternsAreAnchored`.
- Default and the conversation's last model: `TestRunsKeepTheConversationsModel`.
- Variants' prompts: `TestVariantsChangeTheSystemPrompt` (agent API), and with serve's builder `cmd/agentkit` `TestConfigAgentModels`, `TestStoredAgentModels` (`system_append` on a name, `system` on a pattern).
- Provider switch: `TestRunsKeepTheConversationsModel` (409, then a new conversation on the other provider works).
- Stored, shown, resumed, split: `storetest.RunRunStore` `QueueOptionsAreStored` (memory, SQLite, Postgres), `pkg/runner` `TestRunKeepsItsModel`, `TestResumeCannotPickAModel`, `pkg/runner/runnerrpc` `TestRemoteRunOnAModel`, `TestOldRunnerRefusesARunOnAModel`, `cmd/agentkit` `TestSplitRunnerModels`.
- Invalid sections: `modelset` `TestInvalidSpecs`, `cmd/agentkit` `TestStoredAgentModels` (bad pattern, unknown provider, bad name, a variant of a model not allowed, system with system_append, a model that does not build, unknown field), `TestModelsNeedForModel`.
- No `models`: every existing suite passes unchanged (only the OpenAPI probes gained `GET /v1/models`).
- Built once: `TestConfigAgentModels`, `TestSplitRunnerModels`, `TestStoredAgentModels` count the model builder's calls per model during runs.
- Fuzz: `modelset` `FuzzAllows`.

**What was built.**
- `pkg/llm/modelset` (pure, stdlib only): `Parse`/`Canonical` of `<provider>/<id>` (bare ids are `anthropic`'s; ids may hold `/` for gateways), `Spec` (`allow`, `allow_patterns`, `variants`), `Set` (`Resolve`, `Allows`, `Variant`, `Matching`), field errors, limits (64 names, 16 patterns of 256 bytes, 32 variants).
- `pkg/store`: `Run.Model`, `WithModel` (text), migrations `sqlite/0018_run_model.sql` and `postgres/0020_run_model.sql`; the suite checks it.
- `pkg/runner`: `runner.Model` run option, `Placement.Model` on start and resume.
- `pkg/runner/runnerrpc`: `runReq.Model` on a method of its own (`RunWithModel`, so an older runner refuses rather than running the default), `ModelAgent` on the served side, `Client.ModelAgent`, `ErrNoModel`.
- `pkg/agentapi`: `Agent.Models` and `Agent.ForModel`; `RunRequest.Model`, `TaskRequest.Model`, `Run.Model`, `Agent.Models` (`default`, `allow`, `allow_patterns`, `choices`); `GET /v1/models` (`WithModels`); `400 model_not_allowed`, `409 model_provider_mismatch`; a resume cannot name a model. OpenAPI updated and probed.
- `cmd/agentkit`: the `models` section (config and stored agents), `onModel` (a variant's prompt and settings; another provider's defaults), `modelBuilds` (every model allowed by name built with the agent, so a bad one fails startup or the save; pattern-only models built on first use, at most 16 kept), the runner component's `modelHarness`, the remote agent per model in the agent API, `knownModels` for `GET /v1/models`, `ask`/`chat -model`. `pkg/agentclient`: `Models`, `ErrModelNotAllowed`, `ErrModelProviderMismatch`.

**Decisions.**
- Variants are not shown in `GET /v1/agents/{name}` (they hold prompts); admins read them in the stored version or the config.
- A variant may match models the set does not allow (it only applies to allowed ones); a variant naming a model exactly must name an allowed one.
- Compaction on a remote runner uses the agent's default model.
- Triggers and agent messaging run the default model.
- Variants carry `tool_descriptions`, which T126 applies; until then they are stored and ignored.

**Mutation self-check** (each planted alone; the named test failed):
1. Patterns not anchored: `TestPatternsAreAnchored`.
2. Continuing a conversation forgets its model: `TestRunsKeepTheConversationsModel`.
3. No provider check: `TestRunsKeepTheConversationsModel`.
4. Every run on the default: `TestRunsPickAnAllowedModel`.
5. Resume drops the run's model: `TestRunKeepsItsModel`.
6. SQLite forgets the model: `TestRunStore`.
7. No cache of per-model builds: `TestConfigAgentModels`.
8. Variants ignored by serve: `TestConfigAgentModels`.
9. The client sends a model on the old method: `TestOldRunnerRefusesARunOnAModel`.
10. A resume may name a model: `TestResumeCannotPickAModel`.
11. Tasks drop the model: `TestModelsAreListed`.
12. The runner builds a model the agent does not allow: `TestSplitRunnerModels`.

**Size.** About 100 KB per agent variant, 53 KB in runner and toolsets; docs/SIZES.md has the row. The all-in-one has about 64 KB left of 37 MB.

### For T126 and T077
- T126: apply `Variant.ToolDescriptions` in `onModel` over the agent's own map; the definition is validated by building, so an unknown tool name fails there.
- T077: `GET /v1/agents/{name}` `models.choices` is the picker's list; `GET /v1/models` the suggestions for the Models section. Field errors land on `models.allow[i]`, `models.allow_patterns[i]`, `models.variants[i].match` and `models` (a model that does not build).
