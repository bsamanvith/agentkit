package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// CanceledText is the text of the marker reply [Runner.Cancel] appends when a
// canceled run's conversation ends on a user message.
const CanceledText = "(The run was canceled before it answered.)"

// errStopped is the cause of an execution whose consumer stopped iterating.
var errStopped = errors.New("consumer stopped")

// leaseLost is the cause of an execution that could not renew its lease.
type leaseLost struct{ err error }

func (e leaseLost) Error() string { return "lease lost: " + e.err.Error() }
func (e leaseLost) Unwrap() error { return e.err }

// RunOption configures one chat run, started by [Runner.Run].
type RunOption func(*runConfig)

type runConfig struct {
	flush       int
	interactive bool
	err         error
}

// Interactive starts the run as an interactive chat run: one with a user on
// the other end ([store.Interactive], stored with the run, so every resume
// keeps it). Such a run stops with its user and is never recovered in the
// background:
//
//   - when its execution stops ([Runner.Interrupt], the consumer stops
//     reading, ctx ends, [Runner.Shutdown]) or its process dies, it is
//     interrupted ([store.Run.Interrupted]), and the recovery loop leaves it
//     alone; [Runner.Resume] continues it;
//   - a new message on its conversation ([Runner.Run]) cancels it, if
//     nobody is running it, and starts the new run;
//   - a retryable model failure is retried inline ([WithInteractiveRetry]).
func Interactive() RunOption {
	return func(c *runConfig) { c.interactive = true }
}

// FlushEvery sets how many turns the run keeps before it stores them in one
// write, in place of the runner's [WithFlushEvery], for this run and every
// resume of it. Below 1, Run gives [ErrInvalidConfig].
func FlushEvery(n int) RunOption {
	return func(c *runConfig) {
		c.flush = n
		if n < 1 {
			c.err = fmt.Errorf("%w: flush every %d turns", ErrInvalidConfig, n)
		}
	}
}

// Run starts a chat run: msg, a user message, on the conversation, run by the
// conversation's agent. It creates the run and claims its lease before it
// returns; iterate the execution's events to run it. A conversation with an
// unfinished run gives an [*UnfinishedError], unless that run is interactive
// and nobody runs it (interrupted, or failed): Run then cancels it, as
// [Runner.Cancel] does (with the marker reply if it never answered), and
// starts the new run in its place. Of two messages racing to replace it, one
// starts a run and the other gets the UnfinishedError.
func (r *Runner) Run(ctx context.Context, conversationID string, msg llm.Message, opts ...RunOption) (*Execution, error) {
	var c runConfig
	for _, opt := range opts {
		opt(&c)
	}
	if c.err != nil {
		return nil, c.err
	}
	if msg.Role != llm.RoleUser {
		return nil, fmt.Errorf("%w: role %q, want user", ErrInvalidInput, msg.Role)
	}
	if err := (harness.Transcript{msg}).Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	tenant, err := tenancy.Require(ctx)
	if err != nil {
		return nil, err
	}
	agent, err := r.agents(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("agent of conversation %q: %w", conversationID, err)
	}
	var create []store.CreateOption
	if c.interactive {
		create = append(create, store.Interactive())
	}
	x, err := r.start(ctx, tenant, conversationID, true, create...)
	if err != nil {
		return nil, err
	}
	msg = msg.Clone()
	return x.execution(ctx, func(ctx context.Context, emit func(Event) bool) outcome {
		return x.chat(ctx, agent, &msg, c.flush, emit)
	}), nil
}

// RunFlow starts a flow run on the conversation with the step tree of the
// [FlowFactory] and the given initial state (nil for an empty one). It
// creates the run, claims its lease and saves its first checkpoint before it
// returns. A conversation with an unfinished run gives an [*UnfinishedError].
func (r *Runner) RunFlow(ctx context.Context, conversationID string, input *flow.State) (*Execution, error) {
	if r.flows == nil {
		return nil, ErrNoFlows
	}
	tenant, err := tenancy.Require(ctx)
	if err != nil {
		return nil, err
	}
	step, err := r.flows(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("flow of conversation %q: %w", conversationID, err)
	}
	shape, err := flow.Fingerprint(step)
	if err != nil {
		return nil, err
	}
	if input == nil {
		input = flow.NewState()
	}
	initial, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	x, err := r.start(ctx, tenant, conversationID, false)
	if err != nil {
		return nil, err
	}
	ckpt := flow.Checkpoint{
		Version: flow.CheckpointVersion, RunID: x.run.ID, Shape: shape,
		Status: flow.StatusRunning, Initial: initial, Steps: map[string]json.RawMessage{},
	}
	if err := r.store.Save(ctx, x.run.ID, ckpt); err != nil {
		// Without a checkpoint the run cannot start: cancel it so that the
		// conversation is free again.
		wctx := context.WithoutCancel(ctx)
		_ = x.transition(wctx, store.StatusCanceled, store.Details{Error: "first checkpoint not saved: " + err.Error()})
		x.release(wctx)
		return nil, fmt.Errorf("run %q: save first checkpoint: %w", x.run.ID, err)
	}
	return x.execution(ctx, func(ctx context.Context, emit func(Event) bool) outcome {
		return x.flow(ctx, step, nil, emit)
	}), nil
}

