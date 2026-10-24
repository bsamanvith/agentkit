---
id: E24
title: "Agents in the console with a choice of model: supported models, per-model overrides, tool description overrides"
status: todo
---

# E24: agents with a choice of model

Owner request (2026-10-24): build agents in the console, choosing which tools they have and overriding any tool's description, and which LLM models they support, as a static list, regex patterns, or both. When using an agent, the person picks the model from those it supports; Anthropic is the default. A model may change the system prompt and the tool descriptions the agent sends. Gemini comes soon, so the provider layer stays pluggable. Agent memory with size caps is the memory thread's (E23, another thread); the per-chat tool picker and tool search are E22's.

## Decisions (made in planning; change them in the task that finds them wrong)

| Question | Decision |
|---|---|
| Naming a model | One string, `<provider>/<id>`: `anthropic/claude-opus-5-5`, later `gemini/gemini-3-pro`. The provider is a key of `modelProviders` (`cmd/agentkit/models.go`), so a new provider is a package under `pkg/llm`, a builder file and a table row, nothing else ([COMPONENTS.md](../docs/COMPONENTS.md#plugging-in-another-implementation)). A bare id means `anthropic/<id>`. |
| What an agent supports | `models` in the definition: `allow` (exact names) and `allow_patterns` (RE2 regexes, matched against the whole name, at most 16 of 256 bytes each). Either, both, or neither: neither means the agent runs only its `model`, as today. `model` stays the **default** and must itself be allowed. |
| Per-model changes | `models.variants`: a list of `{match, system, system_append, tool_descriptions, effort, max_tokens}`; `match` is an exact name or a pattern (`re:` prefix). The **first** matching variant applies, over the agent's own settings. `system` replaces the prompt, `system_append` adds to it (one or the other). |
| Tool description overrides | `tool_descriptions` in the definition: `{"<tool name>": "<description>"}`, for every model; a variant's own map goes over it. A name the agent's tools don't have is refused at save. The input schema is never overridden (a wrong schema breaks the tool, a wrong description only misleads). |
| Picking at use time | `model` on `POST /v1/runs` (and conversations, tasks, the CLI's `--model`). Not allowed by the agent: `400 model_not_allowed` naming the agent's models. Empty: the conversation's last model, else the agent's default. Recorded on the run (`GET /v1/runs/{id}` `model`) and kept on resume. |
| Switching mid-conversation | Allowed within one provider. Another provider would get the first one's opaque blocks (thinking signatures), so it is `409 model_provider_mismatch`: start a new conversation. |
| Which models exist | No provider is asked (there is no models API we rely on). `GET /v1/models` lists the providers this server builds and a static list of known ids per provider (from the adapter package), for suggestions. A pattern may allow ids not in the list; a name that matches but the provider refuses fails the run with the provider's error. |
| Building | A built agent is per (tenant, agent, version, model), in the registry's LRU, so a pick builds once. Config-file agents take `models` too. |

## Tasks

- [ ] [T125](T125-agent-models.md) Supported models and per-model variants in agent definitions; `model` on runs; `GET /v1/models`
- [ ] [T126](T126-tool-description-overrides.md) Tool description overrides per agent and per model variant
- [ ] [T077](T077-console-agent-builder.md) Console agent builder (E15): tools with description overrides, models, instructions, limits; save, publish, versions
- [ ] [T127](T127-chat-model-picker.md) Model select in the chat composer, and the model on runs in the transcript
- [ ] [T128](T128-llm-gemini.md) `pkg/llm/gemini` (E08), when a key is available

Order: T125, then T126 (shares the variant code), then T077 and T127 (console) in parallel. T128 any time after T125.
