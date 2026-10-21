---
id: E02
title: Agent skills (plan, design, test, code, review, local env)
status: todo
---

# E02: Agent skills

Repo-local skills that turn [docs/ENGINEERING.md](../docs/ENGINEERING.md) into steps a coding agent can follow. They work with Claude Code, Codex, and any agent that reads the open Agent Skills format. Rules: [ENGINEERING.md §4](../docs/ENGINEERING.md#4-agent-skills).

Skills are written **from real code, not ahead of it** (§4.3): each one comes right after the first piece of work it describes, and uses that work as its example.

## Outcome

- One copy of each skill in `.agents/skills/`, loaded by Claude Code (through the `.claude/skills` symlink) and by Codex.
- A contributor can go idea → tasks → design → code + tests → review with a skill at each step.
- Skills are linted and improved in the same PRs as the code they guide.

## How "done" works for a skill

Skills are not Go code, so "one test per criterion" becomes:
- the structural checks in `make lint` (T010), and
- one **dry run** per skill on a real task, recorded in the task's Notes: what the agent did, and what we changed in the skill because of it.

## Tasks, in the order they become possible

- [ ] [T010](T010-skills-layout.md) Skills layout, `AGENTS.md`, cross-agent wiring, skill and task lint (with M0)
- [ ] [T015](T015-skill-review.md) `review` skill, with optional second-agent opinion (with M1; review T017 with it)
- [ ] [T013](T013-skill-write-tests.md) `write-tests` skill (after T017, using its tests as examples; first version in, dry runs pending)
- [ ] [T012](T012-skill-design-module.md) `design-module` skill (after T017, using `pkg/tool` as the example)
- [ ] [T014](T014-skill-implement-task.md) `implement-task` skill (after T012, T013)
- [x] [T011](T011-skill-plan.md) `plan` skill (when planning E04)
- [ ] [T016](T016-skill-local-env.md) `local-env` skill (after T006)