// start creates a run on the conversation and claims it. For a chat run
// (chat), an interactive unfinished run that nobody runs is canceled first.
func (r *Runner) start(ctx context.Context, tenant tenancy.Tenant, conv string, chat bool, opts ...store.CreateOption) (*exec, error) {
	if r.isClosed() {
		return nil, ErrShutdown
	}
	for replaced := false; ; replaced = true {
		run, err := r.store.Create(ctx, r.newID(), conv, opts...)
		if errors.Is(err, store.ErrUnfinished) {
			u, uerr := r.store.Unfinished(ctx, conv)
			if uerr != nil {
				return nil, fmt.Errorf("%w: %w", ErrRunUnfinished, uerr)
			}
			if chat && !replaced && abandoned(u, r.clk.Now()) {
				// Canceled by this call (if only its marker failed, the new
				// run appends it) or by another one racing it: either way
				// the conversation may take a run now. ErrLeased means
				// someone continued it.
				c, cerr := r.cancelRun(ctx, tenant, u.ID)
				if cerr == nil || errors.Is(cerr, store.ErrFinal) || c.Status == store.StatusCanceled {
					continue
				}
				if !errors.Is(cerr, store.ErrLeased) {
					return nil, fmt.Errorf("%w: run %q could not be canceled: %w", ErrRunUnfinished, u.ID, cerr)
				}
			}
			return nil, &UnfinishedError{ConversationID: conv, RunID: u.ID}
		}
		if err != nil {
			return nil, fmt.Errorf("create run: %w", err)
		}
		x := &exec{r: r, tenant: tenant, run: run, owner: r.nextOwner()}
		if x.run.Lease, err = r.store.Claim(ctx, run.ID, x.owner, r.ttl); err != nil {
			if errors.Is(err, store.ErrFinal) {
				// Canceled before it was claimed, by another new message
				// that took its place.
				if u, uerr := r.store.Unfinished(ctx, conv); uerr == nil {
					return nil, &UnfinishedError{ConversationID: conv, RunID: u.ID}
				}
			}
			return nil, fmt.Errorf("run %q: claim: %w", run.ID, err)
		}
		return x, nil
	}
}

// abandoned reports whether a new message may cancel run: an interactive run
// that nobody runs at now, interrupted or failed.
func abandoned(run store.Run, now time.Time) bool {
	return run.Interactive && !held(run.Lease, now) &&
		(run.Status == store.StatusRunning || run.Status == store.StatusFailed)
}

// held reports whether l is held at now.
func held(l store.Lease, now time.Time) bool {
	return l.Owner != "" && now.Before(l.Expires)
}

// ResumeOption configures [Runner.Resume].
type ResumeOption func(*resumeConfig)

type resumeConfig struct {
	allowShape bool
	decision   *decision
}

type decision struct {
	path string
	d    flow.Decision
}

// AllowShapeChange resumes a flow run whose step tree changed since its
// checkpoint ([flow.WithAllowShapeChange]). It is an operator's decision for
// one resume, never a default.
func AllowShapeChange() ResumeOption {
	return func(c *resumeConfig) { c.allowShape = true }
}

// Resume continues a run that is not final: an interrupted running run,
// a failed one (retryable or not: this is how an operator resumes one), from
// its stored transcript or checkpoint. It claims the run's lease, and moves a
// failed run back to running, before it returns. A suspended run waits for
// [Runner.Decide] ([ErrAwaitingDecision]).
//
// A completed run is not resumed: its execution yields one [EventDone] with
// the run, and for a flow run its final state read from the checkpoint. A
// rejected or canceled run gives an error matching [store.ErrFinal].
func (r *Runner) Resume(ctx context.Context, runID string, opts ...ResumeOption) (*Execution, error) {
	var c resumeConfig
	for _, opt := range opts {
		opt(&c)
	}
	return r.resume(ctx, runID, c)
}

