package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// toolCall is the outcome of one tool call. Its worker writes it, and the
// run reads it after the worker's done event has been received, which orders
// the two.
type toolCall struct {
	result llm.ToolResult
	rec    ToolRecord
	err    error // ends the run
	// interrupt is set when the call waits for a person: it has no result.
	interrupt *Interrupt
}

// job is one call for runTools to run: a call of the model's reply (call is
// its index in the turn), or, on a resume, an interrupted call with its
// answer.
type job struct {
	call      int
	use       llm.ToolUse
	interrupt *Interrupt
	answer    *Answer
}

// toolEvent is what the workers hand to the run: exactly one done (or
// interrupt) event per call. skipped says the call never began (the run ended
// while it waited for a free slot), so the consumer is not told about it.
type toolEvent struct {
	ev      Event
	skipped bool
}

func cloneUse(u llm.ToolUse) llm.ToolUse {
	u.Input = json.RawMessage(bytes.Clone(u.Input))
	return u
}

func isCancel(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// runTools runs the jobs of a turn in parallel, at most maxParallel at a
// time, and delivers their events as they happen. It returns the outcomes in
// the order of the jobs, and returns only when every goroutine it started has
// finished, whichever way it leaves.
//
// Only this goroutine yields to the consumer: workers hand their events over a
// channel that is big enough for every event they will send, so a worker never
// blocks, even when the consumer has stopped listening.
//
// The calls the model made belong to the transcript, so workers only ever get
// copies of them: neither the consumer, a hook nor a tool can change history.
func (r *run) runTools(ctx context.Context, turn int, jobs []job) ([]toolCall, error) {
	n := len(jobs)
	calls := make([]toolCall, n)
	events := make(chan toolEvent, 4*n) // at most a start, a policy, a flag and a done per call
	sem := make(chan struct{}, r.h.cfg.maxParallel)

	toolCtx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	// One deferred function, so that the order is right on every exit, a
	// panic in the consumer's loop body included: cancel first, then wait.
	defer func() {
		stop()
		wg.Wait()
	}()
	// A dispatcher hands out the free slots in the order of the calls, so that
	// with one slot the calls run one after the other, in order. It is part of
	// wg, which keeps adding workers to it legal.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i, j := range jobs {
			select {
			case sem <- struct{}{}:
			case <-toolCtx.Done():
			}
			// A slot freed by the cancellation itself can win the select: a
			// call never begins once the run has ended.
			if toolCtx.Err() != nil {
				for k := i; k < n; k++ {
					calls[k].err = toolCtx.Err()
					events <- toolEvent{skipped: true, ev: Event{Kind: EventToolDone}}
				}
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				r.h.call(toolCtx, stop, turn, j, r.toolDef(j.use.Name), r.policy, r.mode, r.deferral, r.dropped, &calls[i], events)
			}()
		}
	}()

	for finished := 0; finished < n; {
		te := <-events
		if te.ev.Kind == EventToolDone || te.ev.Kind == EventInterrupt {
			finished++
		}
		if te.skipped {
			continue
		}
		if !r.emit(te.ev) {
			return nil, nil
		}
	}
	wg.Wait()

	// A turn whose calls all finished is complete, even if the context was
	// cancelled just after: its tools have run, and dropping the turn would
	// have them run again on resume. The next check of the context ends the run.
	var first error
	for i := range calls {
		if err := calls[i].err; err != nil && (first == nil || isCancel(first) && !isCancel(err)) {
			first = err // the cause, not a sibling that was cancelled because of it
		}
	}
	if first != nil {
		return nil, ctxOr(ctx, first)
	}
	return calls, nil
}

