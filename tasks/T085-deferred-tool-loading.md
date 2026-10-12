---
id: T085
title: "Deferred tool loading and skills that load tools"
epic: E17
status: todo
depends_on: [T082, T057]
---

# T085: deferred tool loading

## Context

Agents with many tools waste context and confuse models if every definition is sent every time. Some tools should load only when needed, by a search or by a skill that uses them. Decisions: [E17](E17-tool-safety-control.md) ("Deferred loading", "Skills load tools").

## Scope

- **Config:** `defer_loading: true` per tool or toolset in the agent config.
- **Harness:**
  - sends only non-deferred tools, plus a **`tool_search`** tool (query → matching deferred tools by name, description and tags);
  - found tools are loaded for the rest of the run;
  - loaded tools are recorded in the transcript record, so a resumed run has them again.
- **Native provider support:**
  - check the Claude API's tool search tool and `defer_loading` (`claude-api` skill);
  - if the Anthropic adapter can send deferred tools natively, do so behind the same config and record the decision;
  - other providers use the emulated `tool_search`.
- **Skills:**
  - `SKILL.md` `tools:` (names or globs) are loaded when `skill_load` runs (T057);
  - a skill can't add tools the agent doesn't have.
- **Policy:** loading never changes a decision (T083 still checks every call).
- **API and console:** the console shows "loaded N tools" as a step.

## References

- Claude API docs: tool search tool and deferred loading (find the current page with the `claude-api` skill)
- [Anthropic: Writing effective tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents) (context cost of many tools, namespacing)
- Claude Code skills' `allowed-tools` front matter (prior art for skills naming their tools)

## Acceptance criteria

- [ ] **Search and load** (fake model): with 30 deferred tools, the first request carries only the non-deferred tools plus `tool_search`; after a search, the found tool is available for the rest of the run and after a resume.
- [ ] **Skills:** `skill_load` of a skill listing `tools:` makes them available; a listed tool the agent doesn't have is ignored with a logged warning.
- [ ] **Policy:** policy denials still apply to loaded tools.
- [ ] **Live** (`-tags live`): real Claude finds and uses a deferred tool.
- [ ] **Mutation list** recorded.
