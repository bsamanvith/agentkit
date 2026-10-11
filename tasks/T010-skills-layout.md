---
id: T010
title: Skills layout, AGENTS.md, cross-agent wiring, skill lint
epic: E02
status: todo
depends_on: []
---

# T010: Skills layout, AGENTS.md, cross-agent wiring, skill lint

## Context

One copy of every skill and every agent instruction, readable by Claude Code, Codex and others. See [ENGINEERING.md §4.1](../docs/ENGINEERING.md#41-where-they-live).

## Scope

- `.agents/skills/` as the single source; `.agents/skills/README.md` with the authoring rules from §4.3 and a skill template.
- `.claude/skills` → symlink to `../.agents/skills`.
- `AGENTS.md` at the root: what the repo is, the key commands (`make check`, `scripts/dev.sh`), where docs and tasks live, the list of skills with one line each. Short: one screen.
- `CLAUDE.md` containing only `@AGENTS.md` (plus anything genuinely Claude-specific, ideally nothing).
- `scripts/lint-skills.sh`, run by `make lint`:
  - every `SKILL.md` has front matter with `name` and `description`; `name` equals the directory name;
  - every relative link in skills, `AGENTS.md` and `tasks/` resolves (including `#anchors` into docs);
  - `.claude/skills` is a symlink to `.agents/skills`;
  - each skill body stays under a line limit (start at 150);
  - task files: no duplicate IDs, every `depends_on` exists, each epic's checklist matches its tasks' `status` ([ENGINEERING.md §5](../docs/ENGINEERING.md#5-workflow-tasks-folder)).

## Out of scope

- The skills themselves (T011–T016).
- User-level or global agent config.

## Acceptance criteria

- [ ] Claude Code lists the repo skills when started in the repo (checked once by hand, recorded in Notes).
- [ ] Codex lists the same skills when started in the repo (checked once by hand, recorded in Notes; if Codex reads a different path, add a second symlink and note it).
- [ ] `lint-skills.sh` fails on: missing `description`, `name`/directory mismatch, a broken link, a broken symlink, a duplicate task ID, an epic checklist out of sync with a task's status. One fixture per case under `scripts/testdata/skills/`.
- [ ] `make lint` runs `lint-skills.sh`.

## Notes

- Progress (after T024/T013): `.agents/skills/write-tests` exists and `.claude/skills` is a symlink to it; Claude Code listing the repo skills (first criterion) has been observed. Still to do: `AGENTS.md`, `CLAUDE.md`, the skills README and template, and the skill and task lint script.

- If `make lint` (E01/T001) does not exist yet, add a minimal `Makefile` target and let T001 absorb it.
- 2026-10-11 (with T013): `.agents/skills/` and the `.claude/skills` → `../.agents/skills` symlink exist, holding `write-tests`. Not done yet: `.agents/skills/README.md`, `AGENTS.md`, `CLAUDE.md`, `lint-skills.sh`.
