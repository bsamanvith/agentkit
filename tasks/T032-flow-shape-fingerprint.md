---
id: T032
title: "flow: detect a changed step tree on resume"
epic: E05
status: done
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

- [x] Same tree → same fingerprint, across processes (no map-order or pointer dependence). (`TestFingerprintIsStable`: pinned value in `testdata/shape_fingerprint.txt` plus 100 rebuilds with other closures and a 12-key route map; `TestFingerprintIgnoresWhatDoesNotChangeTheShape`)
- [x] Adding, removing, renaming or reordering a step, or changing a route key or a loop maximum, changes it. (`TestFingerprintChangesWithTheShape`, 28 trees that must all differ; `TestFingerprintMatchesStructureSimulation`, `FuzzFingerprintMatchesStructure`)
- [x] `Resume` against a changed tree fails with `ErrShapeChanged`; with the opt-in it proceeds. (`TestResumeOfAChangedTreeFailsWithErrShapeChanged`, `TestResumeWithAllowShapeChangeProceedsAndWarns`)
- [x] An old checkpoint without a fingerprint still resumes. (`TestResumeOfACheckpointWithoutAShapeStillRuns`, from the real old-format file `testdata/checkpoint_suspended_no_shape.json`)
- [x] `flowtest.RunCheckpointer` still passes; mutation list recorded. (`TestMemoryCheckpointer`, whose sample now carries a `Shape`; mutations below)

## Notes

### API (package `flow`)

- `Fingerprint(step Step) (string, error)`: `"v1:" + hex(SHA-256)` of a canonical text of the tree. Runs `Validate` first (`ErrInvalidFlow`).
- `Checkpoint.Shape string` (`json:"shape,omitempty"`). `CheckpointVersion` stays 1: the field is optional, and bumping the version would make `Resume` reject every old checkpoint, which the task forbids.
- `ErrShapeChanged`, `*ShapeChangedError{RunID, Stored, Current}` (unwraps to `ErrShapeChanged`), `ErrNoShape`, `WithAllowShapeChange()`, and a new `EventWarning` event kind.

### Design decisions

- **What is covered:** the name and kind of every step (`seq`, `par`, `route`, `loop`, `approve`, and `step` for any other leaf), nesting, the order of a `Sequence`'s children and a `Parallel`'s branches, route keys (sorted, each with its child) and the loop maximum. Every name and route key is written as its length in bytes, a colon and its bytes (`3:abc`), so no choice of names (`a"),step("b`, `a),step(b`, a route key `x=step(1:a),y`) can make one tree write the text of another, and the text does not depend on the toolchain: `strconv.Quote`, used first, prints a rune as `\U0001fae9` or as itself depending on the Unicode tables of the Go release, so the same tree had two fingerprints on go1.26 and go1.27 (found in review; the reference tree now has such a name, and its pinned fingerprint is identical on both toolchains). `Named(n, s)` contributes `n` and the shape of `s`, because that is how paths are built. Nothing about Go types, pointers or closures is covered, so refactors and code changes do not invalidate checkpoints.
- **Custom leaf steps** (an agent step, say) are identified by name only: changing an agent's model or prompt does not change the fingerprint. That matches "never closures"; a step that needs more can include it in its name.
- **`Run`** stores the fingerprint in the first checkpoint, and every later save carries it (`TestEveryCheckpointCarriesTheFingerprint`).
- **`Resume`** checks right after the version check, before the state is restored and before any step, event or save. On a mismatch without the opt-in it returns `*ShapeChangedError` and changes nothing, not even the stored checkpoint (`TestResumeOfAChangedTreeFailsWithErrShapeChanged`). Applies to a run in any status, a completed one included.
- **Warnings** are `EventWarning` events on the root path, delivered to the observer before anything else: `Err` matches `ErrNoShape` for an old checkpoint, and `ErrShapeChanged` (a `*ShapeChangedError`) when the opt-in was used. In both cases `Resume` adopts the current fingerprint, written by the run's next save. If the process is cancelled before that save, the stored checkpoint is unchanged and the next `Resume` warns again.
- **`WithAllowShapeChange` is only sound** if every recorded path still means the same step (documented on the option): records are matched by path, so a renamed step that had completed is simply not skipped, and a moved one is skipped wherever its old path now leads.
- T023 Notes said a changed tree "is not detected"; it is now.
- `flowtest` change: the shared sample checkpoint has a `Shape` and `equal` compares it, so every `Checkpointer` (SQLite in T029) must round-trip it. `equal` compares `Shape` before it normalises through `Clone`, so a broken `Clone` cannot hide a lost shape from the suite.

