// Package flow runs multi-step work as plain Go plus a few combinators, and
// makes every step boundary a checkpoint. A run that crashes can be resumed
// without repeating finished steps, and a run can pause for a human decision.
//
// # Steps and state
//
// A [Step] turns a [State] into new state. [Func] adapts a function, and any
// type with a Run method is a Step (an agent run becomes one in the harness
// integration). State holds named, JSON-serializable values read with [Get] and
// written with [Set], so it can be saved in a checkpoint.
//
// # Combinators
//
//   - [Sequence] runs steps in order and stops at the first error.
//   - [Parallel] runs branches concurrently, each on its own copy of the state,
//     and merges the results by key. Two branches changing the same key is an
//     error ([ErrConflict]); one branch failing cancels the others.
//   - [Loop] repeats a step until a condition holds, and fails with
//     [ErrMaxIterations] if it never does.
//   - [Route] picks one of several steps by a key computed from the state.
//   - [Approve] suspends the run until [Resume] is given a decision.
//
// [Parallel] is a barrier: it returns only when every branch has finished. It
// is a fork-and-join inside a flow, not a way to start background work.
//
// # Names and paths
//
// Every step has a name, and every run of a step has a path built only from
// names and indices, never from timing or map order:
//
//	review/search               a step in a Sequence named "review"
//	review/gather[1]/search     branch 1 of a Parallel named "gather"
//	review/refine[2]/draft      iteration 2 of a Loop named "refine"
//	review/pick[approve]/send   the child chosen by Route "pick" for key "approve"
//
// Events and checkpoints use these paths. [Validate] (called by [Run] and
// [Resume]) rejects missing, duplicate or malformed names.
//
// # Checkpoints and resume
//
// [Run] starts a run and [Resume] continues it. After every step completes the
// runner saves a [Checkpoint] through a [Checkpointer]: the state as it was after
// that step, keyed by path. The checkpoint is written only after the step has
// finished changing the state, so a step that fails halfway leaves nothing
// behind. On resume, a step whose path has a record is skipped and its state
// restored; everything else runs again. For this to be sound, the step tree
// passed to [Resume] must be the one that produced the checkpoint, and loop
// conditions and route keys must be pure functions of the state.
//
// # Changed step trees
//
// A checkpoint can outlive the code that wrote it. [Run] therefore saves the
// [Fingerprint] of the step tree (names, combinator kinds, nesting, route keys
// and loop maxima; never closures) in the checkpoint, and [Resume] refuses a
// different tree with [ErrShapeChanged] instead of skipping or misplacing
// steps. [WithAllowShapeChange] resumes anyway, for a change known to be safe.
// A checkpoint written before fingerprints existed resumes as before, with an
// [EventWarning].
//
// [NewMemory] is an in-process Checkpointer. Conformance tests for any
// Checkpointer live in package flowtest.
//
// # Errors
//
// Failures are classified with sentinel values checked with [errors.Is]:
// [ErrSuspended] (waiting for an approval), [ErrRejected], [ErrConflict],
// [ErrMaxIterations], [ErrUnknownRoute], [ErrInvalidFlow], and so on. A
// cancelled context is reported as the context's own error.
package flow
