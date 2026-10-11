package flow

import "context"

// Approve is a step that waits for a human. Run without a decision for its
// path, it suspends the run: the runner saves the checkpoint and returns a
// [SuspendedError] (matching [ErrSuspended]). [Resume] with [WithDecision] for
// the step's path continues past it if the decision approves, and ends the
// run with a [RejectedError] (matching [ErrRejected]) if it rejects. A
// rejection is recorded, so resuming again does not ask a second time.
func Approve(name string) Step { return &approve{name: name} }

type approve struct{ name string }

func (a *approve) Name() string { return a.name }

func (a *approve) Run(ctx context.Context, _ *State) error {
	sc := scopeOf(ctx, a.name)
	if reason, ok := sc.rt.rejection(sc.path); ok {
		return &RejectedError{Path: sc.path, Reason: reason}
	}
	d, ok := sc.rt.decisions[sc.path]
	if !ok {
		return &SuspendedError{Paths: []string{sc.path}}
	}
	if !d.Approved {
		sc.rt.reject(sc.path, d.Reason)
		return &RejectedError{Path: sc.path, Reason: d.Reason}
	}
	return nil
}
