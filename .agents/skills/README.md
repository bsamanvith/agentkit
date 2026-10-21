# Agent skills

Repo skills in the open [Agent Skills](https://agentskills.io) format: one
folder per skill with a `SKILL.md` (front matter `name` and `description`,
then a Markdown body) and optional `references/` and `scripts/`. This folder is
the only copy. Codex and other agents read `.agents/skills` directly; Claude Code
reads it through the `.claude/skills` symlink. Why and how they fit together:
[ENGINEERING §4](../../docs/ENGINEERING.md#4-agent-skills). The list with one
line each is in [AGENTS.md](../../AGENTS.md#skills).

## Rules

From [ENGINEERING §4.3](../../docs/ENGINEERING.md#43-rules-for-skills), checked
by `scripts/lint-skills.sh` (run by `make lint`) where a script can check them:

- **Point to the docs; don't copy them.** A skill says what to do and in what
  order, and links the section of `docs/` that says why.
- **Short.** The body fits on a couple of screens; the lint stops it at 150
  lines. Long material goes in `references/`, loaded only when a step needs it.
- **The description decides when it loads.** Say what it does and when to use
  it, with the words a request would use ("Use when ..."). Agents see only the
  name and description until they load the skill.
- **Agent-neutral.** Nothing only one agent can follow. Using another agent is
  optional and skipped when it is not installed (`review`'s second opinion).
- **Linted.** `name` equals the folder name, every relative link and `#anchor`
  resolves, `.claude/skills` points here.
- **Improved in the same PR.** When a skill sent an agent the wrong way, or a
  review finds the same mistake twice, fix the skill in the PR that fixes the
  code, and say what changed in the task's Notes.
- **Written from real code.** A skill comes right after the first real piece
  of work it describes, and uses that work as its example.
- **Done means a dry run.** A new skill is used once on a real task, and the
  task's Notes record what the agent did and what changed in the skill
  ([E02](../../tasks/E02-agent-skills.md#how-done-works-for-a-skill)).

## Template

`.agents/skills/<name>/SKILL.md`:

```markdown
---
name: <name, same as the folder>
description: <What it does, in one sentence. Use when <the requests and situations that should load it>.>
---

# <name>

**Goal: <the outcome, in one line>.** Why: [ENGINEERING §N](../../../docs/ENGINEERING.md#...).
Worked example: <the real task or package it was written from>.

## Steps

1. **<Verb first.>** <What to do, the command to run, the doc section that says why.>
2. ...

## Done when

- <Each item something a reviewer can check.>
```

Check it with `scripts/lint-skills.sh`, then `make lint`.