// Decide answers the approval at path of a suspended flow run and resumes the
// run: an approval continues it, a rejection makes it rejected, which is
// final. A path the run is not waiting for is [ErrNotPending], and a final
// run, completed included, [store.ErrFinal].
//
// A step tree that changed since the run's checkpoint gives an error matching
// [flow.ErrShapeChanged], from Resume as from Decide, before anything is
// written or claimed; [AllowShapeChange] proceeds anyway.
func (r *Runner) Decide(ctx context.Context, runID, path string, d flow.Decision, opts ...ResumeOption) (*Execution, error) {
	c := resumeConfig{decision: &decision{path: path, d: d}}
	for _, opt := range opts {
		opt(&c)
	}
	return r.resume(ctx, runID, c)
}

func (r *Runner) resume(ctx context.Context, runID string, c resumeConfig) (*Execution, error) {
	tenant, err := tenancy.Require(ctx)
	if err != nil {
		return nil, err
	}
	if r.isClosed() {
		return nil, ErrShutdown
	}
	run, err := r.store.Get(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("run %q: %w", runID, err)
	}
	if run.Status == store.StatusCompleted && c.decision == nil {
		return r.completed(ctx, run)
	}
	if err := checkResumable(run, c); err != nil {
		return nil, err
	}
	ckpt, err := r.store.Load(ctx, runID)
	isFlow := err == nil
	if err != nil && !errors.Is(err, flow.ErrNotFound) {
		return nil, fmt.Errorf("run %q: load checkpoint: %w", runID, err)
	}
	var body func(x *exec) func(context.Context, func(Event) bool) outcome
	if isFlow {
		if r.flows == nil {
			return nil, ErrNoFlows
		}
		step, err := r.flows(ctx, run.ConversationID)
		if err != nil {
			return nil, fmt.Errorf("flow of conversation %q: %w", run.ConversationID, err)
		}
		// Checked before the claim, so that a changed tree writes nothing.
		// flow.Resume checks again, against the checkpoint it loads.
		if ckpt.Shape != "" && !c.allowShape {
			shape, err := flow.Fingerprint(step)
			if err != nil {
				return nil, fmt.Errorf("flow of conversation %q: %w", run.ConversationID, err)
			}
			if shape != ckpt.Shape {
				return nil, &flow.ShapeChangedError{RunID: runID, Stored: ckpt.Shape, Current: shape}
			}
		}
		body = func(x *exec) func(context.Context, func(Event) bool) outcome {
			return func(ctx context.Context, emit func(Event) bool) outcome { return x.flow(ctx, step, &c, emit) }
		}
	} else {
		agent, err := r.agents(ctx, run.ConversationID)
		if err != nil {
			return nil, fmt.Errorf("agent of conversation %q: %w", run.ConversationID, err)
		}
		body = func(x *exec) func(context.Context, func(Event) bool) outcome {
			return func(ctx context.Context, emit func(Event) bool) outcome { return x.chat(ctx, agent, nil, 0, emit) }
		}
	}

	x := &exec{r: r, tenant: tenant, owner: r.nextOwner()}
	if _, err := r.store.Claim(ctx, runID, x.owner, r.ttl); err != nil {
		return nil, fmt.Errorf("run %q: claim: %w", runID, err)
	}
	fail := func(err error) (*Execution, error) {
		x.run.ID = runID
		x.release(context.WithoutCancel(ctx))
		return nil, err
	}
	// The run may have moved between the read above and the claim.
	if x.run, err = r.store.Get(ctx, runID); err != nil {
		return fail(fmt.Errorf("run %q: %w", runID, err))
	}
	if err := checkResumable(x.run, c); err != nil {
		return fail(err)
	}
	if x.run.Status != store.StatusRunning {
		if err := x.transition(ctx, store.StatusRunning, store.Details{}); err != nil {
			return fail(fmt.Errorf("run %q: resume: %w", runID, err))
		}
	}
	return x.execution(ctx, body(x)), nil
}

func checkResumable(run store.Run, c resumeConfig) error {
	switch {
	case run.Status.Final():
		return fmt.Errorf("run %q is %s: %w", run.ID, run.Status, store.ErrFinal)
	case c.decision != nil && (run.Status != store.StatusSuspended || !slices.Contains(run.Pending, c.decision.path)):
		return fmt.Errorf("%w: run %q is %s, pending %v, not %q", ErrNotPending, run.ID, run.Status, run.Pending, c.decision.path)
	case c.decision == nil && run.Status == store.StatusSuspended:
		return fmt.Errorf("%w: run %q waits for %v", ErrAwaitingDecision, run.ID, run.Pending)
	}
	return nil
}

