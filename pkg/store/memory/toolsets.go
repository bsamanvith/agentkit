package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

// PutToolset implements [store.ToolsetStore].
func (s *Store) PutToolset(ctx context.Context, t store.ToolsetRecord, ifVersion int64) (store.ToolsetRecord, error) {
	k, err := scope(ctx, t.Name)
	if err != nil {
		return store.ToolsetRecord{}, err
	}
	t, err = backend.CheckToolset(t, ifVersion)
	if err != nil {
		return store.ToolsetRecord{}, err
	}
	now := s.now()
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.ToolsetRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.toolsets[k]
	if err := backend.PutToolsetError(t.Name, ifVersion, old.Version); err != nil {
		return store.ToolsetRecord{}, err
	}
	t.Version, t.CreatedAt, t.UpdatedAt = old.Version+1, now, now
	if ok {
		t.CreatedAt = old.CreatedAt
		if now.Before(old.UpdatedAt) {
			t.UpdatedAt = old.UpdatedAt
		}
	}
	s.toolsets[k] = t
	return cloneToolset(t), nil
}

// GetToolset implements [store.ToolsetStore].
func (s *Store) GetToolset(ctx context.Context, name string) (store.ToolsetRecord, error) {
	k, err := scope(ctx, name)
	if err != nil {
		return store.ToolsetRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.toolsets[k]
	if !ok {
		return store.ToolsetRecord{}, fmt.Errorf("%w: toolset %q", store.ErrNotFound, name)
	}
	return cloneToolset(t), nil
}

// ListToolsets implements [store.ToolsetStore].
func (s *Store) ListToolsets(ctx context.Context) ([]store.ToolsetRecord, error) {
	k, err := scope(ctx, "")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []store.ToolsetRecord{}
	for tk, t := range s.toolsets {
		if tk.tenant == k.tenant {
			out = append(out, cloneToolset(t))
		}
	}
	slices.SortFunc(out, func(a, b store.ToolsetRecord) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// DeleteToolset implements [store.ToolsetStore].
func (s *Store) DeleteToolset(ctx context.Context, name string, ifVersion int64) error {
	k, err := scope(ctx, name)
	if err != nil {
		return err
	}
	if err := backend.CheckDeleteToolset(name, ifVersion); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := backend.DeleteToolsetError(name, ifVersion, s.toolsets[k].Version); err != nil {
		return err
	}
	delete(s.toolsets, k)
	return nil
}

func cloneToolset(t store.ToolsetRecord) store.ToolsetRecord {
	t.Data = slices.Clone(t.Data)
	return t
}
