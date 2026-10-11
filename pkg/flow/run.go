package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
)

// Decision is a human's answer to an [Approve] step.
type Decision struct {
	Approved bool
	// Reason is kept with a rejection and reported in [RejectedError].
	Reason string
}

// Option configures [Run] and [Resume].
type Option func(*options)

type options struct {
	cp        Checkpointer
	observer  func(Event)
	initial   *State
	decisions map[string]Decision
}

// WithCheckpointer sets where checkpoints are saved. It is required.
func WithCheckpointer(cp Checkpointer) Option {
	return func(o *options) { o.cp = cp }
}

// WithObserver sets a function that receives every [Event]. Calls are
// serialized, so fn needs no locking, and it must not call back into the run.
// Events of one step arrive in order; events of parallel branches interleave.
func WithObserver(fn func(Event)) Option {
	return func(o *options) { o.observer = fn }
}

// WithState sets the state a new run starts with ([Run] only; a resumed run
// starts from its checkpoint). The state is copied.
func WithState(st *State) Option {
	return func(o *options) { o.initial = st }
}

// WithDecision answers the [Approve] step at path. Decisions for approvals the
// run does not reach, or has already passed, are ignored.
func WithDecision(path string, d Decision) Option {
	return func(o *options) {
		if o.decisions == nil {
			o.decisions = map[string]Decision{}
		}
		o.decisions[path] = d
	}
}

// Run starts a new run of step, saving a checkpoint after every completed
// step. It returns the final state. On error it returns a nil state; a
// run that is waiting for an approval returns an error matching [ErrSuspended]
// ([SuspendedError] lists the paths), and [Resume] continues it.
//
// A run id has one writer at a time: do not Run or Resume the same id
// concurrently. Run fails with [ErrRunExists] if the id already has a
// checkpoint.
func Run(ctx context.Context, id string, step Step, opts ...Option) (*State, error) {
	o, err := prepare(id, step, opts)
	if err != nil {
		return nil, err
	}
	if _, err := o.cp.Load(ctx, id); err == nil {
		return nil, fmt.Errorf("%w: %q", ErrRunExists, id)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w: load %q: %w", ErrCheckpoint, id, err)
	}
	st := NewState()
	if o.initial != nil {
		st = o.initial.Clone()
	}
	initial, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	ckpt := Checkpoint{
		Version: CheckpointVersion,
		RunID:   id,
		Status:  StatusRunning,
		Initial: initial,
		Steps:   map[string]json.RawMessage{},
	}
	if err := o.cp.Save(ctx, id, ckpt.Clone()); err != nil {
		return nil, fmt.Errorf("%w: save %q: %w", ErrCheckpoint, id, err)
	}
	return execute(ctx, o, ckpt, step, st)
}

// Resume continues a run from its checkpoint: steps that completed are
// skipped and the state is restored to what it was after them. step must be
// the step tree that produced the checkpoint. Resuming a completed run returns
// its final state; resuming a rejected run returns [ErrRejected] again.
// Resume fails with [ErrNotFound] if the run does not exist.
func Resume(ctx context.Context, id string, step Step, opts ...Option) (*State, error) {
	o, err := prepare(id, step, opts)
	if err != nil {
		return nil, err
	}
	ckpt, err := o.cp.Load(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: load %q: %w", ErrCheckpoint, id, err)
	}
	if ckpt.Version != CheckpointVersion {
		return nil, fmt.Errorf("%w: run %q has checkpoint version %d, want %d", ErrCheckpoint, id, ckpt.Version, CheckpointVersion)
	}
	st := NewState()
	if err := json.Unmarshal(ckpt.Initial, st); err != nil {
		return nil, fmt.Errorf("%w: run %q initial state: %w", ErrCheckpoint, id, err)
	}
	ckpt = ckpt.Clone()
	if ckpt.Steps == nil {
		ckpt.Steps = map[string]json.RawMessage{}
	}
	ckpt.Status = StatusRunning
	ckpt.Pending = nil
	return execute(ctx, o, ckpt, step, st)
}

func prepare(id string, step Step, opts []Option) (*options, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.cp == nil {
		return nil, ErrNoCheckpointer
	}
	if id == "" {
		return nil, fmt.Errorf("%w: empty run id", ErrInvalidFlow)
	}
	if err := Validate(step); err != nil {
		return nil, err
	}
	return &o, nil
}

func execute(ctx context.Context, o *options, ckpt Checkpoint, step Step, st *State) (*State, error) {
	rt := &runtime{
		id:        ckpt.RunID,
		cp:        o.cp,
		observer:  o.observer,
		decisions: maps.Clone(o.decisions),
		ckpt:      ckpt,
	}
	err := rt.step(ctx, nameOf(step), step, st)

	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		// Cancelled: the last saved checkpoint already says how far we got.
		return nil, err
	}
	var susp *SuspendedError
	var status Status
	switch {
	case err == nil:
		status = StatusCompleted
	case errors.Is(err, ErrRejected):
		status = StatusRejected
	case errors.As(err, &susp):
		status = StatusSuspended
	default:
		status = StatusFailed
	}
	var pending []string
	if susp != nil {
		pending = susp.Paths
	}
	if serr := rt.finish(ctx, status, pending); serr != nil {
		err = errors.Join(err, serr)
	}
	if err != nil {
		return nil, err
	}
	return st, nil
}

