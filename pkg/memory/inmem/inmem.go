// Package inmem is the reference [memory.Store]: files in a map, lost when
// the process ends. It is for tests, demos and single-process agents whose
// memories may be lost on a restart.
package inmem

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/fileops"
)

var _ memory.Store = (*Store)(nil)

// Store is an in-memory memory.Store. One lock serializes its commands.
type Store struct {
	cfg    memory.Config
	mu     sync.Mutex
	scopes map[memory.Owner]map[string]string // path -> text
}

// New returns an empty store, or memory.ErrInvalid for a bad cfg.
func New(cfg memory.Config) (*Store, error) {
	cfg, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	return &Store{cfg: cfg, scopes: map[memory.Owner]map[string]string{}}, nil
}

// files is one scope's map as fileops.Files.
type files struct {
	m     map[string]string
	bytes int64
}

func (f *files) Get(path string) (string, bool, error) {
	t, ok := f.m[path]
	return t, ok, nil
}

func (f *files) Under(dir string) ([]fileops.File, error) {
	var out []fileops.File
	for p, t := range f.m {
		if strings.HasPrefix(p, dir+"/") {
			out = append(out, fileops.File{Path: p, Size: int64(len(t))})
		}
	}
	slices.SortFunc(out, func(a, b fileops.File) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

func (f *files) Usage() (int64, int, error) { return f.bytes, len(f.m), nil }

func (f *files) Put(path, text string) error {
	f.bytes += int64(len(text)) - int64(len(f.m[path]))
	f.m[path] = text
	return nil
}

func (f *files) Remove(path string) error {
	f.bytes -= int64(len(f.m[path]))
	delete(f.m, path)
	return nil
}

// do runs fn on the scope of ctx with each path cleaned, under the lock.
func (s *Store) do(ctx context.Context, paths []string, fn func(*files, []string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	owner, err := memory.OwnerOf(ctx, s.cfg.Scope)
	if err != nil {
		return err
	}
	clean := make([]string, len(paths))
	for i, p := range paths {
		if clean[i], err = memory.CleanPath(p); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.scopes[owner]
	if m == nil {
		m = map[string]string{}
	}
	f := &files{m: m}
	for _, t := range m {
		f.bytes += int64(len(t))
	}
	if err := fn(f, clean); err != nil {
		return err
	}
	if len(m) == 0 {
		delete(s.scopes, owner)
	} else {
		s.scopes[owner] = m
	}
	return nil
}

// View implements memory.Store.
func (s *Store) View(ctx context.Context, path string) (memory.View, error) {
	var v memory.View
	err := s.do(ctx, []string{path}, func(f *files, p []string) (err error) {
		v, err = fileops.View(f, p[0])
		return err
	})
	return v, err
}

// Create implements memory.Store.
func (s *Store) Create(ctx context.Context, path, text string) error {
	return s.do(ctx, []string{path}, func(f *files, p []string) error {
		return fileops.Create(f, s.cfg.Limits, p[0], text)
	})
}

// StrReplace implements memory.Store.
func (s *Store) StrReplace(ctx context.Context, path, old, new string) error {
	return s.do(ctx, []string{path}, func(f *files, p []string) error {
		return fileops.StrReplace(f, s.cfg.Limits, p[0], old, new)
	})
}

// Insert implements memory.Store.
func (s *Store) Insert(ctx context.Context, path string, line int, text string) error {
	return s.do(ctx, []string{path}, func(f *files, p []string) error {
		return fileops.Insert(f, s.cfg.Limits, p[0], line, text)
	})
}

// Delete implements memory.Store.
func (s *Store) Delete(ctx context.Context, path string) error {
	return s.do(ctx, []string{path}, func(f *files, p []string) error {
		return fileops.Delete(f, p[0])
	})
}

// Rename implements memory.Store.
func (s *Store) Rename(ctx context.Context, from, to string) error {
	return s.do(ctx, []string{from, to}, func(f *files, p []string) error {
		return fileops.Rename(f, p[0], p[1])
	})
}
