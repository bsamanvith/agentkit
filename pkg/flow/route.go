package flow

import (
	"context"
	"fmt"
	"maps"
	"sort"
)

// Route computes a key from the state and runs the step registered under it.
// A key with no step is [ErrUnknownRoute] and nothing runs. The chosen step's
// path is "<route>[key]/<step name>". The route keys follow the same rules as
// step names (non-empty, none of / [ ]).
//
// key runs again on resume, on the state restored from the checkpoint, so it
// must be a pure function of the state.
func Route(name string, key func(st *State) (string, error), routes map[string]Step) Step {
	return &route{name: name, key: key, routes: maps.Clone(routes)}
}

type route struct {
	name   string
	key    func(*State) (string, error)
	routes map[string]Step
}

func (r *route) Name() string { return r.name }

func (r *route) Run(ctx context.Context, st *State) error {
	sc := scopeOf(ctx, r.name)
	k, err := r.key(st)
	if err != nil {
		return fmt.Errorf("route key: %w", err)
	}
	child, ok := r.routes[k]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownRoute, k)
	}
	return sc.rt.step(ctx, fmt.Sprintf("%s[%s]/%s", sc.path, k, nameOf(child)), child, st)
}

func (r *route) check(path string, e *problems) {
	if r.key == nil {
		e.add("%s: Route has no key function", path)
	}
	keys := make([]string, 0, len(r.routes))
	for k := range r.routes {
		keys = append(keys, k)
	}
	sort.Strings(keys) // validate in a fixed order so errors are stable
	for _, k := range keys {
		if !validSegment(k) {
			e.add("%s: invalid route key %q (keys must be non-empty and contain none of / [ ])", path, k)
			continue
		}
		child := r.routes[k]
		if n, ok := childName(child, path, e); ok {
			checkStep(child, fmt.Sprintf("%s[%s]/%s", path, k, n), e)
		}
	}
}
