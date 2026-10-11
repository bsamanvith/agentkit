---
id: T013
title: write-tests skill
epic: E02
status: todo
depends_on: [T010, T017]
---

# T013: `write-tests` skill

## Context

Makes agents write few, solid, behavior-level tests instead of many brittle ones. Source of truth: [ENGINEERING.md §2](../docs/ENGINEERING.md#2-testing).

## Scope

`.agents/skills/write-tests/SKILL.md`, steps:

1. List the task's acceptance criteria; map each to one test and one tier (§2.2). Show the mapping before writing code.
2. If the interface has (or will have) more than one implementation, put behavior tests in the `xxxtest` conformance suite, not next to one implementation (§2.4).
3. Write black-box tests (`package foo_test`) using fakes, not mocks; `t.Parallel()`; table-driven only when inputs vary.
4. Apply the anti-brittleness rules (§2.5): no sleeps, no message-string asserts, golden files only for wire formats, recorded fixtures for providers.
5. Self-check before finishing: delete any test that duplicates a lower tier, tests a getter/option/generated code, or asserts internals.
6. Run with `-race -shuffle=on -count=3` locally to shake out flakes.

`references/examples.md`: one good and one bad example for each rule, taken from `pkg/tool` (T017).

## Acceptance criteria

- [ ] Dry run on T018 (MCP stdio server): test count equals criteria count ± documented extras (error cases, regressions). Recorded in Notes.
- [ ] Dry run tests pass `-race -shuffle=on -count=3`.

## Notes

-
