---
name: implement-task
description: Take one agentkit task file (tasks/T0xx-*.md) from todo to review or done - read it, design, write tests and code together, pass make check, review the diff, update the task and epic, push, and report. Use when asked to implement, build, do or finish a task by its ID, or when handed a task prompt from the planner.
---

# implement-task

**Goal: the task's criteria each proven by a passing test, `make check` green, the task file telling the next person what was decided, and a short report.** How the planner and task sessions work together: [WORKFLOW §2](../../../docs/WORKFLOW.md#2-rules-every-session-follows) (the rules) and [§3](../../../docs/WORKFLOW.md#3-handoffs) (handoffs). What "done" means for a task: [ENGINEERING §5](../../../docs/ENGINEERING.md#5-workflow-tasks-folder).

## Steps

1. **Read, in order:** the task file; its epic (decisions, the dependency sketch); the Notes of every task in `depends_on`, especially their "For T0xx" sections; [AGENTS.md](../../../AGENTS.md); ENGINEERING §1 and §2; `go doc -all` of the packages you build on. Check each `depends_on` is `done` (or `review` with what you need on `main`). If not, stop and say which.

2. **Branch and claim.** Start from the latest `main` (`git fetch origin main`, then a branch, or `main` itself if the handoff says so). Set `status: in-progress` in the task file. Touch only the files the task owns plus unavoidable wiring (`go.mod`, `go.sum`, `internal/archtest/rules.go`, `cmd` wiring, docs for the feature); `tasks/STATUS.md` and README status are the planner's.

3. **Design first** when the task adds a package or changes one's shape: run the `design-module` skill and commit its skeleton.

4. **Map criteria to tests** with the `write-tests` skill, before code: each criterion → one test → one tier, plus the errors and boundaries a caller must handle. Then write tests and code together, the smallest change that satisfies the criteria. A criterion that cannot run here (a live key, Docker, a real account) stays unticked.

5. **Follow the repo rules as you go:** a new dependency or anything that moves an image by 100 KB or more gets a measured row in `docs/SIZES.md` ([AGENTS.md Sizes](../../../AGENTS.md#sizes)); a new implementation of a seam goes through its picker ([AGENTS.md Seams](../../../AGENTS.md#seams)); a new top-level Go directory goes into `.dockerignore`.

6. **Gate.** `make check > /tmp/check-T0xx.log 2>&1; echo $?` must print 0. Keep the log until it passes; never pipe the gate through `grep` or `tail`. If it fails only in packages you did not touch and they pass alone, the machine may be loaded (timing tests, the 10-minute test timeout): rerun the whole gate later; a gate that never exited 0 is not green, whatever the reason. Also `go test -race -shuffle=on -count=20` on your packages (`-timeout 30m` for slow ones), `make fuzz` if you touched fuzzed code, and the tier the task touches: `make integration`, `make smoke`, `make web-check` (the `local-env` skill picks how to run it for real).

7. **Mutation self-check and review.** Plant the bugs from `write-tests` step 8 and record them. Then run the `review` skill on your diff, preferably in a fresh session (`claude -p` or `codex exec` with the skill), since your own reading misses what you wrote, and fix every blocking finding. This step is not optional, even for a mechanical diff. If a skill sent you the wrong way, fix the skill in this change ([ENGINEERING §4.3](../../../docs/ENGINEERING.md#43-rules-for-skills)).

8. **Update the task file:** tick only criteria whose test actually ran and passed, with the test's name after each; Notes with decisions and why, the mutation list, anything deferred, and a "For T0xx" section for the tasks that build on this one. Set `status: done` only if every criterion is ticked and `make check` exited 0 on the final commit; otherwise `review` with the reason. Update the box in the epic's checklist in the same commit (`scripts/lint-skills.sh` checks it).

9. **Commit and push.** One commit per task with a message that says what and why. Rebase on `origin/main` right before pushing, rerun `go build ./... && go test` on what you touched, then push (retry network failures with backoff). Push to `main` only if the handoff says so; otherwise push the branch and leave the PR to the planner. Check CI on the pushed commit.

10. **Report** (under 40 lines): branch and commit, what was built, the status and why, decisions, size impact, files outside the task you touched and why, questions for the owner, follow-ups found out of scope (reported, not fixed: WORKFLOW §2 rule 8).

## Done when

- Every ticked criterion names a passing test; unticked ones say what they need.
- `make check` exited 0 on the final commit; `-count=20` is clean.
- The mutation list and decisions are in the task's Notes; the epic box matches the status.
- `review` has no blocking findings; the report is sent.
