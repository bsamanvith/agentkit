package storetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// RunToolsetStore runs the conformance suite for a [store.ToolsetStore].
// newStore must return an empty store that reads times from clk (a fake
// clock the suite advances).
//
// Every test goes through one helper that keeps what the store should hold
// and, when the test ends, checks that every tenant's ListToolsets and
// GetToolset return exactly that.
func RunToolsetStore(t *testing.T, newStore func(t *testing.T, clk clock.Clock) store.ToolsetStore) {
	t.Helper()

	t.Run("CreateGetList", func(t *testing.T) {
		t.Parallel()
		r := newToolsetRig(t, newStore)
		ctx := r.ctx(tenantA)
		// Data is kept byte for byte: whitespace, key order, number literals.
		data := json.RawMessage(" { \"z\": 1.50, \"a\" : \"<&>é\" }\n")
		for _, name := range []string{"linear", "Github_2", "a-b"} {
			got := r.mustPut(ctx, store.ToolsetRecord{Name: name, Data: data, Version: 7}, 0)
			if got.Version != 1 {
				t.Errorf("%s: created at version %d, want 1 (the version passed is ignored)", name, got.Version)
			}
			if !bytes.Equal(got.Data, data) {
				t.Errorf("%s: data %q, want %q", name, got.Data, data)
			}
			if !got.CreatedAt.Equal(r.clk.Now()) || !got.UpdatedAt.Equal(r.clk.Now()) {
				t.Errorf("%s: created %v, updated %v; want both the clock's %v", name, got.CreatedAt, got.UpdatedAt, r.clk.Now())
			}
			r.clk.Advance(time.Second)
		}
		names := toolsetNames(r.list(ctx))
		if want := []string{"Github_2", "a-b", "linear"}; !slices.Equal(names, want) {
			t.Errorf("ListToolsets names %v, want %v (sorted by name)", names, want)
		}
		if _, err := r.s.PutToolset(ctx, store.ToolsetRecord{Name: "linear", Data: data}, 0); !errors.Is(err, store.ErrConflict) {
			t.Errorf("creating a toolset that exists: %v, want ErrConflict", err)
		}
		if _, err := r.s.GetToolset(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetToolset of none: %v, want ErrNotFound", err)
		}
		if got := r.list(r.ctx(tenantB)); len(got) != 0 {
			t.Errorf("another tenant lists %v", toolsetNames(got))
		}
	})

	t.Run("EmptyList", func(t *testing.T) {
		t.Parallel()
		r := newToolsetRig(t, newStore)
		got, err := r.s.ListToolsets(r.ctx(tenantA))
		if err != nil || len(got) != 0 {
			t.Errorf("ListToolsets of an empty store = %v, %v", got, err)
		}
	})

	t.Run("ReplaceIsCompareAndSwap", func(t *testing.T) {
		t.Parallel()
		r := newToolsetRig(t, newStore)
		ctx := r.ctx(tenantA)
		v1 := r.mustPut(ctx, toolsetN("t", 1), 0)
		r.clk.Advance(time.Minute)
		v2 := r.mustPut(ctx, toolsetN("t", 2), 1)
		if v2.Version != 2 || !v2.CreatedAt.Equal(v1.CreatedAt) || !v2.UpdatedAt.Equal(r.clk.Now()) {
			t.Errorf("replaced: version %d, created %v, updated %v; want 2, %v, %v", v2.Version, v2.CreatedAt, v2.UpdatedAt, v1.CreatedAt, r.clk.Now())
		}
		for _, stale := range []int64{1, 3} {
			if _, err := r.s.PutToolset(ctx, toolsetN("t", 9), stale); !errors.Is(err, store.ErrConflict) {
				t.Errorf("replacing version %d of a toolset at 2: %v, want ErrConflict", stale, err)
			}
		}
		if _, err := r.s.PutToolset(ctx, toolsetN("none", 1), 1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("replacing a toolset that does not exist: %v, want ErrNotFound", err)
		}
		// A clock that went back does not move UpdatedAt back.
		r.clk.skew(-time.Hour)
		v3 := r.mustPut(ctx, toolsetN("t", 3), 2)
		if v3.UpdatedAt.Before(v2.UpdatedAt) {
			t.Errorf("updated %v after a clock went back, before the previous %v", v3.UpdatedAt, v2.UpdatedAt)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		t.Parallel()
		r := newToolsetRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustPut(ctx, toolsetN("a", 1), 0)
		r.mustPut(ctx, toolsetN("a", 2), 1)
		r.mustPut(ctx, toolsetN("b", 1), 0)
		if err := r.s.DeleteToolset(ctx, "a", 1); !errors.Is(err, store.ErrConflict) {
			t.Errorf("deleting version 1 of a toolset at 2: %v, want ErrConflict", err)
		}
		r.mustDelete(ctx, "a", 2)
		r.mustDelete(ctx, "b", 0)
		for _, name := range []string{"a", "b", "c"} {
			if err := r.s.DeleteToolset(ctx, name, 0); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("deleting %s, which does not exist: %v, want ErrNotFound", name, err)
			}
		}
		// The name is free again.
		if got := r.mustPut(ctx, toolsetN("a", 3), 0); got.Version < 1 {
			t.Errorf("recreated at version %d", got.Version)
		}
	})

	t.Run("Tenants", func(t *testing.T) {
		t.Parallel()
		r := newToolsetRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)
		r.mustPut(a, toolsetN("shared", 1), 0)
		r.mustPut(b, toolsetN("shared", 2), 0)
		r.mustPut(b, toolsetN("own", 1), 0)
		if _, err := r.s.GetToolset(a, "own"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("reading another tenant's toolset: %v, want ErrNotFound", err)
		}
		if err := r.s.DeleteToolset(a, "own", 0); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("deleting another tenant's toolset: %v, want ErrNotFound", err)
		}
		if _, err := r.s.PutToolset(a, toolsetN("own", 3), 1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("replacing another tenant's toolset: %v, want ErrNotFound", err)
		}
		r.mustPut(a, toolsetN("shared", 4), 1)
		r.mustDelete(b, "shared", 1)
	})

	t.Run("Invalid", func(t *testing.T) {
		t.Parallel()
		r := newToolsetRig(t, newStore)
		ctx := r.ctx(tenantA)
		big := json.RawMessage(`"` + strings.Repeat("x", store.MaxToolsetBytes-1) + `"`)
		for _, c := range []struct {
			name      string
			rec       store.ToolsetRecord
			ifVersion int64
		}{
			{"empty name", store.ToolsetRecord{Data: json.RawMessage(`{}`)}, 0},
			{"name with a slash", store.ToolsetRecord{Name: "a/b", Data: json.RawMessage(`{}`)}, 0},
			{"name too long", store.ToolsetRecord{Name: strings.Repeat("a", 65), Data: json.RawMessage(`{}`)}, 0},
			{"no data", store.ToolsetRecord{Name: "a"}, 0},
			{"data not JSON", store.ToolsetRecord{Name: "a", Data: json.RawMessage(`{`)}, 0},
			{"data not UTF-8", store.ToolsetRecord{Name: "a", Data: json.RawMessage("\"\xff\"")}, 0},
			{"data with NUL", store.ToolsetRecord{Name: "a", Data: json.RawMessage("\"\x00\"")}, 0},
			{"data too big", store.ToolsetRecord{Name: "a", Data: big}, 0},
			{"negative version", store.ToolsetRecord{Name: "a", Data: json.RawMessage(`{}`)}, -1},
		} {
			if _, err := r.s.PutToolset(ctx, c.rec, c.ifVersion); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("PutToolset with %s: %v, want ErrInvalid", c.name, err)
			}
		}
		if err := r.s.DeleteToolset(ctx, "a", -1); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("DeleteToolset with a negative version: %v, want ErrInvalid", err)
		}
		// The largest data is kept.
		r.mustPut(ctx, store.ToolsetRecord{Name: "big", Data: json.RawMessage(`"` + strings.Repeat("x", store.MaxToolsetBytes-2) + `"`)}, 0)
	})

	t.Run("NeedsATenant", func(t *testing.T) {
		t.Parallel()
		s := newStore(t, clocktest.NewFake(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)))
		ctx := t.Context()
		_, err1 := s.PutToolset(ctx, toolsetN("a", 1), 0)
		_, err2 := s.GetToolset(ctx, "a")
		_, err3 := s.ListToolsets(ctx)
		err4 := s.DeleteToolset(ctx, "a", 0)
		for i, err := range []error{err1, err2, err3, err4} {
			if !errors.Is(err, tenancy.ErrNoTenant) {
				t.Errorf("call %d with no tenant: %v, want ErrNoTenant", i+1, err)
			}
		}
		done, cancel := context.WithCancel(tenantCtx(ctx, t, tenantA))
		cancel()
		if _, err := s.PutToolset(done, toolsetN("a", 1), 0); !errors.Is(err, context.Canceled) {
			t.Errorf("PutToolset on a done context: %v, want context.Canceled", err)
		}
		if _, err := s.ListToolsets(done); !errors.Is(err, context.Canceled) {
			t.Errorf("ListToolsets on a done context: %v, want context.Canceled", err)
		}
	})

	t.Run("NoSharedMemory", func(t *testing.T) {
		t.Parallel()
		r := newToolsetRig(t, newStore)
		ctx := r.ctx(tenantA)
		rec := toolsetN("a", 1)
		got := r.mustPut(ctx, rec, 0)
		rec.Data[2] = 'X'
		got.Data[2] = 'Y'
		g, err := r.s.GetToolset(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		g.Data[2] = 'Z'
		l := r.list(ctx)
		l[0].Data[2] = 'W'
		// The rig's check reads the toolset again and compares it with
		// toolsetN("a", 1).
	})

	t.Run("ConcurrentReplaces", func(t *testing.T) {
		t.Parallel()
		r := newToolsetRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustPut(ctx, toolsetN("a", 0), 0)
		const n = 8
		var wg sync.WaitGroup
		var mu sync.Mutex
		won := 0
		for i := range n {
			wg.Go(func() {
				got, err := r.s.PutToolset(ctx, toolsetN("a", i+1), 1)
				switch {
				case err == nil:
					mu.Lock()
					won++
					mu.Unlock()
					r.record(tenantA, got)
				case !errors.Is(err, store.ErrConflict):
					t.Errorf("replace %d: %v, want nil or ErrConflict", i, err)
				}
			})
		}
		wg.Wait()
		if won != 1 {
			t.Errorf("%d of %d replaces of version 1 succeeded, want exactly 1", won, n)
		}
		// Concurrent creates of one name: exactly one wins too.
		won = 0
		for i := range n {
			wg.Go(func() {
				got, err := r.s.PutToolset(ctx, toolsetN("new", i), 0)
				switch {
				case err == nil:
					mu.Lock()
					won++
					mu.Unlock()
					r.record(tenantA, got)
				case !errors.Is(err, store.ErrConflict):
					t.Errorf("create %d: %v, want nil or ErrConflict", i, err)
				}
			})
		}
		wg.Wait()
		if won != 1 {
			t.Errorf("%d of %d creates of one name succeeded, want exactly 1", won, n)
		}
	})
}

