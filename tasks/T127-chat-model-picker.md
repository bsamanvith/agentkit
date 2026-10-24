---
id: T127
title: "Model select in the chat composer"
epic: E24
status: done
depends_on: [T125]
---

# T127: model select in chat

## Context

With T125 an agent may support several models; the person picks one in chat. Decisions: [E24](E24-agent-models.md). E22 adds a Tools button to the composer's bottom toolbar; the model select sits next to the agent picker.

## Scope

- A select next to the agent picker listing the agent's allowed models (names from `allow`, plus the known ids of `GET /v1/models` that the patterns match), the default first. Hidden when the agent supports one model.
- The pick goes on each run (`model`, beside E22's `tools` run option), is remembered per conversation, and resets to the default for a new chat.
- `409 model_provider_mismatch` is shown as "Start a new chat to use <model>." with a button.
- Each assistant turn shows its model in its details.

## Acceptance criteria

- [x] The select lists exactly the allowed models and sends the pick (component test with a fake API).
- [x] One-model agents show no select.
- [x] The provider mismatch offers a new chat that keeps the message (component test).
- [x] Playwright smoke: pick a second model, the run reports it.

## Notes

- `run_start` now carries `model` (additive, wire v1), from the run's stored model, also when a run's events are rebuilt (`replay.go`). The console's answers take it from there; history alone does not carry it, so an answer loaded from history shows its model only when this tab ran it.
- The pick lives in `ToolChoices` (`tool-choices.ts`) as `model`, beside the toolsets and mode, so it moves from New chat to the conversation with them. "Reset to the agent's tools" keeps the model.
- The picker's value in a conversation is its pick, else the model of the last answer this tab saw, else the agent's default. A conversation picked on another device shows the default until its next answer here: the server still runs its last model.
- `409 model_provider_mismatch` keeps the message in the box (as every refused send does); **New chat** moves it to the agent's new chat, sets that chat's model and clears the conversation's pick. `400 model_not_allowed` says the agent can't run that model.
- Criteria to tests: `chat.test.tsx` "the model picker" (lists exactly the choices and sends the pick, the usage line names the run's model, the conversation keeps it; no picker for an agent with no models or one; the mismatch's New chat with the message and the model). `e2e/chat.spec.ts` "a chat picks another model…" against the real binary (the e2e agent allows `claude-sonnet-5-5`). `pkg/agentapi` `TestRunStartNamesTheModel` (streamed and read again, default too).
- Mutation self-check (all killed): the picker shown from one choice (`< 1`); the run sent without `model`; the draft not moved on New chat; `run_start` without the model when streamed; and when rebuilt in `replay.go`.
