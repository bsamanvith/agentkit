package flow

import (
	"context"
	"fmt"
)

// Loop runs body repeatedly: after each run it calls until with the state, and
// stops as soon as that returns true. If body has run max times and until has
// still not held, Loop fails with [ErrMaxIterations]. Iteration i of body has
// the path "<loop>[i]/<body name>".
//
// until runs again on resume, on the state restored from the checkpoint, so
// it must be a pure function of the state.
func Loop(name string, body Step, until func(st *State) (bool, error), max int) Step {
	return &loop{name: name, body: body, until: until, max: max}
}

type loop struct {
	name  string
	body  Step
	until func(*State) (bool, error)
	max   int
}

func (l *loop) Name() string { return l.name }

func (l *loop) Run(ctx context.Context, st *State) error {
	sc := scopeOf(ctx, l.name)
	for i := range l.max {
		path := fmt.Sprintf("%s[%d]/%s", sc.path, i, nameOf(l.body))
		if err := sc.rt.step(ctx, path, l.body, st); err != nil {
			return err
		}
		done, err := l.until(st)
		if err != nil {
			return fmt.Errorf("loop condition after iteration %d: %w", i, err)
		}
		if done {
			return nil
		}
	}
	return fmt.Errorf("%w: %d iterations", ErrMaxIterations, l.max)
}

func (l *loop) check(path string, e *problems) {
	if l.until == nil {
		e.add("%s: Loop has no condition", path)
	}
	if l.max < 1 {
		e.add("%s: Loop maximum is %d, need at least 1", path, l.max)
	}
	if n, ok := childName(l.body, path, e); ok {
		checkStep(l.body, fmt.Sprintf("%s[0]/%s", path, n), e)
	}
}
