package flow

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by this package, checked with [errors.Is].
var (
	// ErrNotFound means a state key or a run does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalidValue means a state key or value cannot be stored: an empty
	// key, or a value that does not marshal to JSON.
	ErrInvalidValue = errors.New("invalid state value")
	// ErrInvalidState means bytes are not a JSON object of values and cannot be
	// decoded as a [State].
	ErrInvalidState = errors.New("invalid state")
	// ErrInvalidFlow means the step tree is malformed: a missing, duplicate or
	// illegal name, or a combinator built with bad arguments.
	ErrInvalidFlow = errors.New("invalid flow")
	// ErrConflict means two [Parallel] branches changed the same state key.
	ErrConflict = errors.New("parallel branches changed the same key")
	// ErrMaxIterations means a [Loop] reached its maximum without its
	// condition holding.
	ErrMaxIterations = errors.New("loop reached its maximum iterations")
	// ErrUnknownRoute means a [Route] computed a key that has no step.
	ErrUnknownRoute = errors.New("unknown route key")
	// ErrSuspended means the run is waiting for an approval. It is not a
	// failure: call [Resume] with a [WithDecision] to continue.
	ErrSuspended = errors.New("run suspended")
	// ErrRejected means an approval was rejected and the run has ended.
	ErrRejected = errors.New("approval rejected")
	// ErrRunExists means [Run] found a checkpoint for the run id. Use [Resume].
	ErrRunExists = errors.New("run already exists")
	// ErrNoCheckpointer means [Run] or [Resume] was called without
	// [WithCheckpointer].
	ErrNoCheckpointer = errors.New("no checkpointer")
	// ErrShapeChanged means [Resume] was given a step tree whose [Fingerprint]
	// differs from the one stored in the run's checkpoint. Resuming would skip
	// or misplace steps. [ShapeChangedError] carries both fingerprints;
	// [WithAllowShapeChange] resumes anyway.
	ErrShapeChanged = errors.New("step tree changed since the run was checkpointed")
	// ErrNoShape is the cause of the warning [Event] that [Resume] emits for a
	// checkpoint written before fingerprints existed. The run resumes, and its
	// next checkpoint carries the fingerprint.
	ErrNoShape = errors.New("checkpoint has no step tree fingerprint")
	// ErrCheckpoint means a checkpoint could not be saved or loaded, or the
	// stored one is unusable. The cause is wrapped.
	ErrCheckpoint = errors.New("checkpoint failed")
)

// StepError reports which step failed. It wraps the step's own error.
type StepError struct {
	Path string
	Err  error
}

func (e *StepError) Error() string { return e.Path + ": " + e.Err.Error() }
func (e *StepError) Unwrap() error { return e.Err }

// ConflictError lists the state keys that more than one [Parallel] branch
// changed. It matches [ErrConflict].
type ConflictError struct {
	// Path is the Parallel step.
	Path string
	// Keys are the conflicting keys, sorted.
	Keys []string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s: %v: %s", e.Path, ErrConflict, strings.Join(e.Keys, ", "))
}
func (e *ConflictError) Unwrap() error { return ErrConflict }

// SuspendedError lists the approvals a run is waiting for. It matches
// [ErrSuspended].
type SuspendedError struct {
	// Paths are the pending [Approve] steps, sorted.
	Paths []string
}

func (e *SuspendedError) Error() string {
	return fmt.Sprintf("%v: waiting for %s", ErrSuspended, strings.Join(e.Paths, ", "))
}
func (e *SuspendedError) Unwrap() error { return ErrSuspended }

// RejectedError names the approval that was rejected. It matches [ErrRejected].
type RejectedError struct {
	Path   string
	Reason string
}

func (e *RejectedError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("%s: %v", e.Path, ErrRejected)
	}
	return fmt.Sprintf("%s: %v: %s", e.Path, ErrRejected, e.Reason)
}
func (e *RejectedError) Unwrap() error { return ErrRejected }

// PanicError reports a step that panicked. The runner recovers the panic so
// that a crashing branch cannot take down the process.
type PanicError struct {
	Path  string
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("%s: panic: %v", e.Path, e.Value) }
