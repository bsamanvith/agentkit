package storetest

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// convRig is the one helper every conversation test and the simulation go
// through. It records every append the store accepted and, when the test ends,
// checks the conversation invariants on everything the test touched:
//
//  1. Dense: each conversation's entries are numbered 1..n with no gap, and the
//     accepted appends are exactly those positions.
//  2. Unchanged: every entry reads back as it was first accepted (messages as
//     the same JSON bytes, the same Record bytes, Kind, RunID and Covers): no
//     retry or conflicting append changed it, and no caller scribbling on
//     what it passed in or got back did either.
//  3. Times: CreatedAt is set and never decreases along the log.
//  4. History in both orders, paged, gives the log once, newest-first being
//     the reverse of oldest-first.
//  5. Context is a suffix of History: the entries from the latest compaction
//     (or the first entry) to the end, with Seq the length of the log.
//  6. Tenants: every conversation is read through every tenant the test used
//     plus one that never wrote; each sees only its own appends.
type convRig struct {
	t   *testing.T
	s   store.ConversationStore
	clk *clocktest.Fake

	mu       sync.Mutex
	accepted map[logKey]map[int64]snapshot
	convs    map[string]bool
	tenants  map[string]bool
}

type logKey struct{ tenant, conv string }

func newConvRig(t *testing.T, newStore func(t *testing.T, clk clock.Clock) store.ConversationStore) *convRig {
	t.Helper()
	clk := clocktest.NewFake(time.Time{})
	r := &convRig{
		t:        t,
		s:        newStore(t, clk),
		clk:      clk,
		accepted: map[logKey]map[int64]snapshot{},
		convs:    map[string]bool{},
		tenants:  map[string]bool{unusedTenant: true},
	}
	t.Cleanup(r.check)
	return r
}

// ctx returns a context of tenant id, derived from the test's context.
func (r *convRig) ctx(id string) context.Context {
	r.mu.Lock()
	r.tenants[id] = true
	r.mu.Unlock()
	return tenantCtx(r.t.Context(), r.t, id)
}

// append calls Append and records an accepted entry. It is safe for
// concurrent use.
func (r *convRig) append(ctx context.Context, conv string, e store.Entry) error {
	want, snapErr := snap(e) // before Append: the caller may scribble on e later
	err := r.s.Append(ctx, conv, e)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.convs[conv] = true
	if err != nil {
		return err
	}
	tn, ok := tenancy.From(ctx)
	if !ok {
		r.t.Errorf("Append to %q succeeded without a tenant", conv)
		return err
	}
	if snapErr != nil {
		r.t.Errorf("Append accepted entry %d whose messages do not marshal: %v", e.Seq, snapErr)
		return err
	}
	k := logKey{tn.ID(), conv}
	if r.accepted[k] == nil {
		r.accepted[k] = map[int64]snapshot{}
	}
	if prev, ok := r.accepted[k][e.Seq]; ok && prev != want {
		r.t.Errorf("tenant %s conversation %q: Append accepted a different entry at taken seq %d", k.tenant, conv, e.Seq)
		return err
	}
	if _, ok := r.accepted[k][e.Seq]; !ok {
		r.accepted[k][e.Seq] = want
	}
	return err
}

// mustAppend appends entries and fails the test on any error.
func (r *convRig) mustAppend(ctx context.Context, conv string, es ...store.Entry) {
	r.t.Helper()
	for _, e := range es {
		if err := r.append(ctx, conv, e); err != nil {
			r.t.Fatalf("Append %q entry %d: %v", conv, e.Seq, err)
		}
	}
}

// readAll pages through History and checks each page's shape on the way.
func readAll(ctx context.Context, t *testing.T, s store.ConversationStore, conv string, p store.Page) []store.Entry {
	t.Helper()
	limit := pageLimit(p.Limit)
	var out []store.Entry
	for range 100_000 {
		page, err := s.History(ctx, conv, p)
		if err != nil {
			t.Fatalf("History %q %+v: %v", conv, p, err)
		}
		if page.Next != "" && len(page.Entries) != limit {
			t.Fatalf("History %q: a page with a Next holds %d entries, want the limit %d", conv, len(page.Entries), limit)
		}
		out = append(out, page.Entries...)
		if page.Next == "" {
			return out
		}
		p = store.Page{Cursor: page.Next, Limit: p.Limit, Order: p.Order}
	}
	t.Fatalf("History %q never ended", conv)
	return nil
}

