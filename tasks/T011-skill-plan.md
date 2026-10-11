---
id: T011
title: plan skill
epic: E02
status: todo
depends_on: [T010]
---

# T011: `plan` skill

## Context

Turns an idea or request into an epic and task files that follow [tasks/README.md](README.md), with acceptance criteria that are directly testable ([ENGINEERING.md §2.3](../docs/ENGINEERING.md#23-how-many-tests-one-per-behavior-not-per-function)).

## Scope

`.agents/skills/plan/SKILL.md`, steps:

1. Read `docs/PLAN.md`, `docs/ENGINEERING.md`, and existing `tasks/` to avoid duplicates and pick the next free IDs.
2. Ask only the questions that change the breakdown; otherwise state assumptions in the epic.
3. Write the epic (outcome, ordered task list, dependency sketch) and one file per task from `_template.md`.
4. Check every acceptance criterion is a single observable behavior. Split or rewrite any that are vague ("works well"), implementation-shaped ("uses a mutex"), or multi-behavior.
5. Keep tasks small: roughly one PR each. Flag anything bigger.

## Acceptance criteria

- [ ] Dry run: planning one upcoming epic (E03 core interfaces) produces files that pass `lint-skills.sh` link checks and match the template. Recorded in Notes.
- [ ] Dry run output has no criterion a reviewer marks as untestable. Recorded in Notes with any skill changes made.

## Notes

-