// completed returns the execution of a completed run: its record, and for a
// flow run the final state from its checkpoint. Nothing runs.
func (r *Runner) completed(ctx context.Context, run store.Run) (*Execution, error) {
	done := Event{Kind: EventDone, RunID: run.ID, Run: run}
	ckpt, err := r.store.Load(ctx, run.ID)
	switch {
	case err == nil:
		st, err := finalState(ckpt)
		if err != nil {
			return nil, fmt.Errorf("run %q: %w", run.ID, err)
		}
		done.State = st
	case !errors.Is(err, flow.ErrNotFound):
		return nil, fmt.Errorf("run %q: load checkpoint: %w", run.ID, err)
	}
	return &Execution{Run: run, events: func(yield func(Event, error) bool) { yield(done, nil) }}, nil
}

// finalState reads a completed flow's final state: the record of its root
// step, the only one a completed checkpoint keeps.
func finalState(c flow.Checkpoint) (*flow.State, error) {
	if c.Status != flow.StatusCompleted || len(c.Steps) != 1 {
		return nil, fmt.Errorf("%w: checkpoint is %s with %d step records, want completed with one", flow.ErrCheckpoint, c.Status, len(c.Steps))
	}
	st := flow.NewState()
	for _, raw := range c.Steps {
		if err := json.Unmarshal(raw, st); err != nil {
			return nil, fmt.Errorf("%w: %w", flow.ErrCheckpoint, err)
		}
	}
	return st, nil
}

// Cancel makes a run that is not final canceled. If an execution of this
// runner is running it, it is stopped first (its events end with
// [ErrCanceled]); a run another process holds gives [store.ErrLeased].
// If the run's conversation then ends on a user message, Cancel appends a
// marker reply ([CanceledText]) so the next run sends a valid conversation.
// It returns the run as stored.
func (r *Runner) Cancel(ctx context.Context, runID string) (store.Run, error) {
	tenant, err := tenancy.Require(ctx)
	if err != nil {
		return store.Run{}, err
	}
	if err := r.stopLive(ctx, tenant, runID, ErrCanceled); err != nil {
		return store.Run{}, err
	}
	return r.cancelRun(ctx, tenant, runID)
}

// Interrupt stops the run's execution in this process, which ends with
// [ErrInterrupted]: like a consumer that stops reading, it stores the turns
// it kept and releases its lease, and the run stays running. An interactive
// run is then interrupted ([store.Run.Interrupted]) until [Runner.Resume]
// continues it or a new message replaces it; a background run is left to the
// recovery loop. It is the stop button of a chat.
//
// A run nobody holds (interrupted already, failed, suspended or final) is
// left as it is. A run whose lease another process holds gives
// [store.ErrLeased]: the command must reach that process (as for Cancel).
// So does a run of this runner whose execution has not been iterated yet. It
// returns the run as stored.
func (r *Runner) Interrupt(ctx context.Context, runID string) (store.Run, error) {
	tenant, err := tenancy.Require(ctx)
	if err != nil {
		return store.Run{}, err
	}
	if err := r.stopLive(ctx, tenant, runID, ErrInterrupted); err != nil {
		return store.Run{}, err
	}
	run, err := r.store.Get(ctx, runID)
	if err != nil {
		return store.Run{}, fmt.Errorf("run %q: %w", runID, err)
	}
	if !run.Status.Final() && held(run.Lease, r.clk.Now()) {
		return run, fmt.Errorf("run %q is held by %q: %w", runID, run.Lease.Owner, store.ErrLeased)
	}
	return run, nil
}

