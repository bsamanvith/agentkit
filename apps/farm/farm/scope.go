package farm

import (
	"context"
	"errors"
	"fmt"
)

type holdingKey struct{}

type readOnlyKey struct{}

// ErrReadOnly is a change asked for in a context that may only read
// ([WithReadOnly]): a member whose role does not allow writing.
var ErrReadOnly = errors.New("farm: your role on this farm can only read it")

// WithReadOnly marks ctx as allowed only to read: a [Service] refuses every
// change made with it ([ErrReadOnly]), however it is asked for (the API,
// the assistant's tools).
func WithReadOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, readOnlyKey{}, true)
}

func readOnly(ctx context.Context) bool { v, _ := ctx.Value(readOnlyKey{}).(bool); return v }

// IsReadOnly reports whether ctx may only read ([WithReadOnly]): for
// stores beside the farm service (work events, say) that must refuse the
// same changes with [ErrReadOnly].
func IsReadOnly(ctx context.Context) bool { return readOnly(ctx) }

// WithHolding scopes ctx to the farm (holding) id: a [Service] called with
// it sees, changes and makes only that holding's plots and groups; another
// holding's are [ErrNotFound], as if they did not exist. Without it the
// service sees everything: the app without sign-in, and its background
// jobs.
func WithHolding(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, holdingKey{}, id)
}

// HoldingFrom returns the holding ctx is scoped to, if any.
func HoldingFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(holdingKey{}).(string)
	return id, ok && id != ""
}

// st is the store as ctx may see it.
func (s *Service) st(ctx context.Context) Store {
	st := s.store
	if h, ok := HoldingFrom(ctx); ok {
		st = scopedStore{st, h}
	}
	if readOnly(ctx) {
		st = readOnlyStore{st}
	}
	return st
}

// gs is the group store as ctx may see it; nil without groups.
func (s *Service) gs(ctx context.Context) GroupStore {
	if s.groups == nil {
		return nil
	}
	gs := s.groups
	if h, ok := HoldingFrom(ctx); ok {
		gs = scopedGroups{gs, h}
	}
	if readOnly(ctx) {
		gs = readOnlyGroups{gs}
	}
	return gs
}

// readOnlyStore refuses changes.
type readOnlyStore struct{ Store }

func (readOnlyStore) Create(context.Context, Farm) error   { return ErrReadOnly }
func (readOnlyStore) Update(context.Context, Farm) error   { return ErrReadOnly }
func (readOnlyStore) Delete(context.Context, string) error { return ErrReadOnly }

// readOnlyGroups refuses changes.
type readOnlyGroups struct{ GroupStore }

func (readOnlyGroups) CreateGroup(context.Context, Group) error  { return ErrReadOnly }
func (readOnlyGroups) UpdateGroup(context.Context, Group) error  { return ErrReadOnly }
func (readOnlyGroups) DeleteGroup(context.Context, string) error { return ErrReadOnly }

// Adopt gives every plot and group that belongs to no holding to holding:
// what a farm app used before sign-in keeps, when its first farm is made.
// It returns how many plots it gave.
func (s *Service) Adopt(ctx context.Context, holding string) (int, error) {
	all, err := s.store.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range all {
		if f.Holding == "" {
			f.Holding = holding
			if err := s.store.Update(ctx, f); err != nil {
				return n, err
			}
			n++
		}
	}
	if s.groups != nil {
		s.groupMu.Lock()
		defer s.groupMu.Unlock()
		gs, err := s.groups.ListGroups(ctx)
		if err != nil {
			return n, err
		}
		for _, g := range gs {
			if g.Holding == "" {
				g.Holding = holding
				if err := s.groups.UpdateGroup(ctx, g); err != nil {
					return n, err
				}
			}
		}
	}
	return n, nil
}

// scopedStore is a Store seen from one holding.
type scopedStore struct {
	Store
	holding string
}

func (s scopedStore) Create(ctx context.Context, f Farm) error {
	f.Holding = s.holding
	return s.Store.Create(ctx, f)
}

func (s scopedStore) Get(ctx context.Context, id string) (Farm, error) {
	f, err := s.Store.Get(ctx, id)
	if err == nil && f.Holding != s.holding {
		return Farm{}, fmt.Errorf("%w: farm %s", ErrNotFound, id)
	}
	return f, err
}

func (s scopedStore) List(ctx context.Context) ([]Farm, error) {
	all, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, f := range all {
		if f.Holding == s.holding {
			out = append(out, f)
		}
	}
	return out, nil
}

func (s scopedStore) Update(ctx context.Context, f Farm) error {
	if _, err := s.Get(ctx, f.ID); err != nil {
		return err
	}
	f.Holding = s.holding
	return s.Store.Update(ctx, f)
}

func (s scopedStore) Delete(ctx context.Context, id string) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	return s.Store.Delete(ctx, id)
}

// scopedGroups is a GroupStore seen from one holding.
type scopedGroups struct {
	GroupStore
	holding string
}

func (s scopedGroups) CreateGroup(ctx context.Context, g Group) error {
	g.Holding = s.holding
	return s.GroupStore.CreateGroup(ctx, g)
}

func (s scopedGroups) GetGroup(ctx context.Context, id string) (Group, error) {
	g, err := s.GroupStore.GetGroup(ctx, id)
	if err == nil && g.Holding != s.holding {
		return Group{}, fmt.Errorf("%w: group %s", ErrNotFound, id)
	}
	return g, err
}

func (s scopedGroups) ListGroups(ctx context.Context) ([]Group, error) {
	all, err := s.GroupStore.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, g := range all {
		if g.Holding == s.holding {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s scopedGroups) UpdateGroup(ctx context.Context, g Group) error {
	if _, err := s.GetGroup(ctx, g.ID); err != nil {
		return err
	}
	g.Holding = s.holding
	return s.GroupStore.UpdateGroup(ctx, g)
}

func (s scopedGroups) DeleteGroup(ctx context.Context, id string) error {
	if _, err := s.GetGroup(ctx, id); err != nil {
		return err
	}
	return s.GroupStore.DeleteGroup(ctx, id)
}
