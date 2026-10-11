# Tasks

One task per file. Epics group tasks by reference. Plain Markdown, reviewed in PRs like code.

## Files

| Kind | Filename | Example |
|---|---|---|
| Epic | `E<NN>-<slug>.md` | `E01-foundations.md` |
| Task | `T<NNN>-<slug>.md` | `T003-skeleton-binaries.md` |

- IDs are never reused or renumbered. Take the next free number.
- Start from [`_template.md`](_template.md).
- Keep files flat in this folder; the epic is the grouping.

## Front matter

```yaml
---
id: T003
title: Skeleton binaries with health endpoints
epic: E01
status: todo          # todo | in-progress | review | done | blocked
depends_on: [T001]
---
```

An epic uses the same fields minus `epic`, and lists its tasks in the body.

## Writing a task

- **Context**: why this exists, in a few lines. Link to `docs/` instead of repeating it.
- **Scope / Out of scope**: what is in, and what is explicitly not.
- **Acceptance criteria**: checkboxes, each an observable behavior. **Each criterion is one test** (see [docs/ENGINEERING.md §2.3](../docs/ENGINEERING.md#23-how-many-tests-one-per-behavior-not-per-function)). If a criterion is covered by a test elsewhere, say where.
- **Notes**: decisions, links, gotchas found while doing it.

## Lifecycle

1. `todo` -> `in-progress` when someone starts (update in the same branch as the work).
2. `review` when the PR is open.
3. `done` when merged: tick the criteria, update the epic's checklist.
4. `blocked`: add a line under Notes saying on what.
