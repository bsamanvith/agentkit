---
id: T014
title: implement-task skill
epic: E02
status: todo
depends_on: [T012, T013]
---

# T014: `implement-task` skill

## Context

The coding skill: takes one task file from `todo` to `review` following the repo's conventions.

## Scope

`.agents/skills/implement-task/SKILL.md`, steps:

1. Read the task file, its epic, and linked docs. Check `depends_on` are `done`; stop and say so if not.
2. Set `status: in-progress`.
3. If the task adds or changes a package's shape, run `design-module` first.
4. Use `write-tests` to map criteria to tests; write tests and code together, smallest change that satisfies the criteria.
5. Run `make check` (and `make test-integration` / `scripts/dev.sh smoke` when the task touches those tiers). Fix until green.
6. Run `review` on the diff; fix blocking findings.
7. Tick criteria, add Notes (decisions, anything deferred), set `status: review`, update the epic checklist.
8. If anything in a skill was wrong or missing, fix the skill in the same change (§4.3).

## Acceptance criteria

- [ ] Dry run on one E01 task (e.g. T002): the PR has ticked criteria, a test per criterion, green `make check`, and the task/epic files updated. Recorded in Notes.

## Notes

-