// toolsetRig keeps what a store under test should hold: per tenant, each
// toolset as the store last returned it.
type toolsetRig struct {
	t   *testing.T
	s   store.ToolsetStore
	clk *skewedClock

	mu   sync.Mutex
	want map[string]map[string]store.ToolsetRecord // tenant → name → record
}

func newToolsetRig(t *testing.T, newStore func(t *testing.T, clk clock.Clock) store.ToolsetStore) *toolsetRig {
	t.Helper()
	clk := &skewedClock{Fake: clocktest.NewFake(time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC))}
	r := &toolsetRig{t: t, s: newStore(t, clk), clk: clk, want: map[string]map[string]store.ToolsetRecord{unusedTenant: {}}}
	t.Cleanup(r.check)
	return r
}

func (r *toolsetRig) ctx(tenant string) context.Context {
	r.mu.Lock()
	if r.want[tenant] == nil {
		r.want[tenant] = map[string]store.ToolsetRecord{}
	}
	r.mu.Unlock()
	return tenantCtx(r.t.Context(), r.t, tenant)
}

func (r *toolsetRig) record(tenant string, t store.ToolsetRecord) {
	t.Data = slices.Clone(t.Data)
	r.mu.Lock()
	r.want[tenant][t.Name] = t
	r.mu.Unlock()
}

