package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
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

// Run starts a chat run: msg, a user message, on the conversation, run by the
// conversation's agent. It creates the run and claims its lease before it
// returns; iterate the execution's events to run it. A conversation with an
// unfinished run gives an [*UnfinishedError].
func (r *Runner) Run(ctx context.Context, conversationID string, msg llm.Message) (*Execution, error) {
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
	x, err := r.start(ctx, tenant, conversationID)
	if err != nil {
		return nil, err
	}
	msg = msg.Clone()
	return x.execution(ctx, func(ctx context.Context, emit func(Event) bool) outcome {
		return x.chat(ctx, agent, &msg, emit)
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
	x, err := r.start(ctx, tenant, conversationID)
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

// start creates a run on the conversation and claims it.
func (r *Runner) start(ctx context.Context, tenant tenancy.Tenant, conv string) (*exec, error) {
	run, err := r.store.Create(ctx, r.newID(), conv)
	if errors.Is(err, store.ErrUnfinished) {
		u, uerr := r.store.Unfinished(ctx, conv)
		if uerr != nil {
			return nil, fmt.Errorf("%w: %w", ErrRunUnfinished, uerr)
		}
		return nil, &UnfinishedError{ConversationID: conv, RunID: u.ID}
	}
	if err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	x := &exec{r: r, tenant: tenant, run: run, owner: r.nextOwner()}
	if _, err := r.store.Claim(ctx, run.ID, x.owner, r.ttl); err != nil {
		return nil, fmt.Errorf("run %q: claim: %w", run.ID, err)
	}
	return x, nil
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
			return func(ctx context.Context, emit func(Event) bool) outcome { return x.chat(ctx, agent, nil, emit) }
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
	if l := r.liveRun(tenant, runID); l != nil {
		l.cancel(ErrCanceled)
		select {
		case <-l.done:
		case <-ctx.Done():
			return store.Run{}, ctx.Err()
		}
	}
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
// An unknown failure is retried: an identical entry at the same position is a
// no-op. A conflict means another writer: the cache is brought up to date and
// the conflict reported; nothing is written again.
func (r *Runner) append(ctx context.Context, conv string, v *view, e store.Entry) (int64, error) {
	e.Seq = v.seq + 1
	var err error
	for range 3 {
		err = r.store.Append(ctx, conv, e)
		if err == nil || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrGap) ||
			errors.Is(err, store.ErrInvalid) || errors.Is(err, tenancy.ErrNoTenant) || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			_, _ = r.cache.load(ctx, r.store, conv)
		}
		return 0, fmt.Errorf("append entry %d to conversation %q: %w", e.Seq, conv, err)
	}
	r.cache.appended(ctx, conv, e)
	v.apply(e)
	return e.Seq, nil
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
	live, ok := r.register(x.tenant, x.run.ID, cancel)
	if !ok {
		yield(Event{}, fmt.Errorf("run %q: %w: running in this process", x.run.ID, store.ErrLeased))
		return
	}
	defer r.unregister(x.tenant, x.run.ID, live)

	// The lease may have expired since the execution was made.
	if _, err := r.store.Renew(runCtx, x.run.ID, x.owner, r.ttl); err != nil {
		yield(Event{}, fmt.Errorf("run %q: %w", x.run.ID, err))
		return
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
	if n := x.run.Attempts + 1; llm.IsRetryable(out.err) && n < x.r.retry.MaxAttempts {
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
func (x *exec) chat(ctx context.Context, agent Agent, msg *llm.Message, emit func(Event) bool) outcome {
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
	}

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
		switch ev.Kind {
		case harness.EventRunStart:
			if ev.Prompt != nil {
				e.Seq, err = r.append(ctx, conv, &v, store.Entry{
					Kind:     store.KindPrompt,
					Messages: []llm.Message{ev.Prompt.Message},
					RunID:    x.run.ID,
					Record:   mustJSON(Record{Prompt: &PromptRecord{At: ev.Prompt.At}}),
				})
			}
		case harness.EventTurnDone:
			if ev.TurnRecord != nil {
				msgs := make([]llm.Message, len(ev.TurnRecord.Messages))
				for i, m := range ev.TurnRecord.Messages {
					msgs[i] = m.Message
				}
				e.Seq, err = r.append(ctx, conv, &v, store.Entry{
					Kind:     store.KindTurn,
					Messages: msgs,
					RunID:    x.run.ID,
					Record:   mustJSON(Record{Turn: turnRecord(ev.TurnRecord)}),
				})
			}
		case harness.EventRunDone:
			result = ev.Result
		}
		if err != nil {
			return outcome{kind: outAbort, err: err}
		}
		if !emit(e) {
			if result != nil {
				// Every turn is stored: the run is complete.
				break
			}
			return outcome{kind: outAbort, err: errStopped}
		}
	}
	if result == nil {
		return outcome{kind: outFailed, err: fmt.Errorf("%w: agent ended without a result", harness.ErrUnexpectedStop)}
	}
	return outcome{kind: outCompleted, usage: result.Usage, result: result}
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
