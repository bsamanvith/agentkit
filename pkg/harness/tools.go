package harness

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// toolCall is the outcome of one tool call. Its worker writes it, and the
// run reads it after the worker's EventToolDone has been received, which
// orders the two.
type toolCall struct {
	result llm.ToolResult
	rec    ToolRecord
	err    error // ends the run
}

// runTools runs all the calls of a turn in parallel, at most maxParallel at a
// time, and delivers their events as they happen. It returns the outcomes in
// the order of the calls, and returns only when every goroutine it started has
// finished.
//
// Only this goroutine yields to the consumer: workers hand their events over a
// channel that is big enough for every event they will send, so a worker never
// blocks, even when the consumer has stopped listening.
func (r *run) runTools(ctx context.Context, turn int, uses []llm.ToolUse) ([]toolCall, error) {
	n := len(uses)
	calls := make([]toolCall, n)
	events := make(chan Event, 2*n) // a start and a done per call
	sem := make(chan struct{}, r.h.cfg.maxParallel)

	toolCtx, stop := context.WithCancel(ctx)
	defer stop()
	var wg sync.WaitGroup
	for i, u := range uses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.h.call(toolCtx, sem, turn, i, u, &calls[i], events)
		}()
	}

	for finished := 0; finished < n; {
		ev := <-events
		if ev.Kind == EventToolDone {
			finished++
		}
		if !r.emit(ev) {
			stop()
			wg.Wait()
			return nil, nil
		}
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for i := range calls {
		if calls[i].err != nil {
			return nil, calls[i].err
		}
	}
	return calls, nil
}

// call runs one tool call on its own goroutine and always sends exactly one
// EventToolDone.
func (h *Harness) call(ctx context.Context, sem chan struct{}, turn, i int, u llm.ToolUse, out *toolCall, events chan<- Event) {
	out.rec = ToolRecord{ToolUseID: u.ID, Name: u.Name}
	defer func() {
		if v := recover(); v != nil {
			// A hook panicked (tools themselves are protected by the registry).
			// The value only: a stack never reaches the model.
			out.err = fmt.Errorf("tool %q: hook panicked: %v", u.Name, v)
			out.result = errorResult(u.ID, errors.New("internal error"))
		}
		events <- Event{
			Kind: EventToolDone, Turn: turn, At: h.cfg.now(), Call: i,
			ToolResult: out.result, Duration: out.rec.Duration,
		}
	}()

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		out.err = ctx.Err()
		out.result = errorResult(u.ID, ctx.Err())
		return
	}

	start := h.cfg.now()
	out.rec.StartedAt = start
	events <- Event{Kind: EventToolStart, Turn: turn, At: start, Call: i, ToolUse: u}

	res, err := h.invoke(ctx, u)
	if err != nil {
		out.err = err
		out.result = errorResult(u.ID, err)
	} else {
		for _, f := range h.cfg.after {
			res = f(ctx, u, res)
			res.ToolUseID = u.ID
		}
		out.result = res
	}
	out.rec.Duration = h.cfg.now().Sub(start)
	out.rec.IsError = out.result.IsError
}

// invoke runs the hooks and the tool. It returns an error only for failures
// that end the run; everything the model can react to is an error result.
func (h *Harness) invoke(ctx context.Context, u llm.ToolUse) (llm.ToolResult, error) {
	for _, f := range h.cfg.before {
		if err := f(ctx, u); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return llm.ToolResult{}, cerr
			}
			return errorResult(u.ID, err), nil
		}
	}
	out, err := h.tools.Invoke(ctx, u.Name, u.Input)
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
