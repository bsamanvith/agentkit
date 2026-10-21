---
id: T057
title: "pkg/skill: Agent Skills for the harness (catalog, load, read)"
epic: E12
status: review
depends_on: [T026, T036]
---

# T057: skills

## Context

Skills are packaged know-how (instructions plus reference files) that the agent loads only when relevant. Decisions: [E12](E12-skills-memory.md) (format, progressive disclosure, no script execution).

## Scope

- **`skill.Load(fsys fs.FS, root string) ([]Skill, error)`**: reads `*/SKILL.md`.
  - Optional front matter **`tools:`**: tool names or globs the skill auto-loads when it is loaded (used by T085, [E17](E17-tool-safety-control.md)).
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

## References

- Anthropic Agent Skills: the overview and the `SKILL.md` format in the Claude docs (find the current page with the `claude-api` skill), and [Equipping agents for the real world with Agent Skills](https://www.anthropic.com/engineering/equipping-agents-for-the-real-world-with-agent-skills) (progressive disclosure)
- This repo's `.agents/skills/` (real skills to load as fixtures)
- [Go `io/fs`](https://pkg.go.dev/io/fs) and `os.Root` (Go 1.24+) for confined file access

## Acceptance criteria

- [x] **Valid skills:** each loads with its name, description, body and file index. The real `.agents/skills/` folder loads without errors. — `TestLoadValid` (every front-matter form, metadata, tools, an in-folder link, `.git` skipped), `TestLoadRepoSkills` (`.agents/skills` through `os.Root`)
- [x] **Invalid skills fail with an error naming the skill and the rule:** missing front matter, a bad name, an empty description, oversize files, duplicate names, a symlink out of the folder. — `TestLoadInvalid` (22 cases, each an `*skill.Error` with `Folder` and `Rule`), `TestLoadSymlinkOnDisk`, `TestFrontMatterRefusals`, `TestNewCatalogDuplicates`; through serve: `TestServeSkillsInvalid`
- [x] **Confinement:** `skill_read` refuses `..`, absolute paths, symlinks leaving the folder and binary files (table test). `FuzzSkillPath` never escapes the folder. — `TestReadConfinement` (through the tool, on `fstest.MapFS`, with files swapped for links after the load), `TestReadConfinementOnDisk` (`os.Root` and `os.DirFS`), `FuzzSkillPath` (ran 20 s locally, 176k execs)
- [x] **Stable catalog:** `SystemText` is byte-identical across loads and skill orderings (golden file). — `TestSystemTextStable`, `testdata/catalog.golden.txt`
- [ ] **End to end** (fake model): the model sees only the catalog in the system prompt, calls `skill_load`, then `skill_read`, and gets the files. Live (`-tags live`): real Claude, given a task that matches a demo skill's description, calls `skill_load` for it.
  - fake model: done, `TestEndToEnd` (harness) and `cmd/agentkit.TestServeSkills` (through serve's wiring and the agent API).
  - live: **not run**, so the box stays unticked. `cmd/agentkit.TestLiveSkillLoad` (`-tags live`) is written and compiles; it skipped here because this session has no Anthropic credentials (`ANTHROPIC_API_KEY` unset, no `ant`). Run `go test -tags live -run TestLiveSkillLoad ./cmd/agentkit` with a key.
- [x] **Mutation list** recorded (at least: path check skipped for a nested `..`, catalog order unstable, full skill bodies in the system prompt). — below

## Notes

### Done (2026-10-21)

- `pkg/skill`: `Load(fsys, root)`, `Skill` (`Name`, `Description`, `Metadata`, `Tools`, `Body`, `Files`, `Read`), `NewCatalog`, `Catalog.SystemText`, `Catalog.Toolset` (`skill_load`, `skill_read`), `Catalog.Get`/`Names`/`Skills`; errors `ErrInvalidSkill` (as `*Error{Folder, Name, Rule, Err}`), `ErrNotFound`, `ErrPath`, `ErrBinary`, `ErrTooLarge`.
- Serve: `agents[].skills` (`cmd/agentkit/skills.go`), in `newHarness`, so both the in-process agent API and the `runner` component load them. `/readyz` lists each agent's skills under the component's `details.skills` and is degraded for a skipped optional folder.
- `pkg/component`: an optional `Reporter` interface (`Report() map[string]any`), shown as `details` in a component's `/readyz` entry (`TestReadyzDetails`).
- Demo skill `deploy/local/agent/skills/research-brief` (with `references/format.md`), used by `search.json`'s `search` agent (`TestSearchConfigSkills`). `scripts/try-agent.sh --docker` mounts the `skills` folder next to the config.
- QUICKSTART: the `skills` field and a "Skills" subsection in section 7.

### Decisions

- **Front matter reader, no YAML dependency.** `go.yaml.in/yaml/v4` (an rc) is linked today only through the Anthropic SDK; using it would add it to the API and toolsets images and make it a reviewed direct dependency for five keys. `pkg/skill` reads the subset skills use: plain, quoted and multi-line plain scalars, `|`/`>` block scalars with `-`/`+`, flow and block lists, a one-level map, comments. Anything else (anchors, tags, flow maps, deeper nesting, `|2`) is refused with the line number, never misread. Unknown keys (`license`, `allowed-tools`, `compatibility`) are parsed and ignored. Revisit if a real skill needs more: swap `parseYAML` for the library, the rest stays.
- **Name rule** is the tool name rule `^[a-zA-Z0-9_-]{1,64}$` (task), which admits the Agent Skills spec's lowercase-and-hyphens names. The name does not have to match the folder (Claude Code does not require it either).
- **A folder under the root without `SKILL.md` is an error** (`RuleSkillMD`), so a misnamed `skill.md` fails loudly. Folders starting with `.` and plain files under the root are ignored; `.git` inside a skill is skipped.
- **Files.** The index lists every regular file (and in-folder link to one) except `SKILL.md`, binaries included (images are references too); `skill_read` serves text only (UTF-8, no NUL). The 200-file limit counts `SKILL.md`. A link to a folder inside the skill is allowed but its contents are not indexed.
- **Confinement** is checked twice: at load (every link resolved element by element through `fs.ReadLinkFS`; one leading out of the folder fails the skill) and at every read (only indexed paths; `..` elements, absolute and `\` paths refused before anything is opened; links resolved again, so a file swapped for a link after the load is refused). Serve opens each folder as an `os.Root`, so even a link swapped in between the check and the open cannot leave the root. An `fs.FS` without `ReadLinkFS` (embed.FS) is taken to have no links.
- **`skill_load` returns** `{name, instructions, files: [{path, size}]}`: the body without the front matter, plus the index the model needs for `skill_read`. `skill_read` returns `{name, path, content}`; `SKILL.md` itself is readable too.
- **Catalog text** is a `<skills>` block: one sentence on how to use the tools, then `- name: description` per skill sorted by name, descriptions with whitespace collapsed to one line. Empty catalog: no block. It is appended to the configured system prompt after a blank line; the skill tools are added after the agent's toolsets (and before runtime toolsets). A toolset tool named `skill_load`/`skill_read` fails startup unless `prefix_tool_names`.
- **Paths in config** are relative to the config file's folder (skills ship with the config), unlike the `files` toolset's `root` (relative to the working directory, since it is workspace data). An entry may be a plain string (a required folder) or `{path, optional}`; names must be unique across an agent's folders.
- **`GET /v1/agents`** still shows the configured system prompt, not the catalog: in the split topology the API process does not load skills. The skills are listed in `/readyz` for now (see "For T087").
- **Scripts** are never run (E12); the catalog sentence says so.

### Size

+213 KB on the default binary, +205 to +258 KB on the variants (docs/SIZES.md row). Projected under every image budget; agentkit-toolsets is closest (about 16.8 of 17 MB) because serve's harness wiring, and so `pkg/skill`, is linked into every variant.

### Mutation list (each caught by a failing test)

Path check skipped for a nested `..` (`TestReadConfinement`); catalog order unstable, sort removed (`TestSystemTextStable`); full skill bodies in the system prompt (`TestSystemTextStable`, `TestEndToEnd`); symlinks not checked (`TestReadConfinement`, `TestReadConfinementOnDisk`, `TestLoadInvalid`); binary files served (`TestReadConfinement`); description length unchecked (`TestLoadInvalid`); reads not limited to the index (`TestReadConfinement`); duplicate names allowed (`TestLoadInvalid`, `TestNewCatalogDuplicates`); absolute link targets followed as relative (`TestReadConfinement`); file size unchecked and file count unchecked (`TestLoadInvalid`); folded block scalar read as literal (`TestLoadValid`); catalog not added to the prompt (`TestServeSkills`); skipped optional folders not degraded (`TestServeSkills`); `optional` ignored (`TestServeSkillsInvalid`); skill paths not made relative to the config (`TestSearchConfigSkills`, `TestServeSkills`); `/readyz` details dropped (`TestReadyzDetails`).

### For T085 (deferred tool loading)

`Skill.Tools` holds the front matter's `tools:` (names or globs, validated with `path.Match` and a name-like pattern; a list, or one string split on spaces and commas). Nothing reads it yet: `skill_load` is a plain `tool.Func` on the catalog. To load tools when a skill loads, wrap the `skill_load` tool (or give `Catalog.Toolset` an option with a callback `func(ctx, Skill)`), and intersect `Skill.Tools` with the agent's deferred tools so a skill can't add tools the agent doesn't have.

### For T076 (skills store) and T087 (console)

- `Load` takes any `fs.FS`: a store can hand it an in-memory FS of an uploaded folder (`fstest.MapFS`-like) and reuse every check; `NewCatalog` merges sources and rejects duplicates.
- `GET /v1/agents` does not list an agent's skills yet; adding them (names and descriptions, from `Catalog.Skills`) is the console's "Skills on the agent detail" slot. In the split topology only the runner knows them, so the API needs them from config or from the runner.
