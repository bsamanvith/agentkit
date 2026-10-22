package memory

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

// storedItem is an item and its versions, oldest first.
type storedItem struct {
	item     store.Item
	versions []store.ItemVersion
}

// itemID keys an item within its tenant: a kind cannot hold a NUL.
func itemID(kind, name string) string { return kind + "\x00" + name }

// SaveItem implements [store.ItemStore].
func (s *Store) SaveItem(ctx context.Context, v store.ItemVersion, ifLatest int64) (store.ItemVersion, error) {
	k, err := scope(ctx, itemID(v.Kind, v.Name))
	if err != nil {
		return store.ItemVersion{}, err
	}
	v, err = backend.CheckItemVersion(v, ifLatest)
	if err != nil {
		return store.ItemVersion{}, err
	}
	now := s.now()
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.ItemVersion{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[k]
	var have int64
	if it != nil {
		have = it.item.Latest
	}
	if err := backend.SaveItemError(v.Kind, v.Name, ifLatest, have); err != nil {
		return store.ItemVersion{}, err
	}
	if it == nil {
		it = &storedItem{item: store.Item{Kind: v.Kind, Name: v.Name, CreatedAt: now}}
		s.items[k] = it
	}
	v.Version, v.CreatedAt = have+1, now
	it.item.Latest = v.Version
	it.item.UpdatedAt = later(it.item.UpdatedAt, now)
	it.versions = append(it.versions, v)
	return cloneItemVersion(v), nil
}

// PublishItem implements [store.ItemStore].
func (s *Store) PublishItem(ctx context.Context, kind, name string, version int64) (store.Item, error) {
	k, err := scope(ctx, itemID(kind, name))
	if err != nil {
		return store.Item{}, err
	}
	if err := backend.CheckItemRef(kind, name, version); err != nil {
		return store.Item{}, err
	}
	now := s.now()
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.Item{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[k]
	if it == nil {
		return store.Item{}, backend.ItemVersionNotFound(kind, name, version)
	}
	i := slices.IndexFunc(it.versions, func(v store.ItemVersion) bool { return v.Version == version })
	if i < 0 {
		return store.Item{}, backend.ItemVersionNotFound(kind, name, version)
	}
	if it.item.Published == version {
		return it.item, nil
	}
	if it.versions[i].PublishedAt.IsZero() {
		it.versions[i].PublishedAt = now
	}
	it.item.Published = version
	it.item.UpdatedAt = later(it.item.UpdatedAt, now)
	return it.item, nil
}

// GetItem implements [store.ItemStore].
func (s *Store) GetItem(ctx context.Context, kind, name string) (store.Item, error) {
	k, err := scope(ctx, itemID(kind, name))
	if err != nil {
		return store.Item{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[k]
	if it == nil {
		return store.Item{}, backend.ItemNotFound(kind, name)
	}
	return it.item, nil
}

// ListItems implements [store.ItemStore].
func (s *Store) ListItems(ctx context.Context, kind string) ([]store.Item, error) {
	k, err := scope(ctx, "")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []store.Item{}
	for ik, it := range s.items {
		if ik.tenant == k.tenant && it.item.Kind == kind {
			out = append(out, it.item)
		}
	}
	slices.SortFunc(out, func(a, b store.Item) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// GetItemVersion implements [store.ItemStore].
func (s *Store) GetItemVersion(ctx context.Context, kind, name string, version int64) (store.ItemVersion, error) {
	k, err := scope(ctx, itemID(kind, name))
	if err != nil {
		return store.ItemVersion{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if it := s.items[k]; it != nil {
		if i := slices.IndexFunc(it.versions, func(v store.ItemVersion) bool { return v.Version == version }); i >= 0 {
			return cloneItemVersion(it.versions[i]), nil
		}
	}
	return store.ItemVersion{}, backend.ItemVersionNotFound(kind, name, version)
}

// ListItemVersions implements [store.ItemStore].
func (s *Store) ListItemVersions(ctx context.Context, kind, name string) ([]store.ItemVersion, error) {
	k, err := scope(ctx, itemID(kind, name))
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[k]
	if it == nil {
		return nil, backend.ItemNotFound(kind, name)
	}
	out := make([]store.ItemVersion, len(it.versions))
	for i, v := range it.versions {
		out[i] = cloneItemVersion(v)
	}
	return out, nil
}

// DeleteItemVersion implements [store.ItemStore].
func (s *Store) DeleteItemVersion(ctx context.Context, kind, name string, version int64) error {
	k, err := scope(ctx, itemID(kind, name))
	if err != nil {
		return err
	}
	if err := backend.CheckItemRef(kind, name, version); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[k]
	if it == nil {
		return backend.ItemVersionNotFound(kind, name, version)
	}
	i := slices.IndexFunc(it.versions, func(v store.ItemVersion) bool { return v.Version == version })
	switch {
	case i < 0:
		return backend.ItemVersionNotFound(kind, name, version)
	case !it.versions[i].PublishedAt.IsZero():
		return backend.ItemVersionPublished(kind, name, version)
	}
	it.versions = slices.Delete(it.versions, i, i+1)
	if len(it.versions) == 0 {
		delete(s.items, k)
	}
	return nil
}

func cloneItemVersion(v store.ItemVersion) store.ItemVersion {
	v.Data = slices.Clone(v.Data)
	return v
}

// later is the later of two times: an item's times never go backwards.
func later(a, b time.Time) time.Time {
	if b.Before(a) {
		return a
	}
	return b
}