// call runs one tool call, which holds a slot, on its own goroutine. It always
// sends exactly one done event, or for a call that waits for a person one
// interrupt event, after the start event and the policy's.
func (h *Harness) call(ctx context.Context, stop context.CancelFunc, turn int, j job, def tool.Definition, pol *policy.Policy, mode Interaction, dfr *deferral, dropped map[string]bool, out *toolCall, events chan<- toolEvent) {
	u := j.use
	kind, _ := def.Class()
	out.rec = ToolRecord{ToolUseID: u.ID, Name: u.Name, Kind: kind}
	defer func() {
		if v := recover(); v != nil {
			// A hook, or a registry that does not protect itself, panicked
			// (the in-process registry turns a tool's own panic into an error).
			// The value is kept for the error; a stack never reaches the model.
			out.err = fmt.Errorf("tool %q: %w", u.Name, &tool.PanicError{Value: v, Stack: debug.Stack()})
			out.result = errorResult(u.ID, errors.New("internal error"))
			out.rec.IsError = true
			out.interrupt = nil
			if !out.rec.StartedAt.IsZero() {
				out.rec.Duration = h.cfg.clock.Now().Sub(out.rec.StartedAt)
			}
		}
		if out.err != nil && !isCancel(out.err) {
			stop() // the run is over: do not wait for the other calls
		}
		if in := out.interrupt; in != nil {
			ev := in.clone()
			events <- toolEvent{ev: Event{
				Kind: EventInterrupt, Turn: turn, At: h.cfg.clock.Now(), Call: j.call,
				ToolUse: llm.ToolUse{ID: u.ID, Name: u.Name}, Interrupt: &ev,
			}}
			return
		}
		events <- toolEvent{ev: Event{
			Kind: EventToolDone, Turn: turn, At: h.cfg.clock.Now(), Call: j.call,
			ToolResult: out.result, Duration: out.rec.Duration,
		}}
	}()

	start := h.cfg.clock.Now()
	out.rec.StartedAt = start
	events <- toolEvent{ev: Event{Kind: EventToolStart, Turn: turn, At: start, Call: j.call, ToolUse: cloneUse(u)}}

	hooked := cloneUse(u) // hooks may scribble on their copy
	var res llm.ToolResult
	var ask *tool.InterruptError
	var err error
	switch {
	case j.answer != nil && (j.interrupt.Kind != InterruptApproval || !j.answer.Approved):
		// Answered: a question's answer, or a denial, is the result.
		res = j.interrupt.result(*j.answer)
	case j.answer != nil:
		// Approved: the call runs with its original input, byte for byte,
		// as the policy's decision was taken already.
		res, ask, err = h.invoke(ctx, hooked, []byte(j.interrupt.Input), dfr)
	case dropped[u.Name]:
		// The run's choice left the tool out: the model was not given it.
		res = errorResult(u.ID, errNotChosen(u.Name))
	case dfr.waiting(u.Name):
		// The model was never given the tool: it guessed the name.
		res = errorResult(u.ID, errNotLoaded(u.Name))
	default:
		refused, ok, in := h.decide(turn, j.call, u, def, pol, mode, events)
		switch {
		case in != nil:
			out.interrupt = in
		case ok:
			res = refused
		default:
			res, ask, err = h.invoke(ctx, hooked, bytes.Clone(u.Input), dfr)
		}
	}
	if ask != nil {
		if mode.waits() {
			out.interrupt = &Interrupt{
				ID: u.ID, Call: j.call, Name: u.Name, Kind: InterruptQuestion,
				Prompt: ask.Prompt, Choices: slices.Clone(ask.Choices),
			}
		} else {
			res = errorResult(u.ID, fmt.Errorf("tool %q asks a person (%q), and nobody can answer in this run: it is %s", u.Name, ask.Prompt, mode))
		}
	}
	switch {
	case out.interrupt != nil:
	case err != nil:
		out.err = err
		out.result = errorResult(u.ID, err)
	default:
		loads := res.Loads // what loaded is the harness's to say, not a hook's
		for _, f := range h.cfg.after {
			res = f(ctx, hooked, res)
			res.ToolUseID = u.ID
		}
		res.Loads = nil
		if !res.IsError {
			res.Loads = slices.Clone(loads)
		}
		out.result = res
	}
	out.rec.Duration = h.cfg.clock.Now().Sub(start)
	out.rec.IsError = out.result.IsError
}