// stopLive stops the execution of runID in this process, if there is one,
// with cause, and waits until it has stopped.
func (r *Runner) stopLive(ctx context.Context, tenant tenancy.Tenant, runID string, cause error) error {
	l := r.liveRun(tenant, runID)
	if l == nil {
		return nil
	}
	l.cancel(cause)
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// cancelRun makes a run nobody runs canceled, appending the marker reply if
// its conversation ends on a user message.
func (r *Runner) cancelRun(ctx context.Context, tenant tenancy.Tenant, runID string) (store.Run, error) {
	var err error
	x := &exec{r: r, tenant: tenant, owner: r.nextOwner()}
	if _, err := r.store.Claim(ctx, runID, x.owner, r.ttl); err != nil {
		return store.Run{}, fmt.Errorf("run %q: claim: %w", runID, err)
	}
	x.run.ID = runID
	defer x.release(context.WithoutCancel(ctx))
	if x.run, err = r.store.Get(ctx, runID); err != nil {
		return store.Run{}, fmt.Errorf("run %q: %w", runID, err)
	}
	// Transition first: a crash before the marker leaves a canceled run, and
	// the next run appends the marker.
	if err := x.transition(ctx, store.StatusCanceled, store.Details{Error: "canceled"}); err != nil {
		return store.Run{}, fmt.Errorf("run %q: cancel: %w", runID, err)
	}
	v, err := r.cache.load(ctx, r.store, x.run.ConversationID)
	if err != nil {
		return x.run, fmt.Errorf("run %q: canceled, but its conversation could not be read: %w", runID, err)
	}
	if v.endsOnUser() {
		if err := r.appendMarker(ctx, x.run.ConversationID, &v); err != nil {
			return x.run, fmt.Errorf("run %q: canceled, but the marker reply was not stored: %w", runID, err)
		}
	}
	return x.run, nil
}

// appendMarker closes a conversation that ends on the user message of a
// canceled run with the marker reply, labelled with that run.
func (r *Runner) appendMarker(ctx context.Context, conv string, v *view) error {
	_, err := r.append(ctx, conv, v, store.Entry{
		Kind:     store.KindTurn,
		Messages: []llm.Message{llm.Assistant(llm.Text{Text: CanceledText})},
		RunID:    v.lastRun,
		Record:   mustJSON(Record{Canceled: &CanceledRecord{At: r.clk.Now()}}),
	})
	return err
}

// append stores e at the position after v and applies it to v and the cache.
func (r *Runner) append(ctx context.Context, conv string, v *view, e store.Entry) (int64, error) {
	return r.appendAll(ctx, conv, v, []store.Entry{e})
}

// appendAll stores es, in one write, at the positions after v and applies
// them to v and the cache. It returns the last position. An unknown failure
// is retried: identical entries at the same positions are a no-op. A
// conflict means another writer: the cache is brought up to date and the
// conflict reported; nothing is written again.
func (r *Runner) appendAll(ctx context.Context, conv string, v *view, es []store.Entry) (int64, error) {
	for i := range es {
		es[i].Seq = v.seq + 1 + int64(i)
	}
	var err error
	for range 3 {
		if len(es) == 1 {
			err = r.store.Append(ctx, conv, es[0])
		} else {
			err = r.store.AppendBatch(ctx, conv, es)
		}
		if err == nil || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrGap) ||
			errors.Is(err, store.ErrInvalid) || errors.Is(err, tenancy.ErrNoTenant) || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			_, _ = r.cache.load(ctx, r.store, conv)
		}
		at := strconv.FormatInt(es[0].Seq, 10)
		if len(es) > 1 {
			at = fmt.Sprintf("entries %s-%d", at, es[len(es)-1].Seq)
		} else {
			at = "entry " + at
		}
		return 0, fmt.Errorf("append %s to conversation %q: %w", at, conv, err)
	}
	for _, e := range es {
		r.cache.appended(ctx, conv, e)
		v.apply(e)
	}
	return v.seq, nil
}

// exec is one execution of a run: the run as last stored and the lease owner.
type exec struct {
	r      *Runner
	tenant tenancy.Tenant
	run    store.Run
	owner  string
}

type outKind uint8

const (
	outCompleted  outKind = iota // the run completed
	outSuspended                 // a flow waits for approvals
	outRejected                  // an approval was rejected
	outFailed                    // the run failed: stored per the retry policy
	outAbort                     // the run could not be persisted: stop, no transition
	outNotStarted                // nothing to resume: cancel the run
)

type outcome struct {
	kind    outKind
	err     error
	usage   llm.Usage
	result  *harness.Result
	state   *flow.State
	pending []string
}

func (x *exec) execution(ctx context.Context, body func(context.Context, func(Event) bool) outcome) *Execution {
	return &Execution{Run: x.run, events: func(yield func(Event, error) bool) { x.drive(ctx, body, yield) }}
}

// drive runs body under the lease and stores how the run ended.
func (x *exec) drive(ctx context.Context, body func(context.Context, func(Event) bool) outcome, yield func(Event, error) bool) {
	r := x.r
	wctx := context.WithoutCancel(ctx)
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	live, err := r.register(x.tenant, x.run.ID, cancel)
	if err != nil {
		x.release(wctx) // for another process, at once
		yield(Event{}, fmt.Errorf("run %q: %w", x.run.ID, err))
		return
	}
	defer r.unregister(x.tenant, x.run.ID, live)

	// The lease may have expired since the execution was made. Claimed less
	// than a third of its time to live ago, it is renewed in time by
	// keepLease without a write now.
	if l := x.run.Lease; l.Owner != x.owner || l.Expires.Sub(r.clk.Now()) <= r.ttl*2/3 {
		if _, err := r.store.Renew(runCtx, x.run.ID, x.owner, r.ttl); err != nil {
			yield(Event{}, fmt.Errorf("run %q: %w", x.run.ID, err))
			return
		}
	}
	stopRenewing := x.keepLease(runCtx, cancel)
	stopped := false
	emit := func(ev Event) bool {
		if stopped {
			return false
		}
		ev.RunID = x.run.ID
		if !yield(ev, nil) {
			stopped = true
			cancel(errStopped)
			return false
		}
		return true
	}
	out := body(runCtx, emit)
	stopRenewing()

	ev, err := x.finish(wctx, runCtx, out)
	if stopped {
		return
	}
	if err != nil {
		yield(Event{}, err)
		return
	}
	ev.RunID = x.run.ID
	yield(ev, nil)
}