func pageLimit(l int) int {
	switch {
	case l == 0:
		return store.DefaultPageLimit
	case l > store.MaxPageLimit:
		return store.MaxPageLimit
	}
	return l
}

func (r *convRig) check() {
	t := r.t
	r.mu.Lock()
	defer r.mu.Unlock()
	convs := sortedKeys(r.convs)
	for _, tenant := range sortedKeys(r.tenants) {
		// t.Context is done once cleanup runs.
		ctx := tenantCtx(context.Background(), t, tenant)
		for _, conv := range convs {
			r.checkLog(ctx, tenant, conv)
		}
	}
}

func (r *convRig) checkLog(ctx context.Context, tenant, conv string) {
	t := r.t
	accepted := r.accepted[logKey{tenant, conv}]
	for seq := range accepted {
		if seq < 1 || seq > int64(len(accepted)) {
			t.Errorf("tenant %s conversation %q: Append accepted entry %d, leaving a gap (%d entries accepted)", tenant, conv, seq, len(accepted))
		}
	}

	asc := readAll(ctx, t, r.s, conv, store.Page{Order: store.Ascending, Limit: 7})
	if len(asc) != len(accepted) {
		t.Errorf("tenant %s conversation %q: History has %d entries, %d were accepted", tenant, conv, len(asc), len(accepted))
	}
	for i, e := range asc {
		if e.Seq != int64(i+1) {
			t.Errorf("tenant %s conversation %q: History entry %d has Seq %d (seqs %v)", tenant, conv, i, e.Seq, seqs(asc))
			return
		}
		if want, ok := accepted[e.Seq]; !ok || mustSnap(t, e) != want {
			t.Errorf("tenant %s conversation %q: entry %d reads back as\n%+v\nwant what was accepted\n%+v", tenant, conv, e.Seq, mustSnap(t, e), want)
		}
		if e.CreatedAt.IsZero() {
			t.Errorf("tenant %s conversation %q: entry %d has no CreatedAt", tenant, conv, e.Seq)
		}
		if i > 0 && e.CreatedAt.Before(asc[i-1].CreatedAt) {
			t.Errorf("tenant %s conversation %q: entry %d was created at %v, before entry %d at %v", tenant, conv, e.Seq, e.CreatedAt, i, asc[i-1].CreatedAt)
		}
	}

	desc := readAll(ctx, t, r.s, conv, store.Page{Order: store.Descending, Limit: 3})
	if !sameEntries(t, desc, reversed(asc)) {
		t.Errorf("tenant %s conversation %q: History newest first is %v, want the reverse of %v", tenant, conv, seqs(desc), seqs(asc))
	}

	view, err := r.s.Context(ctx, conv)
	if err != nil {
		t.Errorf("tenant %s conversation %q: Context: %v", tenant, conv, err)
		return
	}
	if view.Seq != int64(len(asc)) {
		t.Errorf("tenant %s conversation %q: Context Seq = %d, want %d", tenant, conv, view.Seq, len(asc))
	}
	if want := contextOf(asc); !sameEntries(t, view.Entries, want) {
		t.Errorf("tenant %s conversation %q: Context entries %v, want the suffix of History from the latest compaction %v", tenant, conv, seqs(view.Entries), seqs(want))
	}
}

// contextOf restates Context: the latest compaction and everything after it,
// or everything.
func contextOf(log []store.Entry) []store.Entry {
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].Kind == store.KindCompaction {
			return log[i:]
		}
	}
	return log
}

// sameEntries compares entries by Seq, CreatedAt and snapshot.
func sameEntries(t *testing.T, a, b []store.Entry) bool {
	t.Helper()
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Seq != b[i].Seq || !a[i].CreatedAt.Equal(b[i].CreatedAt) || mustSnap(t, a[i]) != mustSnap(t, b[i]) {
			return false
		}
	}
	return true
}

func reversed(es []store.Entry) []store.Entry {
	out := slices.Clone(es)
	slices.Reverse(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// isErr reports whether err matches want, with nil matching nil.
func isErr(err, want error) bool {
	if want == nil {
		return err == nil
	}
	return errors.Is(err, want)
}