// decide applies the run's policy to a call: it sends the decision's events
// and, for a call that must not run, returns the error result the model sees
// and true; for a call that waits for an approval (in a mode that may wait),
// its interrupt.
func (h *Harness) decide(turn, i int, u llm.ToolUse, def tool.Definition, pol *policy.Policy, mode Interaction, events chan<- toolEvent) (llm.ToolResult, bool, *Interrupt) {
	if pol == nil {
		return llm.ToolResult{}, false, nil
	}
	toolset := ""
	if h.cfg.toolsetOf != nil {
		toolset = h.cfg.toolsetOf(u.Name)
	}
	d := pol.Decide(def, toolset)
	if d.Action == policy.Allow {
		return llm.ToolResult{}, false, nil
	}
	ev := Event{Turn: turn, At: h.cfg.clock.Now(), Call: i, ToolUse: llm.ToolUse{ID: u.ID, Name: u.Name}, Decision: d}
	ev.Kind = EventToolPolicy
	events <- toolEvent{ev: ev}
	switch d.Action {
	case policy.Flag:
		ev.Kind = EventToolFlagged
		events <- toolEvent{ev: ev}
		return llm.ToolResult{}, false, nil
	case policy.Approve:
		if mode.waits() {
			return llm.ToolResult{}, false, &Interrupt{
				ID: u.ID, Call: i, Name: u.Name, Kind: InterruptApproval, Prompt: d.Reason, Decision: &d,
				Input: string(u.Input),
			}
		}
		return errorResult(u.ID, &PolicyError{Decision: d, NeedsApproval: true, Interaction: mode}), true, nil
	}
	return errorResult(u.ID, &PolicyError{Decision: d}), true, nil
}

// invoke runs the hooks and the tool. It returns an error only for failures
// that end the run; everything the model can react to is an error result. A
// tool that asks a person ([tool.InterruptError]) returns its question.
func (h *Harness) invoke(ctx context.Context, u llm.ToolUse, input json.RawMessage, dfr *deferral) (llm.ToolResult, *tool.InterruptError, error) {
	for _, f := range h.cfg.before {
		if err := f(ctx, u); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return llm.ToolResult{}, nil, cerr
			}
			return errorResult(u.ID, err), nil, nil
		}
	}
	if u.Name == SearchTool && dfr != nil {
		return dfr.search(u.ID, input), nil, nil
	}
	out, err := h.tools.Invoke(ctx, u.Name, input)
	ask, asks := errors.AsType[*tool.InterruptError](err)
	switch {
	case err == nil:
		res := llm.ToolResult{ToolUseID: u.ID, Content: string(out)}
		if load := h.cfg.loaders[u.Name]; load != nil && dfr != nil {
			res.Loads = dfr.match(load(input))
		}
		return res, nil, nil
	case ctx.Err() != nil:
		return llm.ToolResult{}, nil, ctx.Err()
	case asks:
		return llm.ToolResult{}, &tool.InterruptError{Prompt: ask.Prompt, Choices: slices.Clone(ask.Choices)}, nil
	case errors.Is(err, tool.ErrInvalidInput), errors.Is(err, tool.ErrToolFailed),
		errors.Is(err, tool.ErrNotFound), errors.Is(err, context.DeadlineExceeded):
		// The deadline case is a tool's own time limit (tool.Timeout): the
		// run's context is still live.
		return errorResult(u.ID, err), nil, nil
	default:
		return llm.ToolResult{}, nil, fmt.Errorf("tool %q: %w", u.Name, err)
	}
}

// errorResult is what the model is shown for a failed call. The content is the
// error's message; errors from the registry keep stacks out of it.
func errorResult(id string, err error) llm.ToolResult {
	return llm.ToolResult{ToolUseID: id, Content: err.Error(), IsError: true}
}