### Golden files

`make golden` (run for `pkg/flow`) changed `testdata/checkpoint_suspended.json` by exactly one line, `"shape"` (now `v1:f130…`, after the encoding change below). The file as it was is kept as `testdata/checkpoint_suspended_no_shape.json`, an old-format checkpoint that the legacy-resume test loads. `testdata/shape_fingerprint.txt` pins the fingerprint of a reference tree that uses every kind of step: changing the encoding changes it, and that needs a new `shapeVersion`.

### Mutation check

Each bug was planted alone (the code compiled), `go test -race ./pkg/flow/...` was run, and the code was restored afterwards. A first try of three mutants did not compile (not counted) or was incomplete; they were redone. One more made-up mutant (wrapping the error in `ErrCheckpoint` as well) was dropped because it still matches `ErrShapeChanged`, so it is not a bug.

| Planted bug | Failing tests |
|---|---|
| Fingerprint ignores the order of a `Sequence`'s children | `TestFingerprintChangesWithTheShape`, `TestFingerprintIsStable`, `TestFingerprintMatchesStructureSimulation`, `TestCheckpointJSONGolden` |
| Fingerprint ignores the order of a `Parallel`'s branches | same four |
| Fingerprint ignores route keys | `TestFingerprintChangesWithTheShape`, `TestFingerprintIsStable`, `TestFingerprintMatchesStructureSimulation` |
| Fingerprint depends on map order (route keys not sorted) | `TestFingerprintIsStable`, `TestFingerprintIgnoresWhatDoesNotChangeTheShape` |
| Fingerprint ignores the loop maximum | `TestFingerprintChangesWithTheShape`, `TestFingerprintIsStable`, `TestFingerprintMatchesStructureSimulation` |
| Fingerprint ignores the combinator kind (`Parallel` written as `Sequence`) | `TestFingerprintChangesWithTheShape`, `TestFingerprintIsStable`, `TestFingerprintMatchesStructureSimulation`, `TestCheckpointJSONGolden` |
| Fingerprint treats `Approve` as a plain step | same four |
| Fingerprint uses the wrapped step's name under `Named` | `TestFingerprintIgnoresWhatDoesNotChangeTheShape`, `TestFingerprintIsStable`, `TestFingerprintMatchesStructureSimulation` |
| Names written without a length (raw), or raw inside quotes | `TestFingerprintChangesWithTheShape` (with the pinned-value tests skipped; the simulation does not catch it) |
| Only route keys written raw | `TestFingerprintChangesWithTheShape` (same) |
| Rejecting an approval drops `Shape` from the saved checkpoint | `TestApproveRejectionEndsRun` (through the check every `run`/`resume` helper call now makes) |
| The check is skipped | `TestResumeOfAChangedTreeFailsWithErrShapeChanged` (and two more) |
| The opt-in is ignored (a changed tree always fails) | `TestResumeWithAllowShapeChangeProceedsAndWarns`, `TestEveryCheckpointCarriesTheFingerprint` |
| The opt-in is all that is checked (a changed tree always proceeds) | `TestResumeOfAChangedTreeFailsWithErrShapeChanged` |
| An old checkpoint is rejected | `TestResumeOfACheckpointWithoutAShapeStillRuns`, `TestEveryCheckpointCarriesTheFingerprint` |
| No warning event is emitted | `TestResumeOfACheckpointWithoutAShapeStillRuns`, `TestResumeWithAllowShapeChangeProceedsAndWarns` |
| `Run` does not store the fingerprint | `TestEveryCheckpointCarriesTheFingerprint`, `TestResumeOfAChangedTreeFailsWithErrShapeChanged` and 4 more |
| `Resume` does not adopt the current fingerprint | `TestEveryCheckpointCarriesTheFingerprint`, `TestResumeOfACheckpointWithoutAShapeStillRuns`, `TestResumeWithAllowShapeChangeProceedsAndWarns` |
| `Memory.Load` drops the fingerprint (a lossy store) | `TestMemoryCheckpointer/SaveThenLoadRoundTrips` and 4 more subtests |

