package storetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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

// RunItemStore runs the conformance suite for a [store.ItemStore]. newStore
// must return an empty store that reads times from clk (a fake clock the
// suite advances).
//
// Every test goes through one helper that keeps what the store should hold
// and, when the test ends, checks that every tenant's items and versions
// read back exactly so, through each of the reading methods: an item's
// Latest is the highest number it gave, Published names one of its versions
// (or is 0), exactly the versions ever published have a PublishedAt, and
// versions never change otherwise.
func RunItemStore(t *testing.T, newStore func(t *testing.T, clk clock.Clock) store.ItemStore) {
	t.Helper()

	t.Run("SaveCreatesVersions", func(t *testing.T) {
		t.Parallel()
		r := newItemRig(t, newStore)
		ctx := r.ctx(tenantA)
		// Data is kept byte for byte: whitespace, key order, number literals.
		data := json.RawMessage(" { \"z\": 1.50, \"a\" : \"<&>é\" }\n")
		v1 := r.mustSave(ctx, store.ItemVersion{Kind: "agent", Name: "support", Data: data, Author: "key-1", Message: "first é", Version: 9}, 0)
		if v1.Version != 1 || !bytes.Equal(v1.Data, data) || v1.Author != "key-1" || v1.Message != "first é" ||
			!v1.CreatedAt.Equal(r.clk.Now()) || !v1.PublishedAt.IsZero() {
			t.Errorf("first save = %+v; want version 1 (the version passed is ignored), the data, author and message, created now, never published", v1)
		}
		r.clk.Advance(time.Minute)
		v2 := r.mustSave(ctx, itemN("agent", "support", 2), 1)
		if v2.Version != 2 {
			t.Errorf("second save is version %d, want 2", v2.Version)
		}
		it := r.mustGet(ctx, "agent", "support")
		if it.Latest != 2 || it.Published != 0 || !it.CreatedAt.Equal(v1.CreatedAt) || !it.UpdatedAt.Equal(r.clk.Now()) {
			t.Errorf("item = %+v; want latest 2, none published, created at the first save, updated now", it)
		}
		if _, err := r.s.SaveItem(ctx, itemN("agent", "support", 3), 0); !errors.Is(err, store.ErrConflict) {
			t.Errorf("creating an item that exists: %v, want ErrConflict", err)
		}
		for _, stale := range []int64{1, 3} {
			if _, err := r.s.SaveItem(ctx, itemN("agent", "support", 3), stale); !errors.Is(err, store.ErrConflict) {
				t.Errorf("saving on version %d of an item at 2: %v, want ErrConflict", stale, err)
			}
		}
		if _, err := r.s.SaveItem(ctx, itemN("agent", "none", 1), 1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("saving on an item that does not exist: %v, want ErrNotFound", err)
		}
		if _, err := r.s.GetItem(ctx, "agent", "none"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetItem of none: %v, want ErrNotFound", err)
		}
		if _, err := r.s.ListItemVersions(ctx, "agent", "none"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("ListItemVersions of none: %v, want ErrNotFound", err)
		}
		if _, err := r.s.GetItemVersion(ctx, "agent", "support", 3); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetItemVersion of a version not saved: %v, want ErrNotFound", err)
		}
		// A clock that went back does not move UpdatedAt back.
		r.clk.skew(-time.Hour)
		r.mustSave(ctx, itemN("agent", "support", 3), 2)
		if got := r.mustGet(ctx, "agent", "support"); got.UpdatedAt.Before(it.UpdatedAt) {
			t.Errorf("updated %v after a clock went back, before the previous %v", got.UpdatedAt, it.UpdatedAt)
		}
	})

	t.Run("PublishAndRollBack", func(t *testing.T) {
		t.Parallel()
		r := newItemRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustSave(ctx, itemN("agent", "a", 1), 0)
		r.mustSave(ctx, itemN("agent", "a", 2), 1)
		r.clk.Advance(time.Second)
		first := r.clk.Now()
		if it := r.mustPublish(ctx, "agent", "a", 1); it.Published != 1 || !it.UpdatedAt.Equal(first) {
			t.Errorf("published 1: %+v", it)
		}
		r.clk.Advance(time.Second)
		if it := r.mustPublish(ctx, "agent", "a", 2); it.Published != 2 || it.Latest != 2 {
			t.Errorf("published 2: %+v", it)
		}
		r.clk.Advance(time.Second)
		// Rolling back is publishing 1 again: its first publication stays.
		if it := r.mustPublish(ctx, "agent", "a", 1); it.Published != 1 {
			t.Errorf("rolled back: %+v", it)
		}
		if v, err := r.s.GetItemVersion(ctx, "agent", "a", 1); err != nil || !v.PublishedAt.Equal(first) {
			t.Errorf("version 1 after a second publication = %+v, %v; want its first publication %v", v, err, first)
		}
		// Publishing the published version again changes nothing.
		r.clk.Advance(time.Second)
		before := r.mustGet(ctx, "agent", "a")
		if it := r.mustPublish(ctx, "agent", "a", 1); !sameItem(it, before) {
			t.Errorf("publishing the published version again = %+v, want %+v unchanged", it, before)
		}
		for _, v := range []int64{3, 99} {
			if _, err := r.s.PublishItem(ctx, "agent", "a", v); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("publishing version %d, not saved: %v, want ErrNotFound", v, err)
			}
		}
		if _, err := r.s.PublishItem(ctx, "agent", "none", 1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("publishing an item that does not exist: %v, want ErrNotFound", err)
		}
	})

	t.Run("DeleteDrafts", func(t *testing.T) {
		t.Parallel()
		r := newItemRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustSave(ctx, itemN("agent", "a", 1), 0)
		r.mustSave(ctx, itemN("agent", "a", 2), 1)
		r.mustSave(ctx, itemN("agent", "a", 3), 2)
		r.mustPublish(ctx, "agent", "a", 1)
		r.mustPublish(ctx, "agent", "a", 2)
		r.mustPublish(ctx, "agent", "a", 1)
		for _, v := range []int64{1, 2} { // published now, and published once
			if err := r.s.DeleteItemVersion(ctx, "agent", "a", v); !errors.Is(err, store.ErrConflict) {
				t.Errorf("deleting version %d, which was published: %v, want ErrConflict", v, err)
			}
		}
		r.mustDelete(ctx, "agent", "a", 3)
		if err := r.s.DeleteItemVersion(ctx, "agent", "a", 3); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("deleting a deleted version: %v, want ErrNotFound", err)
		}
		// A number is never given twice.
		if v := r.mustSave(ctx, itemN("agent", "a", 4), 3); v.Version != 4 {
			t.Errorf("the save after deleting version 3 is version %d, want 4", v.Version)
		}
		// An item with only drafts goes with its last one, and its name is
		// free again.
		r.mustSave(ctx, itemN("agent", "b", 1), 0)
		r.mustSave(ctx, itemN("agent", "b", 2), 1)
		r.mustDelete(ctx, "agent", "b", 1)
		r.mustDelete(ctx, "agent", "b", 2)
		if _, err := r.s.GetItem(ctx, "agent", "b"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetItem of an item whose versions were all deleted: %v, want ErrNotFound", err)
		}
		if v := r.mustSave(ctx, itemN("agent", "b", 3), 0); v.Version != 1 {
			t.Errorf("recreated at version %d, want 1", v.Version)
		}
		if err := r.s.DeleteItemVersion(ctx, "agent", "none", 1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("deleting a version of no item: %v, want ErrNotFound", err)
		}
	})

	t.Run("KindsAreSeparate", func(t *testing.T) {
		t.Parallel()
		r := newItemRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustSave(ctx, itemN("agent", "x", 1), 0)
		r.mustSave(ctx, itemN("toolset", "x", 2), 0)
		r.mustSave(ctx, itemN("agent", "b", 3), 0)
		r.mustSave(ctx, itemN("agent", "A", 3), 0)
		r.mustPublish(ctx, "toolset", "x", 1)
		if got := itemNames(r.list(ctx, "agent")); !slices.Equal(got, []string{"A", "b", "x"}) {
			t.Errorf("agents %v, want [A b x] (sorted by name)", got)
		}
		if got := r.mustGet(ctx, "agent", "x"); got.Published != 0 {
			t.Errorf("publishing toolset x published agent x: %+v", got)
		}
		if got := r.list(ctx, "skill"); len(got) != 0 {
			t.Errorf("a kind with no items lists %v", itemNames(got))
		}
	})

	t.Run("Tenants", func(t *testing.T) {
		t.Parallel()
		r := newItemRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)
		r.mustSave(a, itemN("agent", "shared", 1), 0)
		r.mustSave(b, itemN("agent", "shared", 2), 0)
		r.mustSave(b, itemN("agent", "own", 1), 0)
		r.mustPublish(b, "agent", "shared", 1)
		if _, err := r.s.GetItem(a, "agent", "own"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("reading another tenant's item: %v, want ErrNotFound", err)
		}
		if _, err := r.s.GetItemVersion(a, "agent", "own", 1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("reading another tenant's version: %v, want ErrNotFound", err)
		}
		if _, err := r.s.ListItemVersions(a, "agent", "own"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("listing another tenant's versions: %v, want ErrNotFound", err)
		}
		if _, err := r.s.PublishItem(a, "agent", "own", 1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("publishing another tenant's item: %v, want ErrNotFound", err)
		}
		if err := r.s.DeleteItemVersion(a, "agent", "own", 1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("deleting another tenant's version: %v, want ErrNotFound", err)
		}
		if _, err := r.s.SaveItem(a, itemN("agent", "own", 3), 1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("saving on another tenant's item: %v, want ErrNotFound", err)
		}
		if got := r.mustGet(a, "agent", "shared"); got.Published != 0 {
			t.Errorf("tenant b's publication shows in tenant a: %+v", got)
		}
		r.mustSave(a, itemN("agent", "shared", 4), 1)
		r.mustDelete(a, "agent", "shared", 2)
	})

	t.Run("Invalid", func(t *testing.T) {
		t.Parallel()
		r := newItemRig(t, newStore)
		ctx := r.ctx(tenantA)
		ok := json.RawMessage(`{}`)
		big := json.RawMessage(`"` + strings.Repeat("x", store.MaxItemBytes-1) + `"`)
		long := strings.Repeat("m", store.MaxItemMessageBytes+1)
		for _, c := range []struct {
			name     string
			v        store.ItemVersion
			ifLatest int64
		}{
			{"empty kind", store.ItemVersion{Name: "a", Data: ok}, 0},
			{"kind in capitals", store.ItemVersion{Kind: "Agent", Name: "a", Data: ok}, 0},
			{"kind with a NUL", store.ItemVersion{Kind: "a\x00b", Name: "a", Data: ok}, 0},
			{"empty name", store.ItemVersion{Kind: "agent", Data: ok}, 0},
			{"name with a slash", store.ItemVersion{Kind: "agent", Name: "a/b", Data: ok}, 0},
			{"name too long", store.ItemVersion{Kind: "agent", Name: strings.Repeat("a", 65), Data: ok}, 0},
			{"no data", store.ItemVersion{Kind: "agent", Name: "a"}, 0},
			{"data not JSON", store.ItemVersion{Kind: "agent", Name: "a", Data: json.RawMessage(`{`)}, 0},
			{"data not UTF-8", store.ItemVersion{Kind: "agent", Name: "a", Data: json.RawMessage("\"\xff\"")}, 0},
			{"data with NUL", store.ItemVersion{Kind: "agent", Name: "a", Data: json.RawMessage("\"\x00\"")}, 0},
			{"data too big", store.ItemVersion{Kind: "agent", Name: "a", Data: big}, 0},
			{"message too long", store.ItemVersion{Kind: "agent", Name: "a", Data: ok, Message: long}, 0},
			{"author too long", store.ItemVersion{Kind: "agent", Name: "a", Data: ok, Author: long}, 0},
			{"message not UTF-8", store.ItemVersion{Kind: "agent", Name: "a", Data: ok, Message: "\xff"}, 0},
			{"author with a NUL", store.ItemVersion{Kind: "agent", Name: "a", Data: ok, Author: "a\x00"}, 0},
			{"negative version", store.ItemVersion{Kind: "agent", Name: "a", Data: ok}, -1},
		} {
			if _, err := r.s.SaveItem(ctx, c.v, c.ifLatest); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("SaveItem with %s: %v, want ErrInvalid", c.name, err)
			}
		}
		r.mustSave(ctx, itemN("agent", "a", 1), 0)
		for _, v := range []int64{0, -1} {
			if _, err := r.s.PublishItem(ctx, "agent", "a", v); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("PublishItem of version %d: %v, want ErrInvalid", v, err)
			}
			if err := r.s.DeleteItemVersion(ctx, "agent", "a", v); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("DeleteItemVersion of version %d: %v, want ErrInvalid", v, err)
			}
		}
		if _, err := r.s.PublishItem(ctx, "Agent", "a", 1); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("PublishItem of a bad kind: %v, want ErrInvalid", err)
		}
		// The largest data and message are kept.
		r.mustSave(ctx, store.ItemVersion{
			Kind: "agent", Name: "big", Data: json.RawMessage(`"` + strings.Repeat("x", store.MaxItemBytes-2) + `"`),
			Message: long[1:], Author: long[1:],
		}, 0)
	})

	t.Run("NeedsATenant", func(t *testing.T) {
		t.Parallel()
		s := newStore(t, clocktest.NewFake(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)))
		ctx := t.Context()
		_, err1 := s.SaveItem(ctx, itemN("agent", "a", 1), 0)
		_, err2 := s.PublishItem(ctx, "agent", "a", 1)
		_, err3 := s.GetItem(ctx, "agent", "a")
		_, err4 := s.ListItems(ctx, "agent")
		_, err5 := s.GetItemVersion(ctx, "agent", "a", 1)
		_, err6 := s.ListItemVersions(ctx, "agent", "a")
		err7 := s.DeleteItemVersion(ctx, "agent", "a", 1)
		for i, err := range []error{err1, err2, err3, err4, err5, err6, err7} {
			if !errors.Is(err, tenancy.ErrNoTenant) {
				t.Errorf("call %d with no tenant: %v, want ErrNoTenant", i+1, err)
			}
		}
		done, cancel := context.WithCancel(tenantCtx(ctx, t, tenantA))
		cancel()
		if _, err := s.SaveItem(done, itemN("agent", "a", 1), 0); !errors.Is(err, context.Canceled) {
			t.Errorf("SaveItem on a done context: %v, want context.Canceled", err)
		}
		if _, err := s.ListItems(done, "agent"); !errors.Is(err, context.Canceled) {
			t.Errorf("ListItems on a done context: %v, want context.Canceled", err)
		}
	})

	t.Run("NoSharedMemory", func(t *testing.T) {
		t.Parallel()
		r := newItemRig(t, newStore)
		ctx := r.ctx(tenantA)
		v := itemN("agent", "a", 1)
		got := r.mustSave(ctx, v, 0)
		v.Data[2] = 'X'
		got.Data[2] = 'Y'
		g, err := r.s.GetItemVersion(ctx, "agent", "a", 1)
		if err != nil {
			t.Fatal(err)
		}
		g.Data[2] = 'Z'
		l, err := r.s.ListItemVersions(ctx, "agent", "a")
		if err != nil {
			t.Fatal(err)
		}
		l[0].Data[2] = 'W'
		// The rig's check reads the version again and compares it with
		// itemN("agent", "a", 1).
	})

	t.Run("ConcurrentSaves", func(t *testing.T) {
		t.Parallel()
		r := newItemRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustSave(ctx, itemN("agent", "a", 0), 0)
		const n = 8
		var wg sync.WaitGroup
		var mu sync.Mutex
		won := 0
		for i := range n {
			wg.Go(func() {
				got, err := r.s.SaveItem(ctx, itemN("agent", "a", i+1), 1)
				switch {
				case err == nil:
					mu.Lock()
					won++
					mu.Unlock()
					r.recordSave(tenantA, got)
				case !errors.Is(err, store.ErrConflict):
					t.Errorf("save %d: %v, want nil or ErrConflict", i, err)
				}
			})
		}
		wg.Wait()
		if won != 1 {
			t.Errorf("%d of %d saves on version 1 succeeded, want exactly 1", won, n)
		}
		// Concurrent creates of one name: exactly one wins too.
		won = 0
		for i := range n {
			wg.Go(func() {
				got, err := r.s.SaveItem(ctx, itemN("agent", "new", i), 0)
				switch {
				case err == nil:
					mu.Lock()
					won++
					mu.Unlock()
					r.recordSave(tenantA, got)
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

	t.Run("PublishRacesDelete", func(t *testing.T) {
		t.Parallel()
		r := newItemRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustSave(ctx, itemN("agent", "keep", 1), 0)
		r.mustPublish(ctx, "agent", "keep", 1)
		const n = 6
		for i := range n {
			r.mustSave(ctx, itemN("agent", "keep", i+2), int64(i+1))
		}
		// Each draft is published and deleted at once: one of the two wins,
		// and the loser sees it (a publish of a deleted draft is not
		// found; a delete of a published one conflicts).
		var wg sync.WaitGroup
		for v := int64(2); v <= n+1; v++ {
			var pubErr, delErr error
			var it store.Item
			wg.Go(func() { it, pubErr = r.s.PublishItem(ctx, "agent", "keep", v) })
			wg.Go(func() { delErr = r.s.DeleteItemVersion(ctx, "agent", "keep", v) })
			wg.Wait()
			switch {
			case pubErr == nil && errors.Is(delErr, store.ErrConflict):
				r.recordPublish(tenantA, it)
			case delErr == nil && errors.Is(pubErr, store.ErrNotFound):
				r.recordDelete(tenantA, "agent", "keep", v)
			default:
				t.Errorf("version %d: publish %v, delete %v; want one to win and the other to fail", v, pubErr, delErr)
				return
			}
		}
	})
}

// itemModel is what the rig expects of one item.
type itemModel struct {
	item     store.Item
	versions map[int64]store.ItemVersion
}

// itemRig keeps what a store under test should hold: per tenant, each item
// and its versions as the store last returned them.
type itemRig struct {
	t   *testing.T
	s   store.ItemStore
	clk *skewedClock

	mu   sync.Mutex
	want map[string]map[[2]string]*itemModel // tenant → (kind, name) → item
}

func newItemRig(t *testing.T, newStore func(t *testing.T, clk clock.Clock) store.ItemStore) *itemRig {
	t.Helper()
	clk := &skewedClock{Fake: clocktest.NewFake(time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))}
	r := &itemRig{t: t, s: newStore(t, clk), clk: clk, want: map[string]map[[2]string]*itemModel{unusedTenant: {}}}
	t.Cleanup(r.check)
	return r
}

func (r *itemRig) ctx(tenant string) context.Context {
	r.mu.Lock()
	if r.want[tenant] == nil {
		r.want[tenant] = map[[2]string]*itemModel{}
	}
	r.mu.Unlock()
	return tenantCtx(r.t.Context(), r.t, tenant)
}

// recordSave records a version a save returned.
func (r *itemRig) recordSave(tenant string, v store.ItemVersion) {
	v.Data = slices.Clone(v.Data)
	r.mu.Lock()
	defer r.mu.Unlock()
	k := [2]string{v.Kind, v.Name}
	m := r.want[tenant][k]
	if m == nil {
		m = &itemModel{item: store.Item{Kind: v.Kind, Name: v.Name, CreatedAt: v.CreatedAt}, versions: map[int64]store.ItemVersion{}}
		r.want[tenant][k] = m
	}
	if v.Version <= m.item.Latest {
		r.t.Errorf("%s %q: saved as version %d, not past the latest %d", v.Kind, v.Name, v.Version, m.item.Latest)
	}
	m.item.Latest = v.Version
	m.item.UpdatedAt = laterOf(m.item.UpdatedAt, v.CreatedAt)
	m.versions[v.Version] = v
}

// recordPublish records an item a publish returned, at the clock's time.
func (r *itemRig) recordPublish(tenant string, it store.Item) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.want[tenant][[2]string{it.Kind, it.Name}]
	if m == nil {
		r.t.Errorf("published %s %q, which the rig never saw saved", it.Kind, it.Name)
		return
	}
	if m.item.Published != it.Published {
		m.item.UpdatedAt = laterOf(m.item.UpdatedAt, r.clk.Now())
	}
	m.item.Published = it.Published
	if v := m.versions[it.Published]; v.PublishedAt.IsZero() {
		v.PublishedAt = r.clk.Now()
		m.versions[it.Published] = v
	}
	if !sameItem(it, m.item) {
		r.t.Errorf("PublishItem returned %+v, want %+v", it, m.item)
	}
}

// recordDelete records a deleted version.
func (r *itemRig) recordDelete(tenant, kind, name string, version int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := [2]string{kind, name}
	m := r.want[tenant][k]
	if m == nil {
		return
	}
	delete(m.versions, version)
	if len(m.versions) == 0 {
		delete(r.want[tenant], k)
	}
}

func (r *itemRig) mustSave(ctx context.Context, v store.ItemVersion, ifLatest int64) store.ItemVersion {
	r.t.Helper()
	sent := slices.Clone(v.Data)
	got, err := r.s.SaveItem(ctx, v, ifLatest)
	if err != nil {
		r.t.Fatalf("SaveItem(%s %q, %d): %v", v.Kind, v.Name, ifLatest, err)
	}
	if got.Kind != v.Kind || got.Name != v.Name || !bytes.Equal(got.Data, sent) || got.Author != v.Author || got.Message != v.Message {
		r.t.Errorf("SaveItem returned %+v, want %s %q %q", got, v.Kind, v.Name, sent)
	}
	if ifLatest > 0 && got.Version != ifLatest+1 {
		r.t.Errorf("saving on version %d stored version %d, want %d", ifLatest, got.Version, ifLatest+1)
	}
	if !got.CreatedAt.Equal(r.clk.Now()) || !got.PublishedAt.IsZero() {
		r.t.Errorf("a new version created %v, published %v; want the clock's %v and never", got.CreatedAt, got.PublishedAt, r.clk.Now())
	}
	tn, _ := tenancy.From(ctx)
	r.recordSave(tn.ID(), got)
	return got
}

func (r *itemRig) mustPublish(ctx context.Context, kind, name string, version int64) store.Item {
	r.t.Helper()
	it, err := r.s.PublishItem(ctx, kind, name, version)
	if err != nil {
		r.t.Fatalf("PublishItem(%s %q, %d): %v", kind, name, version, err)
	}
	tn, _ := tenancy.From(ctx)
	r.recordPublish(tn.ID(), it)
	return it
}

func (r *itemRig) mustDelete(ctx context.Context, kind, name string, version int64) {
	r.t.Helper()
	if err := r.s.DeleteItemVersion(ctx, kind, name, version); err != nil {
		r.t.Fatalf("DeleteItemVersion(%s %q, %d): %v", kind, name, version, err)
	}
	tn, _ := tenancy.From(ctx)
	r.recordDelete(tn.ID(), kind, name, version)
}

func (r *itemRig) mustGet(ctx context.Context, kind, name string) store.Item {
	r.t.Helper()
	it, err := r.s.GetItem(ctx, kind, name)
	if err != nil {
		r.t.Fatalf("GetItem(%s %q): %v", kind, name, err)
	}
	return it
}

func (r *itemRig) list(ctx context.Context, kind string) []store.Item {
	r.t.Helper()
	got, err := r.s.ListItems(ctx, kind)
	if err != nil {
		r.t.Fatalf("ListItems(%s): %v", kind, err)
	}
	return got
}

// check compares every tenant's items and versions with the model.
func (r *itemRig) check() {
	ctx := context.Background()
	r.mu.Lock()
	defer r.mu.Unlock()
	for tenant, want := range r.want {
		tctx := tenantCtx(ctx, r.t, tenant)
		kinds := map[string][]string{"agent": nil}
		for k := range want {
			kinds[k[0]] = append(kinds[k[0]], k[1])
		}
		for kind, names := range kinds {
			slices.Sort(names)
			got, err := r.s.ListItems(tctx, kind)
			if err != nil {
				r.t.Errorf("tenant %s: ListItems(%s): %v", tenant, kind, err)
				continue
			}
			if g := itemNames(got); !slices.Equal(g, names) {
				r.t.Errorf("tenant %s lists %s items %v, want %v", tenant, kind, g, names)
				continue
			}
			for _, it := range got {
				r.checkItem(tctx, tenant, it, want[[2]string{kind, it.Name}])
			}
		}
	}
}

func (r *itemRig) checkItem(ctx context.Context, tenant string, listed store.Item, m *itemModel) {
	if !sameItem(listed, m.item) {
		r.t.Errorf("tenant %s: ListItems has %+v, want %+v", tenant, listed, m.item)
	}
	if one, err := r.s.GetItem(ctx, listed.Kind, listed.Name); err != nil || !sameItem(one, m.item) {
		r.t.Errorf("tenant %s: GetItem = %+v, %v; want %+v", tenant, one, err, m.item)
	}
	if m.item.Published != 0 && m.versions[m.item.Published].Version == 0 {
		r.t.Errorf("tenant %s: %s %q publishes version %d, which it does not have", tenant, listed.Kind, listed.Name, m.item.Published)
	}
	vs, err := r.s.ListItemVersions(ctx, listed.Kind, listed.Name)
	if err != nil {
		r.t.Errorf("tenant %s: ListItemVersions(%s %q): %v", tenant, listed.Kind, listed.Name, err)
		return
	}
	nums := slices.Sorted(maps.Keys(m.versions))
	var got []int64
	for _, v := range vs {
		got = append(got, v.Version)
		if err := sameItemVersion(v, m.versions[v.Version]); err != nil {
			r.t.Errorf("tenant %s: ListItemVersions: %v", tenant, err)
		}
		one, err := r.s.GetItemVersion(ctx, v.Kind, v.Name, v.Version)
		if err != nil {
			r.t.Errorf("tenant %s: GetItemVersion: %v", tenant, err)
		} else if err := sameItemVersion(one, m.versions[v.Version]); err != nil {
			r.t.Errorf("tenant %s: GetItemVersion: %v", tenant, err)
		}
	}
	if !slices.Equal(got, nums) {
		r.t.Errorf("tenant %s: %s %q lists versions %v, want %v (oldest first)", tenant, listed.Kind, listed.Name, got, nums)
	}
}

func sameItemVersion(got, want store.ItemVersion) error {
	if got.Kind != want.Kind || got.Name != want.Name || got.Version != want.Version || !bytes.Equal(got.Data, want.Data) ||
		got.Author != want.Author || got.Message != want.Message || !got.CreatedAt.Equal(want.CreatedAt) || !got.PublishedAt.Equal(want.PublishedAt) {
		return fmt.Errorf("%s %q version %d is %+v, want %+v", want.Kind, want.Name, want.Version, got, want)
	}
	return nil
}

func sameItem(a, b store.Item) bool {
	return a.Kind == b.Kind && a.Name == b.Name && a.Latest == b.Latest && a.Published == b.Published &&
		a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

func laterOf(a, b time.Time) time.Time {
	if b.Before(a) {
		return a
	}
	return b
}

func itemN(kind, name string, i int) store.ItemVersion {
	return store.ItemVersion{
		Kind: kind, Name: name, Data: json.RawMessage(fmt.Sprintf(`{"system": "prompt %d", "n": 1.50}`, i)),
		Author: "key-" + name, Message: fmt.Sprintf("change %d", i),
	}
}

func itemNames(its []store.Item) []string {
	names := []string{}
	for _, it := range its {
		names = append(names, it.Name)
	}
	return names
}
