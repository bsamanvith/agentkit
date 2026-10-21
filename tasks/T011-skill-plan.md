---
id: T011
title: plan skill
epic: E02
status: done
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

- [x] Dry run: planning the M3 epic (`llm` + Anthropic adapter + harness) produces files that pass `lint-skills.sh` link checks and match the template. Recorded in Notes.
- [x] Dry run output has no criterion a reviewer marks as untestable. Recorded in Notes with any skill changes made.

## Notes

- 2026-10-21: `.agents/skills/plan/SKILL.md` written from how E04 and T020 were planned (they are its worked example). It has eight steps: read; take the next free IDs; ask only what changes the breakdown; the epic; the tasks from `_template.md`; one observable behavior per criterion, with its test name; small tasks; lint. Every rule is a link to tasks/README.md, ENGINEERING §2.3, §2.6 and §5, or WORKFLOW.
- **Dry runs**, each by a separate Claude Code session (`claude -p`, no context but the repo and the skill), in a scratch copy of the repo. E04, T020-T025 and `pkg/llm`, `pkg/harness`, `pkg/flow`, `pkg/agentstep` were removed, so M3 was unplanned. The prompt asked for M3 (`llm`, the Anthropic adapter, the harness) with the plan skill, no questions, assumptions recorded. Nothing was pushed.
  - **Run 1** wrote E04 and five tasks: T020, T021 and T022, reused because other tasks link them, plus T112 and T113, the next free IDs.
    - Lint: the new files are clean. The 4 problems left were links and `depends_on` in existing tasks pointing at T023-T025, which were removed for the run.
    - Every file matches the template; every criterion names a test.
    - As the reviewer I found no untestable criterion, but three weak ones:
      - T113 had "the mutation self-check is done" as a criterion: process, not behavior.
      - T021 joined three behaviors with semicolons ("`Effort` is sent; another provider's option is skipped; an unsupported option fails").
      - T021's caching criterion joined two behaviors.
  - **Skill change** from run 1 (step 6): behaviors joined by semicolons count as joined by "and" (split them, or make one table test whose rows are cases of one rule, and say so); process items that every task does (mutation check, `make check`) are not criteria.
  - **Run 2**, with the changed skill, wrote E04 and seven tasks (T020-T022, T112-T115), 71 criteria in all.
    - Lint: clean for the new files.
    - Template: every task has the five sections, and every criterion names its test.
    - No process criteria. Compound cases are written as table tests of one rule (stop reasons, error mapping, transcript shapes). Live criteria say they need the key and stay in review.
    - I found no criterion I would mark untestable or vague.
    - The split is finer than the real E04 (four harness tasks instead of one), which the skill's "about one PR each" asks for.
  - The run's output stays in the session's scratch directory; nothing from it was committed.
