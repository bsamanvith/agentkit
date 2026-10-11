package flow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
)

// Parallel runs branches concurrently and waits for all of them. Each branch
// gets its own copy of the state as it was when Parallel started; when every
// branch has finished, the changes are merged back into the state.
//
// A key that two branches changed (set, changed or deleted) is a conflict,
// even if both wrote the same value: the merge fails with [ConflictError] and
// leaves the state unchanged. Because the merge goes branch by branch in order
// and conflicts are errors, the result does not depend on which branch
// finished first.
//
// If a branch fails, the others are cancelled and Parallel returns the error
// of the lowest-numbered branch that failed on its own. A branch waiting at an
// [Approve] does not cancel the others: they run to completion, and Parallel
// then returns a [SuspendedError] listing every pending approval.
//
// A branch's path is the Parallel's path, then "[i]" for its index, then the
// branch's name: "review/gather[1]/search".
func Parallel(name string, branches ...Step) Step {
	return &parallel{name: name, branches: slices.Clone(branches)}
}

type parallel struct {
	name     string
	branches []Step
}

func (p *parallel) Name() string { return p.name }

func (p *parallel) Run(parent context.Context, st *State) error {
	sc := scopeOf(parent, p.name)
	base := st.Clone()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	states := make([]*State, len(p.branches))
	errs := make([]error, len(p.branches))
	var failed atomic.Bool
	var wg sync.WaitGroup
	for i, b := range p.branches {
		states[i] = base.Clone()
		path := fmt.Sprintf("%s[%d]/%s", sc.path, i, nameOf(b))
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = sc.rt.step(ctx, path, b, states[i])
			if errs[i] != nil && !errors.Is(errs[i], ErrSuspended) {
				failed.Store(true)
				cancel()
			}
		}()
	}
	wg.Wait()

	if err := p.firstFailure(parent, errs, failed.Load()); err != nil {
		return err
	}
	if pending := pendingApprovals(errs); len(pending) > 0 {
		return &SuspendedError{Paths: pending}
	}
	return p.merge(sc.path, st, base, states)
}

// firstFailure picks the error to report: the lowest-numbered branch that
// failed on its own. A branch that only stopped because we cancelled it after
// a sibling failed is not the cause.
func (p *parallel) firstFailure(parent context.Context, errs []error, siblingFailed bool) error {
	var fallback error
	for _, err := range errs {
		if err == nil || errors.Is(err, ErrSuspended) {
			continue
		}
		if fallback == nil {
			fallback = err
		}
		if siblingFailed && parent.Err() == nil && errors.Is(err, context.Canceled) {
			continue
		}
		return err
	}
	return fallback
}

func pendingApprovals(errs []error) []string {
	var paths []string
	for _, err := range errs {
		var susp *SuspendedError
		if errors.As(err, &susp) {
			paths = append(paths, susp.Paths...)
		}
	}
	sort.Strings(paths)
	return slices.Compact(paths)
}

// merge applies every branch's changes to st, or applies none and reports the
// conflicting keys.
func (p *parallel) merge(path string, st, base *State, branches []*State) error {
	merged := map[string]write{}
	owner := map[string]int{}
	conflicts := map[string]bool{}
	for i, b := range branches {
		for k, w := range b.changesFrom(base) {
			if _, taken := owner[k]; taken {
				conflicts[k] = true
				continue
			}
			owner[k] = i
			merged[k] = w
		}
	}
	if len(conflicts) > 0 {
		keys := make([]string, 0, len(conflicts))
		for k := range conflicts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return &ConflictError{Path: path, Keys: keys}
	}
	st.apply(merged)
	return nil
}

func (p *parallel) check(path string, e *problems) {
	for i, b := range p.branches {
		if n, ok := childName(b, path, e); ok {
			checkStep(b, fmt.Sprintf("%s[%d]/%s", path, i, n), e)
		}
	}
}