// finish stores how the run ended and releases the lease. It returns the
// final event, or the error that ends the execution.
func (x *exec) finish(ctx, runCtx context.Context, out outcome) (Event, error) {
	id := x.run.ID
	if runCtx.Err() != nil && out.kind != outCompleted {
		// Interrupted: by Cancel, by the consumer, by ctx or by a lost lease.
		// The run stays as it is, for whoever continues it.
		cause := context.Cause(runCtx)
		x.release(ctx)
		var lost leaseLost
		switch {
		case errors.As(cause, &lost):
			return Event{}, fmt.Errorf("run %q: %w", id, cause)
		case errors.Is(cause, ErrCanceled):
			return Event{}, fmt.Errorf("run %q: %w", id, ErrCanceled)
		}
		return Event{}, fmt.Errorf("run %q: %w", id, cause)
	}
	defer x.release(ctx)
	switch out.kind {
	case outCompleted:
		if err := x.transition(ctx, store.StatusCompleted, store.Details{Usage: out.usage}); err != nil {
			return Event{}, fmt.Errorf("run %q: complete: %w", id, err)
		}
		return Event{Kind: EventDone, Run: x.run, Result: out.result, State: out.state}, nil
	case outSuspended:
		if err := x.transition(ctx, store.StatusSuspended, store.Details{Usage: out.usage, Pending: out.pending}); err != nil {
			return Event{}, fmt.Errorf("run %q: suspend: %w", id, err)
		}
		return Event{Kind: EventDone, Run: x.run}, nil
	case outRejected:
		if err := x.transition(ctx, store.StatusRejected, store.Details{Usage: out.usage, Error: out.err.Error()}); err != nil {
			return Event{}, fmt.Errorf("run %q: reject: %w", id, err)
		}
		return Event{Kind: EventDone, Run: x.run}, nil
	case outNotStarted:
		if err := x.transition(ctx, store.StatusCanceled, store.Details{Error: ErrNotStarted.Error()}); err != nil {
			return Event{}, fmt.Errorf("run %q: %w; cancel: %w", id, ErrNotStarted, err)
		}
		return Event{}, fmt.Errorf("run %q: %w", id, ErrNotStarted)
	case outAbort:
		return Event{}, fmt.Errorf("run %q: %w", id, out.err)
	}
	d := store.Details{Usage: out.usage, Error: out.err.Error()}
	// An interactive run was retried inline: it fails for good.
	if n := x.run.Attempts + 1; !x.run.Interactive && llm.IsRetryable(out.err) && n < x.r.retry.MaxAttempts {
		d.Retryable = true
		d.NextAttemptAt = x.r.clk.Now().Add(x.r.retry.delay(n, x.r.jitter))
	}
	if err := x.transition(ctx, store.StatusFailed, d); err != nil {
		return Event{}, fmt.Errorf("run %q: %w; storing the failure: %w", id, out.err, err)
	}
	return Event{}, &RunError{Run: x.run, Err: out.err}
}

// transition moves the run from the status it was last stored in. A failure
// that is not one of the store's answers may have happened anyway (a
// timeout): the run is read back to see.
func (x *exec) transition(ctx context.Context, to store.Status, d store.Details) error {
	run, err := x.r.store.Transition(ctx, x.run.ID, x.run.Status, to, d)
	if err == nil {
		x.run = run
		return nil
	}
	for _, known := range []error{store.ErrConflict, store.ErrFinal, store.ErrInvalid, store.ErrNotFound, tenancy.ErrNoTenant} {
		if errors.Is(err, known) {
			return err
		}
	}
	if got, gerr := x.r.store.Get(ctx, x.run.ID); gerr == nil && got.Status == to && x.run.Status != to {
		x.run = got
		return nil
	}
	return err
}

// release gives the lease up. It fails only if another owner holds it, which
// is no business of this execution any more.
func (x *exec) release(ctx context.Context) {
	_ = x.r.store.Release(ctx, x.run.ID, x.owner)
}

