---
id: T012
title: design-module skill
epic: E02
status: done
depends_on: [T010, T017]
---

# T012: `design-module` skill

## Context

Gives every new package the same shape and keeps dependency direction and component wiring right from the start. Source of truth: [ENGINEERING.md §1](../docs/ENGINEERING.md#1-module-design).

## Scope

`.agents/skills/design-module/SKILL.md`, steps:

1. Place the package in the dependency layers (§1.4); name what it may and may not import.
2. Define the smallest interface(s) callers need; list sentinel errors.
3. Lay out subpackages: implementations, `xxxtest` (fake + conformance suite).
4. If it is deployable, define the `Component` and its local/remote dependencies (§1.5), including the remote client that implements the same interface.
5. Note any heavy dependency and how it is isolated (subpackage, build tag, thin client instead of SDK) (§1.1).
6. Output: a short design note in the task file's Notes (or `docs/design/<pkg>.md` if long), plus `doc.go` and the interface file as a first commit.

## Acceptance criteria

- [x] Dry run on the next new package (`llm`, M3): the output passes a `review` (T015) with no boundary or shape findings. Recorded in Notes.
- [x] The skill links §1 sections rather than restating rules (checked in review).

## Notes

- 2026-10-21: `.agents/skills/design-module/SKILL.md` written from `pkg/tool` (T017), `pkg/llm` (T020's Design) and `pkg/store` (implementations, `storetest`, the `storeopen` picker). Its six steps:
  - place the package in the layers and check `internal/archtest/rules.go`;
  - define the smallest interface, its errors and who owns what;
  - lay out the subpackages and `xxxtest`;
  - name the component and the remote wiring;
  - isolate heavy dependencies, per image;
  - write the note and commit the skeleton.

  Each step links its §1 section (§1.1-§1.5), [COMPONENTS.md](../docs/COMPONENTS.md) or AGENTS.md Seams/Sizes, and gives the rule only as a short pointer.
- **Dry run.** `llm` was built long ago, so the run used the next real new package, `pkg/skill` (T057, `todo`). Each round ran in a scratch copy:
  - a separate `claude -p` session designed T057 with only the skill and committed the skeleton;
  - a second fresh session reviewed that commit with the `review` skill.

  Nothing was pushed, and the T057 design stays in the scratch copies: T057 belongs to its own session.
  - **Round 1:** four blocking findings, two of them design rules.
    - The catalog was documented as immutable but handed out shared maps, and `NewCatalog` accepted a `Skill` that never went through `Load` (§1.3).
    - Behaviors the design added had no acceptance criteria.
    - Others: loose zero values, and a `go.mod` line moving from indirect to direct unmentioned.

    Skill changes:
    - step 2 decides ownership and makes the constructor the only way to a valid value;
    - step 5 states `go.mod` changes;
    - step 6 adds a criterion for every behavior the design adds, and runs `make lint` and archtest `-count=1` on the skeleton.
  - **Round 2:** ownership and criteria were fixed. New blocking findings were on the component contract and dependency isolation:
    - `/readyz` was to list skills from a process that, in the split deployment, never loads them (§1.5);
    - the YAML dependency was said to cost nothing because the Anthropic SDK already links it, which is false for the `api` and `toolsets` images (§1.1, AGENTS.md Sizes);
    - a stub panicked on the zero value.

    Skill changes:
    - step 4 says which split-deployment process holds each piece of state, and how another process gets it;
    - step 5 checks each image with `make print-tags-<variant>` and `go list -deps -tags`;
    - step 6 makes stubs only return not-implemented.
  - **Round 3:** the reviewer listed boundaries, shape and design rules as "checked and fine", with **no boundary or package-shape finding**. It checked imports, `rules.go`, no interface where `fs.FS` is the seam, no `init()`, `ctx` first, sentinels plus a typed error, `doc.go`, and the component reasoning. It confirmed with `go list -deps -tags nomodels` that the tagged wiring keeps YAML out of the API image.
    - Its three remaining blocking findings were about T057's criteria, not the package's shape:
      - a golden file proposed for prose (§2.5);
      - an undecided zero-value `Skill`;
      - limits re-checked at read time.
    - These are for T057's implementer and are recorded in the scratch output, not here.
- Not done: a dry run on `pkg/llm` itself, since it exists. The T057 rounds stand in for it.
