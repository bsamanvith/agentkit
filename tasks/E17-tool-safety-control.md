---
id: E17
title: "Tool safety and control: tool kinds and risk, policies, interrupts and approvals, deferred loading, steering"
status: todo
---

# E17: tool safety and control

Owner request (2026-10-12):
- tools classified by **kind** (read, write, ...) and **risk**;
- **policies** that allow, flag, require approval for or deny tool calls by those classes (some runs read-only, some environments allow low-risk writes);
- a generic **interrupt** framework (`ask_user` is the first interrupting tool), allowed or not per agent, because background and ephemeral agents must not stop to ask;
- **deferred tool loading** per tool;
- skills that **auto-load** their tools;
- **steering** a running task.

This is harness and runner work. The console screens are designed in T087 and built in E15/E14 tasks.

## Decisions (made in planning; change them in the task that finds them wrong)

| Question | Decision |
|---|---|
| Tool metadata | `tool.Definition` gains optional fields: <br>- **`kind`**: `read`, `write`, `delete`, `send` (communicates outside, e.g. email or a post), `execute` (runs code or commands); <br>- **`risk`**: `low`, `medium`, `high`; <br>- **`interrupts`** (the tool needs a person to answer, like `ask_user`); <br>- **`requires_approval`** (the default for this tool); <br>- **`tags`** (free-form, e.g. `external`, `billing`, `pii`); <br>- **display** (title, icon, present and past verbs, from docs/UI.md). <br>Connector authors set them; T049's suite checks they are set. An **unclassified tool counts as `write`/`medium`**, so a read-only policy blocks it until someone classifies it. |
| MCP interop | Map both ways to MCP tool annotations: <br>- `readOnlyHint` ↔ `kind: read`; <br>- `destructiveHint` → `kind: delete` or `risk: high`; <br>- `openWorldHint` → tag `external`; <br>- `idempotentHint` → tag `idempotent`. <br>Our MCP server publishes the hints; tools from external MCP servers (later) get classes from their hints. |
| Policies | A **tool policy** is an ordered list of rules. Each rule matches on kind, risk, tags, tool or toolset globs, and gives an action: <br>- **`allow`**; <br>- **`flag`**: allow, but record a `tool_flagged` event and notify; <br>- **`approve`**: a person must approve first; <br>- **`deny`**: the model gets a clear error and can choose something else. <br>Presets: `read_only`, `low_risk_writes` (read, plus low-risk writes; approve medium; deny high, delete, send, execute), `approve_writes`, `all`. |
| Where policies come from | The **agent's** policy (agent builder). The **run or environment** can only narrow it (e.g. an ephemeral environment forces `read_only`). The effective policy is the stricter of the two for every rule. Every decision appears in the run's events, so the transcript shows why a call was allowed, flagged, held or denied. |
| Interaction mode | Per agent, overridable per run (narrower only): <br>- **`interactive`**: a person is in the conversation. Interrupting tools are offered, and approvals show inline. Chat default. <br>- **`supervised`**: nobody is watching live, but the run may wait. An interrupt or approval suspends the task as **waiting for you** and notifies; the answer resumes it. <br>- **`autonomous`**: never waits. Interrupting tools are **not offered** and `approve` becomes `deny`. Default for tasks and ephemeral runs, as the owner asked. |
| Interrupt mechanism | Generic, in harness plus runner: a tool returns an **interrupt** (a question, a set of choices, or an approval request with the pending call). The harness ends the turn with the tool call pending; the runner suspends the run (E05 suspended status). The answer (`POST /v1/runs/{id}/input` or `/v1/tasks/{id}/input`) becomes the tool's result, and the run continues. The transcript stays valid: the assistant's tool call, then its result when answered. Approvals use the same path: approved runs the call, denied returns "denied by <who>: <reason>". |
| Deferred loading | Per tool or toolset in the agent config: `defer_loading: true`. Deferred tools are not sent with every request. The model finds them with a **tool search** tool, and found tools stay loaded for the rest of the run. Where the provider supports deferred tools natively (check the Claude API's tool search and `defer_loading` with the `claude-api` skill), the adapter uses that; otherwise the harness emulates it with its own `tool_search` tool. Loading changes no policy: a loaded tool is still checked. |
| Skills load tools | `SKILL.md` front matter may list **`tools:`** (names or globs; our field, like Claude Code's `allowed-tools`). Loading the skill (`skill_load`) un-defers those tools for the run. A skill cannot grant a tool its agent doesn't have, and cannot bypass the policy. |
| Steering | A person can send a message to a running task. The runner delivers it at the **next turn boundary** (after the current tool results, before the next model call), as a user message marked `steer`. The task's transcript shows it in place. Pause and resume are planned too; stop is cancel (E14). |

## Tasks

- [ ] [T082](T082-tool-metadata.md) Tool metadata: kind, risk, interrupts, approval, tags, display; MCP annotation mapping; connectors classified
- [ ] [T083](T083-tool-policy.md) Tool policy engine: rules, presets, agent and environment policies, decisions in events
- [ ] [T084](T084-interrupts-approvals.md) Interrupts and approvals: interaction modes, `ask_user`, approval holds, suspend and answer
- [ ] [T085](T085-deferred-tool-loading.md) Deferred tool loading and skills that load tools
- [ ] [T086](T086-task-steering.md) Steering a running task

Order: T082 first (small, unblocks the rest). Then T083 and T085 in parallel. T084 after T083 and T030 (suspend and resume need the runner). T086 after T071.
