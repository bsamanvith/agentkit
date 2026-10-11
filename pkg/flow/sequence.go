package flow

import (
	"context"
	"slices"
)

// Sequence runs steps in order on the same state and stops at the first
// error. The path of each child is the sequence's path plus the child's name,
// so the children of a sequence need distinct names.
func Sequence(name string, steps ...Step) Step {
	return &sequence{name: name, steps: slices.Clone(steps)}
}

type sequence struct {
	name  string
	steps []Step
}

func (s *sequence) Name() string { return s.name }

func (s *sequence) Run(ctx context.Context, st *State) error {
	sc := scopeOf(ctx, s.name)
	for _, child := range s.steps {
		if err := sc.rt.step(ctx, sc.path+"/"+nameOf(child), child, st); err != nil {
			return err
		}
	}
	return nil
}

func (s *sequence) check(path string, e *problems) {
	seen := map[string]bool{}
	for _, child := range s.steps {
		n, ok := childName(child, path, e)
		if !ok {
			continue
		}
		if seen[n] {
			e.add("%s: two steps are named %q", path, n)
		}
		seen[n] = true
		checkStep(child, path+"/"+n, e)
	}
}
