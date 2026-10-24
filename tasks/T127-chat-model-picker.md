---
id: T127
title: "Model select in the chat composer"
epic: E24
status: todo
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

- [ ] The select lists exactly the allowed models and sends the pick (component test with a fake API).
- [ ] One-model agents show no select.
- [ ] The provider mismatch offers a new chat that keeps the message (component test).
- [ ] Playwright smoke: pick a second model, the run reports it.

## Notes

-