// runtime is the state of one Run or Resume call, shared by all its steps
// through the context.
type runtime struct {
	id        string
	cp        Checkpointer // nil when a step is run outside Run
	observer  func(Event)
	decisions map[string]Decision

	mu   sync.Mutex // guards ckpt and serializes Save calls
	ckpt Checkpoint // copy-on-write: replaced, never edited in place

	emitMu sync.Mutex
}

type scopeKey struct{}

// scope is where a step is running: the run's runtime and the step's path.
type scope struct {
	rt   *runtime
	path string
}

// scopeOf returns the scope of the step whose Run is executing. A combinator
// run directly, outside Run, gets a detached scope with no checkpoints.
func scopeOf(ctx context.Context, name string) *scope {
	if sc, ok := ctx.Value(scopeKey{}).(*scope); ok {
		return sc
	}
	return &scope{rt: &runtime{ckpt: Checkpoint{Steps: map[string]json.RawMessage{}}}, path: name}
}

// StepPath returns the run id and the path of the step whose Run is executing
// with ctx, for a step that keeps progress of its own between checkpoints (an
// agent step saving its transcript turn by turn) and needs a key that is unique
// within the run and the same when the step runs again on resume. ok is false
// when the step is not running under [Run] or [Resume].
func StepPath(ctx context.Context) (runID, path string, ok bool) {
	sc, found := ctx.Value(scopeKey{}).(*scope)
	if !found || sc.rt.cp == nil {
		return "", "", false
	}
	return sc.rt.id, sc.path, true
}

// step runs one step at path: skipped if it completed before, otherwise run,
// checkpointed and reported. Every combinator runs its children through it.
func (rt *runtime) step(ctx context.Context, path string, step Step, st *State) error {
	if raw, ok := rt.record(path); ok {
		if err := json.Unmarshal(raw, st); err != nil {
			return fmt.Errorf("%w: record %q: %w", ErrCheckpoint, path, err)
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rt.emit(Event{Kind: EventStarted, Path: path})
	err := runSafely(context.WithValue(ctx, scopeKey{}, &scope{rt: rt, path: path}), path, step, st)
	if err == nil {
		// The checkpoint is taken here, after Run returned: the step has
		// finished changing the state.
		err = rt.complete(ctx, path, st)
	}
	if err != nil {
		err = annotate(path, err)
		kind := EventFailed
		if errors.Is(err, ErrSuspended) {
			kind = EventSuspended
		}
		rt.emit(Event{Kind: kind, Path: path, Err: err})
		return err
	}
	rt.emit(Event{Kind: EventFinished, Path: path})
	return nil
}

// annotate attaches the path of the failing step to err, once: errors that
// already carry a path are returned as they are.
func annotate(path string, err error) error {
	var se *StepError
	var susp *SuspendedError
	var rej *RejectedError
	var pan *PanicError
	var con *ConflictError
	if errors.As(err, &se) || errors.As(err, &susp) || errors.As(err, &rej) ||
		errors.As(err, &pan) || errors.As(err, &con) {
		return err
	}
	return &StepError{Path: path, Err: err}
}

func runSafely(ctx context.Context, path string, step Step, st *State) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &PanicError{Path: path, Value: r, Stack: debug.Stack()}
		}
	}()
	return step.Run(ctx, st)
}

func (rt *runtime) record(path string) (json.RawMessage, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	raw, ok := rt.ckpt.Steps[path]
	return raw, ok
}

// complete records that the step at path finished with state st and saves the
// checkpoint. The records of the step's children are dropped: the step's own
// record supersedes them.
func (rt *runtime) complete(ctx context.Context, path string, st *State) error {
	if rt.cp == nil {
		return nil
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	next := rt.ckpt.Clone()
	for p := range next.Steps {
		if strings.HasPrefix(p, path+"/") || strings.HasPrefix(p, path+"[") {
			delete(next.Steps, p)
		}
	}
	next.Steps[path] = raw
	if err := rt.cp.Save(ctx, rt.id, next); err != nil {
		return fmt.Errorf("%w: save %q after %s: %w", ErrCheckpoint, rt.id, path, err)
	}
	rt.ckpt = next
	return nil
}

// finish saves the run's final status.
func (rt *runtime) finish(ctx context.Context, status Status, pending []string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	next := rt.ckpt.Clone()
	next.Status = status
	next.Pending = slices.Clone(pending)
	if err := rt.cp.Save(ctx, rt.id, next); err != nil {
		return fmt.Errorf("%w: save %q status %s: %w", ErrCheckpoint, rt.id, status, err)
	}
	rt.ckpt = next
	return nil
}

func (rt *runtime) rejection(path string) (string, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	reason, ok := rt.ckpt.Rejected[path]
	return reason, ok
}

func (rt *runtime) reject(path, reason string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	next := rt.ckpt.Clone()
	if next.Rejected == nil {
		next.Rejected = map[string]string{}
	}
	next.Rejected[path] = reason
	rt.ckpt = next
}

func (rt *runtime) emit(e Event) {
	if rt.observer == nil {
		return
	}
	rt.emitMu.Lock()
	defer rt.emitMu.Unlock()
	rt.observer(e)
}
