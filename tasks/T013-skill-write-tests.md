---
id: T013
title: write-tests skill
epic: E02
status: in-progress
depends_on: [T010, T017]
---

# T013: `write-tests` skill

## Context

Makes agents write few, solid, behavior-level tests instead of many brittle ones, and tests that fail when the code is wrong, not only when it is changed. Source of truth: [ENGINEERING.md §2](../docs/ENGINEERING.md#2-testing), including [§2.6](../docs/ENGINEERING.md#26-invariants-and-simulation-for-code-with-many-paths).

Lesson from T022 (`pkg/harness`): its first tests had one test per criterion and passed, yet review found nine blocking bugs on paths no criterion named. A test per criterion proves each criterion once; it says nothing about what must hold on every path. The skill therefore asks for both.

## Scope

`.agents/skills/write-tests/SKILL.md`, steps:

1. List the task's acceptance criteria; map each to one test and one tier (§2.2). Show the mapping before writing code.
2. If the interface has (or will have) more than one implementation, put behavior tests in the `xxxtest` conformance suite, not next to one implementation (§2.4).
3. Write black-box tests (`package foo_test`) using fakes, not mocks; `t.Parallel()`; table-driven only when inputs vary.
4. Apply the anti-brittleness rules (§2.5): no sleeps, no message-string asserts, golden files only for wire formats, recorded fixtures for providers.
5. **Name the invariants** (§2.6): for each public entry point, write down what must hold for *every* call or run whatever the input: outputs valid, nothing the caller got back shares memory with internal state, what was sent is never changed afterwards, events or results well-formed, totals add up, no goroutine or resource outlives the call. Put the checks in the one shared test helper that every test goes through, so all tests check all of them.
6. **For code with many paths** (concurrency, cancellation, state machines, parsers, anything that takes hostile input), add a seeded simulation through the same helper, plus a fuzz target over the seed: generate scripts including inputs no sane caller sends, vary what the *consumer* does (stop, panic, cancel, write to what it is handed), and compare runs where the property is a comparison. Where a function's job is to judge validity, compare it with an independent restatement of the rules (a second implementation written from the docs), never with itself.
7. **Concurrency tests are driven from the consumer's side**: what it does when it sees an event happens at a fixed point of the run. Never rely on which goroutine is scheduled first. A wait for something that must not happen is allowed only if it can make a broken implementation pass, never fail a correct one; say so in a comment.
8. **Mutation self-check** before finishing: plant, one at a time, the bugs a reviewer would look for and confirm a test fails for each (not a compile error): results in the wrong order, a value shared instead of copied, data dropped or edited, a budget or limit off by one or not enforced, a missing cancellation check, a wrong error class, a missing wait for goroutines, a validation weakened. A bug that survives means a missing general check: add the invariant, not a test for that one case. Record the list and the catching tests in the task Notes.
9. Self-check: delete any test that duplicates a lower tier, tests a getter/option/generated code, or asserts internals.
10. Run with `-race -shuffle=on -count=20` locally, and `-count=300` for any test that depends on timing or goroutine order (a 3% flake passes `-count=3` nine times in ten).

`references/examples.md`: one good and one bad example for each rule, taken from `pkg/tool` (T017).

`references/invariants.md`: the worked example of steps 5 to 8, from `pkg/harness` (T022): `invariants_test.go` (the checks), `helpers_test.go` (`rig.drive`, the recorder that keeps each request twice to catch changes after sending), `sim_test.go` (the simulation, the scribbling consumer), `resume_test.go` (`referenceValid`, the independent oracle).

`references/mutation-checklist.md`: the bug classes of step 8, with the T022 list of 29 planted bugs as a model.

## Acceptance criteria

- [ ] Dry run on T018 (MCP stdio server): test count equals criteria count ± documented extras (error cases, regressions). Recorded in Notes.
- [ ] Dry run tests pass `-race -shuffle=on -count=20`.
- [x] Dry run on a package with concurrency or hostile input: the invariants are listed and checked after every run through one helper, a seeded simulation and fuzz target exist, and the mutation self-check was run with no survivor. Recorded in Notes with what the first round missed.

## Notes

- 2026-10-11: first version written after T024, at the user's request: `.agents/skills/write-tests/SKILL.md` (steps 1-10 above, condensed) and `references/`:
  - `examples.md`: good/bad pairs, taken from `pkg/agentstep`, `cmd/agentkit` and `pkg/llm/anthropic` rather than `pkg/tool`.
  - `invariants.md`: `pkg/harness` and `pkg/agentstep`.
  - `mutation-checklist.md`.
  - `e2e-replay.md`: the replay tier from T024.
  - `perf.md`: benchmarks and load tests, from T018.
- Scope added beyond the plan above:
  - **Behaviors over volume** as the headline: test outcomes a caller depends on, prefer comparisons (interrupted vs. uninterrupted run) to pasted expectations, prune tests that break on refactors.
  - **Replay e2e** and **perf** tiers, now in ENGINEERING §2.2 and the new §2.7.
  - **Evals** are named as a later tier: the skill says not to write them yet.
- `.claude/skills` → `../.agents/skills` symlink added so Claude Code loads it (part of T010; the rest of T010 — `AGENTS.md`, the skill lint — is still open).
- 2026-10-11, dry runs, each by a fresh agent that had only the skill and the task spec:
  - **Criterion 3, on `pkg/flow`: done and merged** (commit "pkg/flow: invariants after every run, seeded simulation, FuzzSimulatedFlow (T013 dry run)", 2026-10-12). The agent named 13 invariants (state vs. error, refused calls change nothing, event grammar and nesting, checkpoint well-formedness and frontier, records only after Finished, progress never lost, finished steps never re-run, rejected and completed runs stay so). It checked them after every Run/Resume through one helper, routing 25+ existing tests through it. It added a seeded simulation of random step trees with failures, panics, cancellations, failed saves and approvals: 300 seeds per run, about 1 s under `-race`, checked against an independent reference interpreter. It also added `FuzzSimulatedFlow`.
    - Mutation check, 21 plants: the original tests missed 6; round 1 still missed 3; round 2 missed none.
    - What round 1 missed:
      - observer calls not serialized: needed a race-visible check;
      - branch index taken from goroutine start order: needed each leaf to check its own path;
      - pending approvals not sorted: the check existed but the generator never made Parallels wider than 10.
    - `-count=20` and `-count=300` clean; fuzz 30 s clean.
    - **It found a real bug in `pkg/flow`**, reproduced deterministically by `TestResumeOfARejectedRunIsRejectedEvenIfASiblingFails`. When a rejection inside a `Parallel` cancels a sibling branch, `Resume` runs that sibling again, returns its error instead of `ErrRejected`, and flips the stored status from `rejected` to `failed`. This contradicts "a rejection is final" (T023).
    - Proposed fix, about 5 lines: in `Resume`, after the shape check, if `ckpt.Status == StatusRejected`, return the recorded `RejectedError` (smallest rejected path) without running anything. Then drop the simulation's temporary tolerance `failedBesideRejection` and also assert that such a Resume emits no events.
    - **2026-10-12: the fix is on `main`** (`a645012`, by the planner with the owner's approval, with its own regression test; see T023). The dry run's tests were then merged on top with the owner's OK in the T024 session. The duplicate regression test was dropped, the tolerance removed, the invariants were brought up to date with T032's later fingerprint rule, and two newer tests were moved to the helper's `observer(...)` form so their observers are not replaced. Re-planting the bug fails `TestResumeOfARejectedRunIsRejectedEvenIfASiblingFails`, `TestSimulatedFlows` and `TestApproveRejectionEndsRun`.
    - Not applied here: `pkg/flow` belongs to the flow tasks, and the change was left to its owner. The new tests stay unmerged until the fix lands, since they include the failing regression test. They are in the local worktree branch `worktree-agent-a33f09ba8020c8abe`, which is not pushed.
  - **Criteria 1 and 2, on T018 (`pkg/mcp/server`): stopped before finishing**, at the user's request to wrap up. No result. Rerun later the same way: move the package's tests aside, have a fresh agent write them from T018's criteria with the skill, then compare count, mutations and `-count=20`.
  - **The skill was improved from the flow agent's notes**:
    - per-call plus cross-call invariant checks;
    - check only documented guarantees, including ownership;
    - chain single-slot options in the helper;
    - goroutine proxies under `t.Parallel()`;
    - reference interpreters as the oracle;
    - assert the generator's case mix;
    - fuzz new invariants;
    - gates need an escape;
    - the mutation driver counts timeouts and races and uses a private directory;
    - note catches that are timing luck;
    - how to handle a real bug found while testing.
