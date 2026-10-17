// Package memory is the reference [credentials.BlobStore]: in process, gone
// when the process ends. It passes credentialstest's suites; every other
// backend behaves as it does.
package memory

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Store is an in-memory blob store.
type Store struct {
	mu    sync.Mutex
	blobs map[key]credentials.Blob
	last  map[key]int64 // the last version each key had, kept after a delete
}

type key struct{ tenant, user, slot string }

var _ credentials.BlobStore = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{blobs: map[key]credentials.Blob{}, last: map[key]int64{}}
}

func keyOf(ctx context.Context, user, slot string) (key, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return key{}, err
	}
	return key{t.ID(), user, slot}, nil
}

// Get implements [credentials.BlobStore].
func (s *Store) Get(ctx context.Context, user, slot string) (credentials.Blob, error) {
	k, err := keyOf(ctx, user, slot)
	if err != nil {
		return credentials.Blob{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.blobs[k]
	if !ok {
		return credentials.Blob{}, fmt.Errorf("credential %q: %w", slot, credentials.ErrNotFound)
	}
	b.Data = bytes.Clone(b.Data)
	return b, nil
}

// Put implements [credentials.BlobStore].
func (s *Store) Put(ctx context.Context, user, slot string, b credentials.Blob, ifVersion int64) (credentials.Meta, error) {
	k, err := keyOf(ctx, user, slot)
	if err != nil {
		return credentials.Meta{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, exists := s.blobs[k]
	switch {
	case ifVersion == 0 && exists:
		return credentials.Meta{}, fmt.Errorf("credential %q: %w", slot, credentials.ErrExists)
	case ifVersion != 0 && (!exists || cur.Version != ifVersion):
		return credentials.Meta{}, fmt.Errorf("credential %q: version %d: %w", slot, ifVersion, credentials.ErrConflict)
	}
	b.User, b.Slot, b.NeedsReconnect = user, slot, false
	b.Version = s.last[k] + 1
	b.CreatedAt = b.UpdatedAt
	if exists {
		b.CreatedAt = cur.CreatedAt
	}
	if b.UpdatedAt.Before(b.CreatedAt) {
		b.UpdatedAt = b.CreatedAt
	}
	b.Data = bytes.Clone(b.Data)
	s.blobs[k], s.last[k] = b, b.Version
	return b.Meta, nil
}

// Delete implements [credentials.BlobStore].
func (s *Store) Delete(ctx context.Context, user, slot string, ifVersion int64) error {
	k, err := keyOf(ctx, user, slot)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, exists := s.blobs[k]
	switch {
	case !exists:
		return fmt.Errorf("credential %q: %w", slot, credentials.ErrNotFound)
	case ifVersion != 0 && cur.Version != ifVersion:
		return fmt.Errorf("credential %q: version %d: %w", slot, ifVersion, credentials.ErrConflict)
	}
	delete(s.blobs, k)
	return nil
}

// List implements [credentials.BlobStore].
func (s *Store) List(ctx context.Context, user string) ([]credentials.Meta, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []credentials.Meta
	for k, b := range s.blobs {
		if k.tenant == t.ID() && k.user == user {
			out = append(out, b.Meta)
		}
	}
	slices.SortFunc(out, func(a, b credentials.Meta) int { return strings.Compare(a.Slot, b.Slot) })
	return out, nil
}
