---
name: review
description: Review an agentkit diff (the current branch, a PR, or a commit range) against the repo's own rules - correctness, dependency direction and package shape, tests that would catch a wrong implementation, flaky tests, the task file - and report blocking and optional findings with file:line and the fix. Plants bugs to test the tests. Can add a read-only second opinion from another agent CLI (codex, claude) when one is installed. Use when asked to review a change, a branch or a PR, or before handing a task back.
---

# review

**Goal: findings a reviewer would stand behind, each citing the rule it breaks, with the tests themselves tested.** The rules are [ENGINEERING §1](../../../docs/ENGINEERING.md#1-module-design) and [§2](../../../docs/ENGINEERING.md#2-testing); this skill is the order to check them in. The checklist in compact form, also for people: [references/checklist.md](references/checklist.md). The skill produces findings; where they go (a PR comment, the session) is the caller's choice.

## Steps

1. **Collect the change.** The diff (`git diff origin/main...HEAD`, a PR's `gh pr diff <n>`, or the range you were given), the task file it implements (`tasks/T0xx-*.md`, named in the commit or branch), and that task's epic. Read every changed file whole, not only the hunks.

2. **Check, in this order**, and cite the section for each finding:
   1. **Correctness.** Bugs on paths no test names: errors dropped or misclassified, `ctx` not passed or not checked, goroutines without an owner or a wait, shared memory handed to a caller, limits off by one, cleanup on the error path. Prefer one bug you can show over five you suspect.
   2. **Boundaries** ([§1.4](../../../docs/ENGINEERING.md#14-dependency-direction)): run `go test -count=1 ./internal/archtest/` (a cached pass does not see new imports). Then read the imports anyway: `pkg/` or `internal/` importing `cmd/`, a parent importing its own implementation, non-test code importing a `*test` package, a new third-party import without its `rules.go` line and reason.
   3. **Shape and design** ([§1.2](../../../docs/ENGINEERING.md#12-standard-package-shape), [§1.3](../../../docs/ENGINEERING.md#13-design-rules), [§1.5](../../../docs/ENGINEERING.md#15-components-mix-and-match-deployment)): interface in the parent, implementations in subpackages, an `xxxtest`; no `init()` or package state; `ctx` first; sentinel errors; a seam with a second implementation goes through its picker ([AGENTS.md Seams](../../../AGENTS.md#seams)); size rows for new dependencies ([AGENTS.md Sizes](../../../AGENTS.md#sizes)).
   4. **Tests** ([§2](../../../docs/ENGINEERING.md#2-testing)): one per criterion at the right tier; black box; fakes, not mocks; `t.Parallel()`. Blocking: `time.Sleep` or a wall-clock deadline in a test, an assertion on an error or log string instead of `errors.Is`/`As`, golden files for anything but a wire format ([§2.5](../../../docs/ENGINEERING.md#25-rules-that-keep-tests-from-being-flaky-or-brittle)).
   5. **Task file.** Every ticked criterion names a test that exists and tests that criterion (grep for it); an unticked one says why; Notes hold the decisions and the mutation list; the epic's box matches the status (`scripts/lint-skills.sh`).

3. **Test the tests** ([§2.6](../../../docs/ENGINEERING.md#26-invariants-and-simulation-for-code-with-many-paths)).
   - For code with concurrency, state or hostile input, check the invariants are asserted after every run through one helper, not per scenario.
   - Plant 3 to 5 plausible bugs in the changed code, one at a time, from the classes in [the mutation checklist](../write-tests/references/mutation-checklist.md): a value shared instead of copied, a lock dropped, a check weakened, an error class changed, a limit off by one. Include any the task's Notes claim are caught.
   - Run the changed package's tests for each plant, then restore the code exactly (`git diff` must be empty). A bug no test catches is **blocking**, with the invariant or test that would catch it. A plant that does not compile, or that does not change behavior, proves nothing: redo it. A plant caught only on some runs (by the race detector) is caught, but say so.
   - Run each changed test with `go test -race -count=50 -run '<names>'` (`-count=300` if it waits on goroutines or time). Any failure is **blocking**: a flaky test is a bug in the test.

4. **Second opinion (optional).** If another agent CLI is on `PATH` (prefer one other than yourself), run it read-only and non-interactive on the same diff with the same checklist: [references/second-opinion.md](references/second-opinion.md). Verify each of its findings yourself before reporting it, drop duplicates, and mark what you keep with its source. If none is installed, skip this step without comment.

5. **Report**, ranked:
   - **Blocking**: a bug, a rule broken, a criterion with no test, a planted bug that survived, a flaky test.
   - **Optional**: style, naming, nits, a simpler way.

   Each finding: `file:line`, what is wrong, the rule (doc and section), and the fix. End with what you ran (commands and results, the plants and which test caught each) so the author can repeat it.

## Done when

- Every changed file was read whole; archtest ran uncached.
- 3 to 5 bugs were planted and restored, and changed tests ran `-count=50`.
- Each finding has a location, a rule and a fix; second-opinion findings were verified and are marked by source.

Fixtures for checking this skill (a planted-problem change, a weak-test change, a clean change): [testdata/README.md](testdata/README.md).
