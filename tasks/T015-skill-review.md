---
id: T015
title: review skill (with optional second-agent opinion)
epic: E02
status: todo
depends_on: [T013]
---

# T015: `review` skill

## Context

Reviews a diff against our own rules, so review comments are consistent whoever (or whatever) reviews. Can optionally get a second opinion from a different agent.

## Scope

`.agents/skills/review/SKILL.md`, steps:

1. Collect the diff (current branch vs. base, or a given PR/range) and the task file it implements.
2. Check, in order, citing the doc section for each finding:
   - correctness bugs;
   - boundaries and dependency direction (§1.4), package shape (§1.2), design rules (§1.3), component contract (§1.5);
   - tests: one per criterion, right tier, black-box, no brittleness (§2);
   - task file updated; criteria actually covered.
3. **Optional second opinion**: if another agent CLI is on `PATH` (`codex`, `claude`), run it read-only and non-interactive on the same diff with the same checklist (`references/second-opinion.md` has the exact commands and prompt). Merge its findings, drop duplicates, and verify each one before reporting it. If none is available, skip silently.
4. Output findings ranked: **blocking** (bug, rule violation, missing test for a criterion) vs **optional** (style, nits). Each with file:line, what is wrong, and the fix.

`references/checklist.md`: the checklist above in compact form, also usable by humans.

## Out of scope

- Posting to GitHub. The skill produces findings; the agent or person decides where they go.

## Acceptance criteria

- [ ] Run on a branch with planted problems (a `pkg/` → `cmd/` import, a `time.Sleep` in a test, an error-string assertion, a criterion with no test): all four reported as blocking. Fixture branch or patch kept under `.agents/skills/review/testdata/`.
- [ ] Run on a clean change: no blocking findings.
- [ ] With no second agent installed, the skill completes without errors; with one installed, its findings appear merged and marked by source. Recorded in Notes.

## Notes

-