func (r *toolsetRig) mustPut(ctx context.Context, t store.ToolsetRecord, ifVersion int64) store.ToolsetRecord {
	r.t.Helper()
	sent := slices.Clone(t.Data)
	got, err := r.s.PutToolset(ctx, t, ifVersion)
	if err != nil {
		r.t.Fatalf("PutToolset(%q, %d): %v", t.Name, ifVersion, err)
	}
	if got.Name != t.Name || !bytes.Equal(got.Data, sent) {
		r.t.Errorf("PutToolset returned %q %q, want %q %q", got.Name, got.Data, t.Name, sent)
	}
	if ifVersion > 0 && got.Version != ifVersion+1 {
		r.t.Errorf("replacing version %d stored version %d, want %d", ifVersion, got.Version, ifVersion+1)
	}
	tn, _ := tenancy.From(ctx)
	r.record(tn.ID(), got)
	return got
}

func (r *toolsetRig) mustDelete(ctx context.Context, name string, ifVersion int64) {
	r.t.Helper()
	if err := r.s.DeleteToolset(ctx, name, ifVersion); err != nil {
		r.t.Fatalf("DeleteToolset(%q, %d): %v", name, ifVersion, err)
	}
	tn, _ := tenancy.From(ctx)
	r.mu.Lock()
	delete(r.want[tn.ID()], name)
	r.mu.Unlock()
}

