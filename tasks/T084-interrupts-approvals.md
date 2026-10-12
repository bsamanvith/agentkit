---
id: T084
title: "Interrupts and approvals: interaction modes, ask_user, approval holds, suspend and answer"
epic: E17
status: todo
depends_on: [T083, T030]
---

# T084: interrupts and approvals

## Context

Some tools need a person: `ask_user` asks a question, and tools needing approval wait for a yes. Chats can do that inline. Tasks may wait (supervised) or must never wait (autonomous). Decisions: [E17](E17-tool-safety-control.md) ("Interaction mode", "Interrupt mechanism").

## Scope

- **Harness:**
  - a tool may return an `Interrupt{Kind: question|approval, Prompt, Choices?, Pending call}`;
  - the turn ends with the call pending, and an `interrupt` event is emitted;
  - the transcript stays valid until the answer arrives.
- **Runner:**
  - suspends the run (E05 suspended status) with the pending interrupt;
  - `POST /v1/runs/{id}/input` (and later `/v1/tasks/{id}/input`) takes the answer: text or a choice for questions, `approve`/`deny` plus a reason for approvals. The answer becomes the tool result, and the run resumes;
  - answers are idempotent (a second answer to the same interrupt is `409`).
- **`ask_user` built-in tool** (`interrupts: true`): question, optional choices.
- **Policy `approve`** (T083) creates an approval interrupt for the pending call. Approved runs the call with the original input; denied returns "denied by <who>: <reason>".
- **Interaction mode** per agent (`interactive`, `supervised`, `autonomous`), narrowed per run:
  - `autonomous` doesn't offer interrupting tools, and turns `approve` into `deny`;
  - `supervised` suspends and notifies (E14 notifications).
- `agentclient` and the OpenAPI spec cover `interrupt` events and `input`.

## References

- [LangGraph human-in-the-loop (`interrupt`, `Command(resume=...)`)](https://langchain-ai.github.io/langgraph/concepts/human_in_the_loop/) for the interrupt and resume model
- [A2A spec: `input-required` task state](https://a2a-protocol.org/latest/specification/) (so interrupts map onto A2A later)
- [AG-UI interrupts](https://docs.ag-ui.com/) and Claude Code's permission prompts, for how approvals are shown
- E05's suspended status and `flow.Approve` (same suspend and resume path)

## Acceptance criteria

- [ ] **Question, interactive:** `ask_user` suspends; the answer resumes; the transcript is valid throughout and after a process restart while suspended (replay e2e).
- [ ] **Approval:** approved runs the call with exactly the original input; denied returns the reason. A second answer gives `409`.
- [ ] **Autonomous:** `ask_user` isn't offered (the fake model records the tools); an `approve` rule denies with a clear reason.
- [ ] **Supervised:** the task shows `waiting for input`, and a notification event is emitted.
- [ ] **Simulation:** a seeded simulation with interrupts, answers, crashes and duplicate answers keeps the runner invariants.
- [ ] **Mutation list** recorded.