Found while checking: a `Clone` that drops `Shape` was first *not* caught by the suite, because `equal` normalised both sides through `Clone`. `equal` now compares `Shape` first; the lossy-store mutant above is the one that proves it.

### Extra tests beyond the criteria

`TestFingerprintOfAnInvalidTreeIsAnError`, `TestResumeWithTheSameTreeWarnsAboutNothing` (a rebuilt tree is the same tree), `TestEveryCheckpointCarriesTheFingerprint` (an invariant over every save of a failing run, a legacy adoption and an opt-in) and, for the whole package, the `run` and `resume` test helpers, which fail any test whose checkpointer is asked to save a checkpoint without the fingerprint of the running tree (§2.6: the invariant is checked after every run, not in one test), `TestFingerprintMatchesStructureSimulation` (3,000 seeded trees, about 1,800 distinct, many built by more than one seed: fingerprint and an independent JSON description of the structure identify each other; a failure names its seed) and `FuzzFingerprintMatchesStructure` (the same property for two seeds).

### Review fixes (an independent review after the first push)

- **The encoding used `strconv.Quote`, which depends on the toolchain's Unicode tables** (see Design decisions). Replaced by length-prefixed bytes before anything stable stored a fingerprint; `shapeVersion` stays `v1` because nothing was released. A suspended `agentkit flow demo` file written by the first push holds the old value and will answer `ErrShapeChanged`: delete it or use the opt-in.
- The quoting guarantee was protected only by the pinned golden value, because the forging names in the tests (`a"),step("b`) contain quotes and forge nothing against an unquoted encoding. Added forging names, route keys and root names that do collide under the raw encodings (verified), and the generator uses them too.
- The "every checkpoint carries the fingerprint" invariant was checked in one test only; planting a bug that dropped `Shape` when an approval is rejected passed the suite. It is now checked by the shared test helpers.
- `Event.Err` doc now includes `EventWarning`.

### Open points (not changed)

- `Resume` also checks a completed run, so a deploy that edits a tree makes "read the final state of a finished run" fail with `ErrShapeChanged` unless the opt-in is given. No step can run for a completed run, so the check protects nothing there; kept because the spec says `Resume` fails on a changed tree, and a runner can read the final state from the checkpoint instead. Decide in T030.
- Outside this task's files: `tasks/T023-flow.md` and `tasks/T024-run-cli.md` still say a changed tree "is not detected".

### For T028, T029 and T030

- **Persist `Checkpoint.Shape`** (SQLite: a `shape TEXT NOT NULL DEFAULT ''` column, or inside the one JSON document). `flowtest.RunCheckpointer` fails a store that loses it. An empty value means "written before fingerprints existed" and must stay empty, not become a default.
- **`ErrShapeChanged` comes back before anything runs and writes nothing.** The runner should report it as a distinct, non-retryable failure of the run (not as a generic error it retries), release the lease, and leave the run resumable. Offer `WithAllowShapeChange` only as an explicit operator decision (a flag on the resume command), never as a default.
- A deployment that edits a flow while runs are suspended at an approval will hit this on every such run. Listing runs whose stored `Shape` differs from `flow.Fingerprint(currentTree)` is possible with what exists now, if the runner wants to show it.
- Observers see a new `EventWarning` kind; code that switches on `flow.EventKind` should tolerate unknown kinds.
- Custom leaf steps count by name only (see Design decisions), so `agentstep` fingerprints do not change when an agent's model or prompt does.