// keepLease renews the lease every third of its time to live until stop is
// called. If the lease is lost, or the run became final or vanished, it
// cancels the run with a [leaseLost] cause, so that the execution stops
// writing. Other failures are retried at the next renewal; if they last, the
// lease expires and the next renewal reports it lost.
func (x *exec) keepLease(ctx context.Context, cancel context.CancelCauseFunc) (stop func()) {
	quit, done := make(chan struct{}), make(chan struct{})
	every := x.r.ttl / 3
	go func() {
		defer close(done)
		t := x.r.clk.NewTimer(every)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ctx.Done():
				return
			case <-t.C():
			}
			_, err := x.r.store.Renew(ctx, x.run.ID, x.owner, x.r.ttl)
			if errors.Is(err, store.ErrLeaseLost) || errors.Is(err, store.ErrLeased) ||
				errors.Is(err, store.ErrFinal) || errors.Is(err, store.ErrNotFound) {
				cancel(leaseLost{err})
				return
			}
			t.Reset(every)
		}
	}()
	return func() { close(quit); <-done }
}

// chat runs a chat run's agent: a new run if msg is set, a resume otherwise.
// flush is the run's FlushEvery, 0 for the runner's; a resume reads it from
// the run's prompt entry.
func (x *exec) chat(ctx context.Context, agent Agent, msg *llm.Message, flush int, emit func(Event) bool) outcome {
	r, conv := x.r, x.run.ConversationID
	v, err := r.cache.load(ctx, r.store, conv)
	if err != nil {
		return outcome{kind: outAbort, err: err}
	}
	in := harness.Input{Transcript: v.tr}
	if msg != nil {
		if v.endsOnUser() {
			// A run canceled before the model answered, whose canceler
			// could not store the marker.
			if err := r.appendMarker(ctx, conv, &v); err != nil {
				return outcome{kind: outAbort, err: err}
			}
			in.Transcript = v.tr
		}
		in.Message = *msg
	} else {
		switch {
		case v.lastRun != x.run.ID:
			// One unfinished run per conversation, so its entries are the
			// last ones: none means its message was never stored.
			return outcome{kind: outNotStarted}
		case !v.endsOnUser():
			// The run had answered; only its completion was not stored.
			return outcome{kind: outCompleted}
		}
		flush = v.flush
	}
	t := &turns{x: x, v: &v, every: flush, emit: emit}
	if flush == 0 {
		t.every = r.flush
	}
	var spent llm.Usage
	for attempt := 1; ; attempt++ {
		out := t.run(ctx, agent, in, flush)
		if t.failed {
			return out
		}
		// The turns the run did are stored before how it ended, and when it
		// was interrupted; but not after its lease was lost, when the run's
		// next execution may be writing the same positions.
		if _, lost := errors.AsType[leaseLost](context.Cause(ctx)); lost {
			return out
		}
		if err := t.flush(context.WithoutCancel(ctx)); err != nil {
			return outcome{kind: outAbort, err: err}
		}
		spent = addUsage(spent, out.usage)
		out.usage = spent
		if out.kind != outFailed || !x.run.Interactive || !llm.IsRetryable(out.err) ||
			attempt >= r.iretry.MaxAttempts || ctx.Err() != nil {
			return out
		}
		// Retried inline, while the user waits: nothing is written but the
		// turns. An interrupt ends the wait, and finish sees why.
		delay := r.iretry.delay(attempt, r.jitter)
		if !emit(Event{Kind: EventRetrying, Attempt: attempt + 1, Delay: delay, Err: out.err}) || !r.wait(ctx, delay) {
			return out
		}
		if !t.v.endsOnUser() {
			// The model had answered: only the run's end failed.
			return outcome{kind: outCompleted, usage: spent}
		}
		in = harness.Input{Transcript: t.v.tr.Clone()}
	}
}

// wait waits d on the runner's clock. It reports false if ctx ended first.
func (r *Runner) wait(ctx context.Context, d time.Duration) bool {
	t := r.clk.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return true
	case <-ctx.Done():
		return false
	}
}

func addUsage(a, b llm.Usage) llm.Usage {
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.CacheWriteTokens += b.CacheWriteTokens
	return a
}

// turns stores a chat execution's entries: the prompt at once, the turns in
// batches.
type turns struct {
	x     *exec
	v     *view // the context as stored
	every int
	emit  func(Event) bool
	// pending are the turns not stored yet, at the positions after v.
	pending []store.Entry
	// failed is set when a write failed: nothing more is written.
	failed bool
}

