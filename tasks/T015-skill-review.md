---
id: T015
title: review skill (with optional second-agent opinion)
epic: E02
status: done
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
   - tests: one per criterion, right tier, black-box, no brittleness (§2); **and whether they would catch a wrong implementation** (below);
   - task file updated; criteria actually covered.
3. **Test the tests** (§2.6): for a change with concurrency, state or hostile input, check that invariants are asserted after every run and not only per scenario. Then plant 3 to 5 plausible bugs in the changed code (the bug classes in `write-tests/references/mutation-checklist.md`), run the changed tests, and report every bug that no test catches as **blocking**, with the invariant that would have caught it. Run each changed test `-race -count=50` (`-count=300` if it depends on timing) and report any failure as blocking: a flaky test is a bug in the test.
4. **Optional second opinion**: if another agent CLI is on `PATH` (`codex`, `claude`), run it read-only and non-interactive on the same diff with the same checklist (`references/second-opinion.md` has the exact commands and prompt). Merge its findings, drop duplicates, and verify each one before reporting it. If none is available, skip silently.
5. Output findings ranked: **blocking** (bug, rule violation, missing test for a criterion) vs **optional** (style, nits). Each with file:line, what is wrong, and the fix.

`references/checklist.md`: the checklist above in compact form, also usable by humans.

## Out of scope

- Posting to GitHub. The skill produces findings; the agent or person decides where they go.

## Acceptance criteria

- [x] Run on a branch with planted problems (a `pkg/` → `cmd/` import, a `time.Sleep` in a test, an error-string assertion, a criterion with no test): all four reported as blocking. Fixture branch or patch kept under `.agents/skills/review/testdata/`.
- [x] Run on a branch whose tests pass but are weak (a test that still passes with the bug planted; a timing-dependent concurrency test that fails 1 run in 30): both reported as blocking.
- [x] Run on a clean change: no blocking findings.
- [x] With no second agent installed, the skill completes without errors; with one installed, its findings appear merged and marked by source. Recorded in Notes.

## Notes

- 2026-10-21:
  - `.agents/skills/review/SKILL.md` has five steps: collect the change; check in order (correctness, boundaries, shape, tests, task file); test the tests; an optional second opinion; a ranked report.
  - `references/checklist.md` is the compact checklist, marked blocking or optional.
  - `references/second-opinion.md` has the exact `codex exec --sandbox read-only` and `claude -p --allowedTools ...` commands and the prompt.
  - `testdata/` holds three patches and a README with the expected findings. Each patch adds `pkg/tally` (a counter safe for concurrent use) and `tasks/T900-review-fixture.md`. They only add files, so they apply to any later `main`.
- **Dry runs**: each by a separate Claude Code session (`claude -p`, nothing but the repo and the skill). Each ran in a scratch copy with the patch committed on a `base` branch, and the fixtures' README and patches deleted so the reviewer could not read the expected answer. Nothing was pushed.
  - **`planted.patch`** (criterion 1), with `claude` on `PATH` for the second opinion. All four problems were reported as blocking:
    - `pkg/tally` importing `cmd/reviewfixture/flags` (it also ran archtest);
    - the `time.Sleep` (it reproduced the flake at `-count=50`);
    - the error-string assertion;
    - `TestAddDefault` named by a ticked criterion but missing (its plant in `AddDefault` survived).

    It added a fifth blocking finding: the task's mutation claims were only partly true.
  - **`weak.patch`** (criterion 2). Both problems were reported as blocking:
    - `TestSnapshotIsACopy` passes with `Snapshot` returning the internal map. It planted that bug, and showed that the Notes' claim was false.
    - The 70 ms wall-clock wait in `TestConcurrentAdds`: 5 failures in `-count=50` on a loaded machine (load average about 50 on 4 CPUs).

    It also flagged the goroutines nobody waits for.
  - **`clean.patch`** (criterion 3), with no second agent on `PATH` (criterion 4). The first run found a real gap in the "clean" fixture: dropping the lock in `Get` survived, because no test read `Get` concurrently. The fixture was fixed (a concurrent `Get`, and `doc.go`), and the other two patches were regenerated from it.
    - The rerun found **no blocking findings** and two optional ones: T900 is not in E02's list, and the race on `Snapshot` is caught only with `-count` above 1.
    - It ran 8 plants and said in one line that no second opinion ran, because neither CLI was on `PATH`.
  - **Second opinion** (criterion 4): in the planted and weak runs, `codex` was absent, so the reviewer used `claude -p` read-only. It verified the findings and marked each `[review]`, `[claude]` or `[review, claude]`, and it corrected the second agent's diff-relative line numbers.
  - The second opinion through `codex` was not run, since Codex is not installed here. Its command is from Codex's CLI documentation.
- **Skill change** from the clean runs: a plant that does not compile or does not change behavior proves nothing and is redone; a plant caught only on some runs counts, but the report says so.
- **Found out of scope:** `go test ./internal/archtest/` can pass from the test cache after a new import is added. archtest reads the import graph through `go list` in a subprocess, which the cache does not track. A pkg → cmd import passed with `(cached)` and failed with `-count=1`. CI starts with a cold cache, so CI catches it; a local `make check` may not. The skill runs archtest with `-count=1`. A fix in `internal/archtest` (or `-count=1` for that package in `make test`) is for the planner to schedule.