func (r *toolsetRig) list(ctx context.Context) []store.ToolsetRecord {
	r.t.Helper()
	got, err := r.s.ListToolsets(ctx)
	if err != nil {
		r.t.Fatalf("ListToolsets: %v", err)
	}
	return got
}

// check compares every tenant's toolsets with what the store returned
// when they were last written.
func (r *toolsetRig) check() {
	ctx := context.Background()
	r.mu.Lock()
	defer r.mu.Unlock()
	for tenant, want := range r.want {
		tctx := tenantCtx(ctx, r.t, tenant)
		got, err := r.s.ListToolsets(tctx)
		if err != nil {
			r.t.Errorf("tenant %s: ListToolsets: %v", tenant, err)
			continue
		}
		var names []string
		for name := range want {
			names = append(names, name)
		}
		slices.Sort(names)
		if g := toolsetNames(got); !slices.Equal(g, names) {
			r.t.Errorf("tenant %s lists toolsets %v, want %v", tenant, g, names)
			continue
		}
		for _, g := range got {
			if err := sameToolset(g, want[g.Name]); err != nil {
				r.t.Errorf("tenant %s: ListToolsets: %v", tenant, err)
			}
			one, err := r.s.GetToolset(tctx, g.Name)
			if err != nil {
				r.t.Errorf("tenant %s: GetToolset(%q): %v", tenant, g.Name, err)
			} else if err := sameToolset(one, want[g.Name]); err != nil {
				r.t.Errorf("tenant %s: GetToolset: %v", tenant, err)
			}
		}
	}
}

func sameToolset(got, want store.ToolsetRecord) error {
	if got.Name != want.Name || got.Version != want.Version || !bytes.Equal(got.Data, want.Data) ||
		!got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		return fmt.Errorf("toolset %q is %d %q %v %v, want %d %q %v %v", want.Name,
			got.Version, got.Data, got.CreatedAt, got.UpdatedAt, want.Version, want.Data, want.CreatedAt, want.UpdatedAt)
	}
	return nil
}

func toolsetN(name string, i int) store.ToolsetRecord {
	return store.ToolsetRecord{Name: name, Data: json.RawMessage(fmt.Sprintf(`{"url": "https://mcp.example.com/%d", "n": 1.50}`, i))}
}

func toolsetNames(ts []store.ToolsetRecord) []string {
	names := []string{}
	for _, t := range ts {
		names = append(names, t.Name)
	}
	return names
}
