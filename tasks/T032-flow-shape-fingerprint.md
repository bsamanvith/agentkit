---
id: T032
title: "flow: detect a changed step tree on resume"
epic: E05
status: todo
depends_on: [T023]
---

# T032: Flow shape fingerprint

## Context

T023 handoff, contract 2: `Resume` assumes the step tree is the one that produced the checkpoint, and does not check. With in-memory checkpoints that holds; with the SQLite store, checkpoints outlive deployments, so a new version of the code can resume an old checkpoint against a different tree and silently skip or misplace steps.

## Scope

- A deterministic fingerprint of the step tree (names, combinator kinds, nesting, route keys, loop maxima): not of closures.
- Stored in `flow.Checkpoint`; `Resume` compares and fails with `ErrShapeChanged` (both fingerprints in the error) unless the caller opts in with `flow.WithAllowShapeChange()`.
- Checkpoints written before this change (no fingerprint) resume as before, with a warning event.
- Update the pinned `pkg/flow/testdata/checkpoint_*.json` golden files.

## Acceptance criteria

- [ ] Same tree → same fingerprint, across processes (no map-order or pointer dependence).
- [ ] Adding, removing, renaming or reordering a step, or changing a route key or a loop maximum, changes it.
- [ ] `Resume` against a changed tree fails with `ErrShapeChanged`; with the opt-in it proceeds.
- [ ] An old checkpoint without a fingerprint still resumes.
- [ ] `flowtest.RunCheckpointer` still passes; mutation list recorded.
