// Package memorytest is the conformance suite of memory.Store: every
// backend runs [RunStore] with one line.
//
// After every command a test makes, the suite checks the scope's invariants
// ([CheckState]): every listed file reads back with its size, a directory's
// size is the sum of its files, a listing goes two levels deep and no
// further, and no limit is passed. A command that fails must leave the scope
// exactly as it was, and its error must be justified by the state before it
// (a "not found" path was not there, a "limit" write would have passed one).
// A seeded simulation runs random commands, valid and not, through the same
// checks, and a concurrent one checks that no edit is lost.
package memorytest

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// NewStore makes an empty store with cfg for one test.
type NewStore func(t *testing.T, cfg memory.Config) memory.Store

// RunStore runs the conformance suite against the stores newStore makes.
func RunStore(t *testing.T, newStore NewStore) {
	t.Helper()
	t.Run("Commands", func(t *testing.T) { t.Parallel(); testCommands(t, newStore) })
	t.Run("Directories", func(t *testing.T) { t.Parallel(); testDirectories(t, newStore) })
	t.Run("Isolation", func(t *testing.T) { t.Parallel(); testIsolation(t, newStore) })
	t.Run("PathRules", func(t *testing.T) { t.Parallel(); testPathRules(t, newStore) })
	t.Run("Limits", func(t *testing.T) { t.Parallel(); testLimits(t, newStore) })
	t.Run("Canceled", func(t *testing.T) { t.Parallel(); testCanceled(t, newStore) })
	t.Run("Simulation", func(t *testing.T) { t.Parallel(); testSimulation(t, newStore) })
	t.Run("Concurrent", func(t *testing.T) { t.Parallel(); testConcurrent(t, newStore) })
}

// Ctx returns a context of tenant (and user, if not empty).
func Ctx(t testing.TB, tenant, user string) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant(tenant)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenancy.With(t.Context(), tn)
	if user != "" {
		ctx = tenancy.WithUser(ctx, user)
	}
	return ctx
}

// Snapshot returns every file of the scope of ctx, by path, walking the
// listings down from memory.Root.
func Snapshot(ctx context.Context, s memory.Store) (map[string]string, error) {
	files := map[string]string{}
	seen := map[string]bool{}
	dirs := []string{memory.Root}
	for len(dirs) > 0 {
		dir := dirs[0]
		dirs = dirs[1:]
		if seen[dir] {
			continue
		}
		seen[dir] = true
		v, err := s.View(ctx, dir)
		if err != nil {
			return nil, err
		}
		for _, e := range v.Entries {
			if e.Dir {
				dirs = append(dirs, e.Path)
				continue
			}
			if _, ok := files[e.Path]; ok {
				continue
			}
			f, err := s.View(ctx, e.Path)
			if err != nil {
				return nil, err
			}
			files[e.Path] = f.Text
		}
	}
	return files, nil
}

// CheckState checks the invariants of the scope of ctx against its files,
// and returns them.
func CheckState(ctx context.Context, t testing.TB, s memory.Store, l memory.Limits) map[string]string {
	t.Helper()
	files, err := Snapshot(ctx, s)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var total int64
	for p, text := range files {
		total += int64(len(text))
		v, err := s.View(ctx, p)
		if err != nil || v.Dir || v.Text != text || v.Size != int64(len(text)) || v.Path != p || len(v.Entries) != 0 {
			t.Errorf("View(%s) = %+v, %v; want the file's text %q", p, v, err, text)
		}
		if int64(len(text)) > l.MaxFileBytes {
			t.Errorf("%s holds %d bytes, past the file limit %d", p, len(text), l.MaxFileBytes)
		}
		if c, err := memory.CleanPath(p); err != nil || c != p {
			t.Errorf("stored path %q is not clean: %q, %v", p, c, err)
		}
		for _, d := range memory.Parents(p) {
			if _, ok := files[d]; ok {
				t.Errorf("%s is under %s, which is a file", p, d)
			}
		}
	}
	if total > l.MaxScopeBytes {
		t.Errorf("scope holds %d bytes, past the limit %d", total, l.MaxScopeBytes)
	}
	if len(files) > l.MaxFiles {
		t.Errorf("scope holds %d files, past the limit %d", len(files), l.MaxFiles)
	}
	// Every directory's listing, the root's included.
	dirs := map[string]bool{memory.Root: true}
	for p := range files {
		for _, d := range memory.Parents(p) {
			dirs[d] = true
		}
	}
	for d := range dirs {
		checkListing(ctx, t, s, d, files)
	}
	return files
}

// checkListing checks the view of directory dir against files.
func checkListing(ctx context.Context, t testing.TB, s memory.Store, dir string, files map[string]string) {
	t.Helper()
	v, err := s.View(ctx, dir)
	if err != nil {
		t.Errorf("View(%s): %v", dir, err)
		return
	}
	want := map[string]memory.Entry{}
	var size int64
	for p, text := range files {
		if !strings.HasPrefix(p, dir+"/") {
			continue
		}
		n := int64(len(text))
		size += n
		rel := strings.Split(strings.TrimPrefix(p, dir+"/"), "/")
		for i := 1; i < len(rel) && i <= 2; i++ {
			d := dir + "/" + strings.Join(rel[:i], "/")
			e := want[d]
			want[d] = memory.Entry{Path: d, Dir: true, Size: e.Size + n}
		}
		if len(rel) <= 2 {
			want[p] = memory.Entry{Path: p, Size: n}
		}
	}
	wantEntries := slices.SortedFunc(maps.Values(want), func(a, b memory.Entry) int { return strings.Compare(a.Path, b.Path) })
	if !v.Dir || v.Path != dir || v.Size != size || v.Text != "" || !slices.Equal(v.Entries, wantEntries) {
		t.Errorf("View(%s) = %+v\nwant a directory of %d bytes with %+v", dir, v, size, wantEntries)
	}
}

// same reports whether two snapshots are equal.
func same(a, b map[string]string) bool { return maps.Equal(a, b) }

// is reports whether err matches any of targets.
func is(err error, targets ...error) bool {
	for _, t := range targets {
		if errors.Is(err, t) {
			return true
		}
	}
	return false
}
