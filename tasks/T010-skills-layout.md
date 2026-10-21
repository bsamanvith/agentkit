---
id: T010
title: Skills layout, AGENTS.md, cross-agent wiring, skill lint
epic: E02
status: review
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

- [x] Claude Code lists the repo skills when started in the repo (checked once by hand, recorded in Notes).
- [ ] Codex lists the same skills when started in the repo (checked once by hand, recorded in Notes; if Codex reads a different path, add a second symlink and note it).
- [x] `lint-skills.sh` fails on: missing `description`, `name`/directory mismatch, a broken link, a broken symlink, a duplicate task ID, an epic checklist out of sync with a task's status. One fixture per case under `scripts/testdata/skills/`.
- [x] `make lint` runs `lint-skills.sh`.

## Notes

- Progress (after T024/T013): `.agents/skills/write-tests` exists and `.claude/skills` is a symlink to it; Claude Code listing the repo skills (first criterion) has been observed. Still to do: `AGENTS.md`, `CLAUDE.md`, the skills README and template, and the skill and task lint script.

- If `make lint` (E01/T001) does not exist yet, add a minimal `Makefile` target and let T001 absorb it.
- 2026-10-11 (with T013): `.agents/skills/` and the `.claude/skills` → `../.agents/skills` symlink exist, holding `write-tests`. Not done yet: `.agents/skills/README.md`, `AGENTS.md`, `CLAUDE.md`, `lint-skills.sh`.
- 2026-10-11: Claude Code listed `write-tests` among the session's skills right after `.claude/skills` was pushed (first criterion, observed; not yet ticked because Codex is unchecked).
- 2026-10-21, done (status `review` only for the Codex criterion):
  - `AGENTS.md`: commands, where things are, the skill list; the Seams, Sizes and Cloud sessions sections kept as they were. It names the skills in a table without links, so it stays valid whichever skills exist. `CLAUDE.md` stays `@AGENTS.md`. `scripts/dev.sh` is not named: T006 replaced it with `scripts/k8s-local.sh`, `scripts/smoke.sh` and `scripts/try-agent.sh`.
  - `.agents/skills/README.md`: the §4.3 rules and a `SKILL.md` template.
  - `scripts/lint-skills.sh`, run by `make lint` (then `--test` on its fixtures). Every failure line starts with a check ID (`skill.description`, `link.anchor`, `epic.checklist`, ...; the list is in the script's header). Bash 3.2 and plain awk, so it runs on macOS too (written and run on Linux with mawk; not run on macOS yet). About 4 s on the repo.
  - Fixtures: `scripts/testdata/skills/good/` must pass; each of the 15 `cases/<name>/` (files laid over `good/`, or a `setup.sh`, and an `expect` with the check ID) must fail with **only** its own check. Fixture skill folders are `agents/`, renamed to `.agents/` in a scratch copy, so no agent loads the fixture skills.
- Criterion 1, rechecked 2026-10-21: `claude -p` started in the repo listed `write-connector` and `write-tests` (the skills on `main` then).
- Criterion 2 (Codex) not checked: Codex is not installed here and needs an OpenAI login. Codex reads repo skills from `.agents/skills` as far as we know, so no second symlink was added. Left unticked.
- Decisions:
  - **Epic checklist rule.** A task's box is checked only in the epic named by its `epic:` field (a task moved to another epic keeps a line in the old one, like T019 in E01). `done` needs a ticked box; `todo`, `in-progress` and `blocked` need an empty one; `review` may be either, because both are in use on `main` today (T047 ticked, T006 not).
  - **Links checked** in `AGENTS.md`, `CLAUDE.md`, `.agents/skills/**/*.md` and `tasks/*.md`: inline links outside code, relative or repo-rooted; `#anchors` into Markdown files by GitHub's slug rule (lower case, punctuation dropped, spaces to hyphens, `-1` for a repeated heading) or an `<a id>`. URLs with a scheme are not fetched.
  - **Task files** are `tasks/[TE][0-9]*.md`; `README.md`, `STATUS.md` and the `_` templates are not tasks. The `id` must match the file name's prefix.
  - Three epic boxes on `main` were out of sync and the lint stopped on them, so this change ticks them: T007 (E01), T037 (E09) and T088 (E18), all `done`. These are the only edits outside T010's own files besides the `Makefile` `lint` target.
- Mutation self-check: 19 bugs planted one at a time in a copy of the script, each failing `--test`:
  - no `-1` suffix for repeated headings;
  - links read inside code fences, or inside inline code;
  - URLs checked as files;
  - `<a id>` anchors ignored;
  - anchors not checked;
  - the epic rule applied in every epic that lists a task;
  - `review` treated as `done`;
  - a ticked `todo` accepted;
  - duplicate ids allowed;
  - name not checked;
  - empty description accepted;
  - length limit off by one;
  - the `.claude/skills` target not checked;
  - broken symlinks ignored;
  - missing dependencies accepted;
  - any status accepted;
  - a folder without `SKILL.md` accepted;
  - missing front matter accepted.

  The first round had two survivors: the length limit off by one (the case was 160 lines, not 151) and the symlink target (no fixture had a symlink to the wrong place). That led to `cases/claude-wrong-target` and an exact 151-line case. A third weakness came from a mutant that "was caught" for the wrong reason: the case also broke an anchor. Since then, a case must fail with its own check and nothing else.

