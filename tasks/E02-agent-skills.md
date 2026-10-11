---
id: E02
title: Agent skills (plan, design, test, code, review, local env)
status: todo
---

# E02: Agent skills

Repo-local skills that turn [docs/ENGINEERING.md](../docs/ENGINEERING.md) into steps a coding agent can follow. They work with Claude Code, Codex, and any agent that reads the open Agent Skills format. Rules: [ENGINEERING.md §4](../docs/ENGINEERING.md#4-agent-skills).

## Outcome

- One copy of each skill in `.agents/skills/`, loaded by Claude Code (through the `.claude/skills` symlink) and by Codex.
- A contributor can go idea → tasks → design → code + tests → review with a skill at each step.
- Skills are linted and improved in the same PRs as the code they guide.

## How "done" works for a skill

Skills are not Go code, so "one test per criterion" becomes:
- the structural checks in `make lint` (T010), and
- one **dry run** per skill on a real task, recorded in the task's Notes: what the agent did, and what we changed in the skill because of it.

## Tasks

- [ ] [T010](T010-skills-layout.md) Skills layout, `AGENTS.md`, cross-agent wiring, skill lint
- [ ] [T011](T011-skill-plan.md) `plan` skill
- [ ] [T012](T012-skill-design-module.md) `design-module` skill
- [ ] [T013](T013-skill-write-tests.md) `write-tests` skill
- [ ] [T014](T014-skill-implement-task.md) `implement-task` skill
- [ ] [T015](T015-skill-review.md) `review` skill (with optional second-agent opinion)
- [ ] [T016](T016-skill-local-env.md) `local-env` skill

```
T010 ─┬─ T011
      ├─ T012
      ├─ T013 ─┐
      ├────────┴─ T014 ─ T015
      └─ T016 (after E01/T006)
```

T011–T013 can start right away. T014 needs `make check` (E01/T001) to point at; T016 needs `scripts/dev.sh` (E01/T006).
