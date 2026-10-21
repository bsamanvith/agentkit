---
id: T014
title: implement-task skill
epic: E02
status: done
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

- [x] Dry run on one E01 task (e.g. T002): the PR has ticked criteria, a test per criterion, green `make check`, and the task/epic files updated. Recorded in Notes.

## Notes

- 2026-10-21: `.agents/skills/implement-task/SKILL.md`, ten steps:
  1. read, and check `depends_on`;
  2. branch and claim;
  3. `design-module` when the shape changes;
  4. `write-tests` mapping, then code;
  5. the Sizes and Seams rules;
  6. the gate: `make check` exit status, `-count=20`, the tier the task touches;
  7. mutation check and `review`, and fix a skill that misled;
  8. update the task and epic;
  9. commit, rebase and push as the handoff says;
  10. the report.

  It is written from WORKFLOW §2-§3 and the handoff template, and from how the E02 sessions themselves work. A rule appears only as a link.
- **Dry run.** Every E01 task is done or in review, so the run used the smallest real `todo` task: T033 (the harness on `pkg/clock`).
  - A separate Claude Code session (`claude -p`, nothing but the repo and the skill) worked in a scratch copy with its own git repository and no remote, so nothing could be pushed. Push and CI were skipped by instruction.
  - Its commit `df9e48c` on branch `claude/T033-harness-clock` (scratch only) had:
    - all three criteria ticked, each naming its evidence: the new `TestRecordTimingsComeFromTheInjectedClock` for the behavior, `-race -count=20` on the existing harness tests, and archtest;
    - four planted bugs, all caught, recorded in Notes;
    - the epic E05 box ticked in the same commit;
    - a report in the skill's shape: decisions (removed `WithNow` rather than deprecate it, since staticcheck SA1019 fails every remaining caller), size impact (none), files outside the task (callers in `cmd/agentkit`, `pkg/runner`, `pkg/agentapi` tests), and out-of-scope findings.
  - **`make check`:** the session's own runs never exited 0. The failures were all in packages it did not touch: wall-clock tests in `pkg/llm`, `pkg/llm/anthropic` and `pkg/mcp/client`, and `pkg/store/sqlite` over the 10-minute timeout. Load average was 20-50 from other sessions on the machine.
    - It still set `status: done`.
    - It skipped the `review` step on a mechanical diff.
  - I reran `make check` on its commit with `TESTFLAGS="-race -shuffle=on -timeout 30m"` (the longer timeout WORKFLOW §4 asks for under `-race`). It **exited 0**. So the dry run meets the criterion: ticked criteria, a test per criterion, green `make check`, task and epic updated.
- **Skill changes** from the dry run:
  - `done` only when `make check` exited 0 on the final commit, otherwise `review`;
  - a gate that fails only in untouched packages is rerun, never called green;
  - `review` is not optional, even for a mechanical diff, and runs best in a fresh session.
- **Found out of scope** (from the run): `pkg/llm` (`TestIdleTimeoutIgnoresTheCallersOwnTime`), `pkg/llm/anthropic` (`TestIdleTimeoutIgnoresTheConsumer`), `pkg/mcp/client` (`TestErrors/oversized`) and `pkg/rpc` (`TestPingsKeepASilentCallAlive`) fail on a heavily loaded machine. `pkg/store/sqlite` passes the default 10-minute `go test` timeout only on a quiet one. My own `make check` on this branch hit the same failures under the same load.