// run runs the agent, streaming its events. flush is recorded with the
// prompt.
func (t *turns) run(ctx context.Context, agent Agent, in harness.Input, flush int) outcome {
	x := t.x
	var result *harness.Result
	for ev, err := range agent.Run(ctx, in) {
		if err != nil {
			out := outcome{kind: outFailed, err: err}
			if re, ok := errors.AsType[*harness.RunError](err); ok {
				out.usage = re.Usage()
			}
			return out
		}
		e := Event{Kind: EventHarness, Harness: ev}
		save, saved := false, false
		switch ev.Kind {
		case harness.EventRunStart:
			if ev.Prompt != nil {
				// Stored before its event: a run whose prompt is not
				// stored has not started.
				e.Seq, err = x.r.append(ctx, x.run.ConversationID, t.v, store.Entry{
					Kind:     store.KindPrompt,
					Messages: []llm.Message{ev.Prompt.Message},
					RunID:    x.run.ID,
					Record:   mustJSON(Record{Prompt: &PromptRecord{At: ev.Prompt.At, FlushEvery: flush}}),
				})
				if err != nil {
					t.failed = true
					return outcome{kind: outAbort, err: err}
				}
				saved = true
			}
		case harness.EventTurnDone:
			if tr := ev.TurnRecord; tr != nil {
				msgs := make([]llm.Message, len(tr.Messages))
				for i, m := range tr.Messages {
					msgs[i] = m.Message
				}
				e.Seq = t.v.seq + int64(len(t.pending)) + 1
				t.pending = append(t.pending, store.Entry{
					Seq:      e.Seq,
					Kind:     store.KindTurn,
					Messages: msgs,
					RunID:    x.run.ID,
					Record:   mustJSON(Record{Turn: turnRecord(tr)}),
				})
				save = len(t.pending) >= t.every || changedWorld(tr)
			}
		case harness.EventRunDone:
			result = ev.Result
		}
		if !t.emit(e) {
			if result != nil {
				// Every turn is done: the run is complete once they are
				// stored.
				break
			}
			return outcome{kind: outAbort, err: errStopped}
		}
		if saved {
			t.emit(Event{Kind: EventSaved, Seq: e.Seq})
		}
		// An interrupted run's turns are left to the flush after the loop,
		// which knows why it was interrupted.
		if save && ctx.Err() == nil {
			if err := t.flush(ctx); err != nil {
				return outcome{kind: outAbort, err: err}
			}
		}
	}
	if result == nil {
		return outcome{kind: outFailed, err: fmt.Errorf("%w: agent ended without a result", harness.ErrUnexpectedStop)}
	}
	return outcome{kind: outCompleted, usage: result.Usage, result: result}
}

// flush stores the pending entries in one write and tells the consumer.
func (t *turns) flush(ctx context.Context) error {
	if len(t.pending) == 0 || t.failed {
		return nil
	}
	seq, err := t.x.r.appendAll(ctx, t.x.run.ConversationID, t.v, t.pending)
	if err != nil {
		t.failed = true
		return err
	}
	t.pending = nil
	t.emit(Event{Kind: EventSaved, Seq: seq})
	return nil
}

// changedWorld reports whether a turn called a tool that may have changed
// something: one not declared read-only. Such a turn is stored before the
// next model call, so that a resume does not call the tool again.
func changedWorld(tr *harness.TurnRecord) bool {
	for _, c := range tr.Tools {
		if c.Kind != tool.KindRead {
			return true
		}
	}
	return false
}

// flow runs a flow run from its checkpoint, streaming its events.
func (x *exec) flow(ctx context.Context, step flow.Step, c *resumeConfig, emit func(Event) bool) outcome {
	events := make(chan flow.Event)
	quit := make(chan struct{})
	type result struct {
		st  *flow.State
		err error
	}
	done := make(chan result, 1)
	opts := []flow.Option{
		flow.WithCheckpointer(x.r.store),
		flow.WithObserver(func(ev flow.Event) {
			select {
			case events <- ev:
			case <-quit:
			}
		}),
	}
	if c != nil && c.allowShape {
		opts = append(opts, flow.WithAllowShapeChange())
	}
	if c != nil && c.decision != nil {
		opts = append(opts, flow.WithDecision(c.decision.path, c.decision.d))
	}
	go func() {
		st, err := flow.Resume(ctx, x.run.ID, step, opts...)
		done <- result{st, err}
	}()
	var res result
	listening := true
	for waiting := true; waiting; {
		select {
		case ev := <-events:
			if listening && !emit(Event{Kind: EventFlow, Flow: ev}) {
				listening = false
				close(quit)
			}
		case res = <-done:
			waiting = false
		}
	}
	if listening {
		close(quit)
	}
	if !listening {
		return outcome{kind: outAbort, err: errStopped}
	}
	var susp *flow.SuspendedError
	switch {
	case res.err == nil:
		return outcome{kind: outCompleted, state: res.st}
	case errors.As(res.err, &susp):
		return outcome{kind: outSuspended, pending: susp.Paths}
	case errors.Is(res.err, flow.ErrRejected):
		return outcome{kind: outRejected, err: res.err}
	case errors.Is(res.err, flow.ErrShapeChanged):
		// Changed since resume checked: leave the run as it is.
		return outcome{kind: outAbort, err: res.err}
	}
	return outcome{kind: outFailed, err: res.err}
}
