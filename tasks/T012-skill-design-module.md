---
id: T012
title: design-module skill
epic: E02
status: todo
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

- [ ] Dry run on the next new package (`llm`, M3): the output passes a `review` (T015) with no boundary or shape findings. Recorded in Notes.
- [ ] The skill links §1 sections rather than restating rules (checked in review).

## Notes

-
