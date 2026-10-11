package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Step is one unit of work: it reads and changes the state. A step must be
// safe to run again from the state it started with, because a run that failed
// halfway re-runs the steps that had not completed.
type Step interface {
	Run(ctx context.Context, st *State) error
}

// Namer is implemented by steps that have a name. Every step in a flow needs
// one, because the name is part of its checkpoint path. A step without a Name
// method is wrapped with [Named].
type Namer interface {
	Name() string
}

// Func adapts fn to a [Step] called name.
func Func(name string, fn func(ctx context.Context, st *State) error) Step {
	return &funcStep{name: name, fn: fn}
}

type funcStep struct {
	name string
	fn   func(ctx context.Context, st *State) error
}

func (f *funcStep) Name() string { return f.name }

func (f *funcStep) Run(ctx context.Context, st *State) error { return f.fn(ctx, st) }

func (f *funcStep) check(path string, e *problems) {
	if f.fn == nil {
		e.add("%s: Func has no function", path)
	}
}

// Named gives step the name used in its path, whatever name it has itself.
func Named(name string, step Step) Step { return &named{name: name, step: step} }

type named struct {
	name string
	step Step
}

func (n *named) Name() string { return n.name }

func (n *named) Run(ctx context.Context, st *State) error { return n.step.Run(ctx, st) }

func (n *named) check(path string, e *problems) {
	if n.step == nil {
		e.add("%s: Named has no step", path)
		return
	}
	checkStep(n.step, path, e)
}

// checker is implemented by steps that can validate themselves and their
// children. path is the step's own full path.
type checker interface {
	check(path string, e *problems)
}

type problems struct{ list []error }

func (p *problems) add(format string, args ...any) {
	p.list = append(p.list, fmt.Errorf("%w: "+format, append([]any{ErrInvalidFlow}, args...)...))
}

func checkStep(s Step, path string, e *problems) {
	if c, ok := s.(checker); ok {
		c.check(path, e)
	}
}

// validSegment reports whether name can be one segment of a step path.
func validSegment(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/[]")
}

func nameOf(s Step) string {
	if n, ok := s.(Namer); ok {
		return n.Name()
	}
	return ""
}

// childName returns the name of child, recording a problem if it has none or
// an illegal one. where describes the parent for the message.
func childName(child Step, where string, e *problems) (string, bool) {
	if child == nil {
		e.add("%s: nil step", where)
		return "", false
	}
	n := nameOf(child)
	if !validSegment(n) {
		e.add("%s: step %T has invalid name %q (names must be non-empty and contain none of / [ ])", where, child, n)
		return "", false
	}
	return n, true
}

// Validate checks the step tree: every step is named, names are legal, the
// children of a [Sequence] have distinct names, and combinators were built
// with usable arguments. [Run] and [Resume] call it; call it yourself at
// startup to fail early. Problems match [ErrInvalidFlow].
func Validate(step Step) error {
	var e problems
	n, ok := childName(step, "root", &e)
	if ok {
		checkStep(step, n, &e)
	}
	if len(e.list) == 0 {
		return nil
	}
	return errors.Join(e.list...)
}
