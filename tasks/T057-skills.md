---
id: T057
title: "pkg/skill: Agent Skills for the harness (catalog, load, read)"
epic: E12
status: todo
depends_on: [T026, T036]
---

# T057: skills

## Context

Skills are packaged know-how (instructions plus reference files) that the agent loads only when relevant. Decisions: [E12](E12-skills-memory.md) (format, progressive disclosure, no script execution).

## Scope

- **`skill.Load(fsys fs.FS, root string) ([]Skill, error)`**: reads `*/SKILL.md`.
  - Front matter: `name` (the same rule as tool names), `description` (required, at most 1,024 characters), optional `metadata`.
  - Body, plus an index of the other files in the folder.
  - Limits: `SKILL.md` at most 64 KB, files at most 1 MB, at most 200 files per skill.
  - Symlinks leaving the folder are refused.
- **`skill.Catalog`**:
  - `SystemText()`: the name-and-description list for the system prompt, in a stable order so the prompt cache holds;
  - `Toolset()` gives two tools:
    - `skill_load(name)` returns `SKILL.md`;
    - `skill_read(name, path)` returns one file, with paths confined to the folder (no `..` or absolute paths, symlinks checked) and text only (binary files refused with a clear error).
- **Wiring:**
  - config `agent.skills: [{path, optional}]`;
  - `agentkit serve` appends the catalog to the agent's system prompt and adds the toolset;
  - startup fails on an invalid skill unless it is `optional`;
  - `/readyz` lists the loaded skills.
- **Example:** load this repo's own `.agents/skills/` as a test fixture, plus one small demo skill in `deploy/local/agent/skills/` used by `search.json`.

## Acceptance criteria

- [ ] **Valid skills:** each loads with its name, description, body and file index. The real `.agents/skills/` folder loads without errors.
- [ ] **Invalid skills fail with an error naming the skill and the rule:** missing front matter, a bad name, an empty description, oversize files, duplicate names, a symlink out of the folder.
- [ ] **Confinement:** `skill_read` refuses `..`, absolute paths, symlinks leaving the folder and binary files (table test). `FuzzSkillPath` never escapes the folder.
- [ ] **Stable catalog:** `SystemText` is byte-identical across loads and skill orderings (golden file).
- [ ] **End to end** (fake model): the model sees only the catalog in the system prompt, calls `skill_load`, then `skill_read`, and gets the files. Live (`-tags live`): real Claude, given a task that matches a demo skill's description, calls `skill_load` for it.
- [ ] **Mutation list** recorded (at least: path check skipped for a nested `..`, catalog order unstable, full skill bodies in the system prompt).
