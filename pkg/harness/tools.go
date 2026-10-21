package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// toolCall is the outcome of one tool call. Its worker writes it, and the
// run reads it after the worker's done event has been received, which orders
// the two.
type toolCall struct {
	result llm.ToolResult
	rec    ToolRecord
	err    error // ends the run
}

// toolEvent is what the workers hand to the run: exactly one done event per
// call. skipped says the call never began (the run ended while it waited for a
// free slot), so the consumer is not told about it.
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

// runTools runs all the calls of a turn in parallel, at most maxParallel at a
// time, and delivers their events as they happen. It returns the outcomes in
// the order of the calls, and returns only when every goroutine it started has
// finished, whichever way it leaves.
//
// Only this goroutine yields to the consumer: workers hand their events over a
// channel that is big enough for every event they will send, so a worker never
// blocks, even when the consumer has stopped listening.
//
// The calls the model made belong to the transcript, so workers only ever get
// copies of them: neither the consumer, a hook nor a tool can change history.
func (r *run) runTools(ctx context.Context, turn int, uses []llm.ToolUse) ([]toolCall, error) {
	n := len(uses)
	calls := make([]toolCall, n)
	events := make(chan toolEvent, 2*n) // at most a start and a done per call
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
		for i, u := range uses {
			select {
			case sem <- struct{}{}:
			case <-toolCtx.Done():
			}
			// A slot freed by the cancellation itself can win the select: a
			// call never begins once the run has ended.
			if toolCtx.Err() != nil {
				for j := i; j < n; j++ {
					calls[j].err = toolCtx.Err()
					events <- toolEvent{skipped: true, ev: Event{Kind: EventToolDone}}
				}
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				r.h.call(toolCtx, stop, turn, i, u, r.kindOf(u.Name), &calls[i], events)
			}()
		}
	}()

	for finished := 0; finished < n; {
		te := <-events
		if te.ev.Kind == EventToolDone {
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
// sends exactly one done event, after the start event.
func (h *Harness) call(ctx context.Context, stop context.CancelFunc, turn, i int, u llm.ToolUse, kind tool.Kind, out *toolCall, events chan<- toolEvent) {
	out.rec = ToolRecord{ToolUseID: u.ID, Name: u.Name, Kind: kind}
	defer func() {
		if v := recover(); v != nil {
			// A hook, or a registry that does not protect itself, panicked
			// (the in-process registry turns a tool's own panic into an error).
			// The value is kept for the error; a stack never reaches the model.
			out.err = fmt.Errorf("tool %q: %w", u.Name, &tool.PanicError{Value: v, Stack: debug.Stack()})
			out.result = errorResult(u.ID, errors.New("internal error"))
			out.rec.IsError = true
			if !out.rec.StartedAt.IsZero() {
				out.rec.Duration = h.cfg.clock.Now().Sub(out.rec.StartedAt)
			}
		}
		if out.err != nil && !isCancel(out.err) {
			stop() // the run is over: do not wait for the other calls
		}
		events <- toolEvent{ev: Event{
			Kind: EventToolDone, Turn: turn, At: h.cfg.clock.Now(), Call: i,
			ToolResult: out.result, Duration: out.rec.Duration,
		}}
	}()

	start := h.cfg.clock.Now()
	out.rec.StartedAt = start
	events <- toolEvent{ev: Event{Kind: EventToolStart, Turn: turn, At: start, Call: i, ToolUse: cloneUse(u)}}

	hooked := cloneUse(u) // hooks may scribble on their copy
	res, err := h.invoke(ctx, hooked, bytes.Clone(u.Input))
	if err != nil {
		out.err = err
		out.result = errorResult(u.ID, err)
	} else {
		for _, f := range h.cfg.after {
			res = f(ctx, hooked, res)
			res.ToolUseID = u.ID
		}
		out.result = res
	}
	out.rec.Duration = h.cfg.clock.Now().Sub(start)
	out.rec.IsError = out.result.IsError
}

// invoke runs the hooks and the tool. It returns an error only for failures
// that end the run; everything the model can react to is an error result.
func (h *Harness) invoke(ctx context.Context, u llm.ToolUse, input json.RawMessage) (llm.ToolResult, error) {
	for _, f := range h.cfg.before {
		if err := f(ctx, u); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return llm.ToolResult{}, cerr
			}
			return errorResult(u.ID, err), nil
		}
	}
	out, err := h.tools.Invoke(ctx, u.Name, input)
	switch {
	case err == nil:
		return llm.ToolResult{ToolUseID: u.ID, Content: string(out)}, nil
	case ctx.Err() != nil:
		return llm.ToolResult{}, ctx.Err()
	case errors.Is(err, tool.ErrInvalidInput), errors.Is(err, tool.ErrToolFailed),
		errors.Is(err, tool.ErrNotFound), errors.Is(err, context.DeadlineExceeded):
		// The deadline case is a tool's own time limit (tool.Timeout): the
		// run's context is still live.
		return errorResult(u.ID, err), nil
	default:
		return llm.ToolResult{}, fmt.Errorf("tool %q: %w", u.Name, err)
	}
}

// errorResult is what the model is shown for a failed call. The content is the
// error's message; errors from the registry keep stacks out of it.
func errorResult(id string, err error) llm.ToolResult {
	return llm.ToolResult{ToolUseID: id, Content: err.Error(), IsError: true}
}
