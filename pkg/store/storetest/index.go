package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// IndexStore is what [RunConversationIndex] tests: a backend's conversation
// records together with the logs, runs, event logs and checkpoints a delete
// removes.
type IndexStore interface {
	store.ConversationIndex
	store.ConversationStore
	store.RunStore
	store.EventLog
	flow.Checkpointer
}

// RunConversationIndex runs the conformance suite for a
// [store.ConversationIndex]. newStore must return an empty store that reads
// times from clk (a fake clock the suite advances).
//
// Every test goes through one helper that checks, when the test ends, through
// every tenant the test used plus one that never wrote:
//
//  1. Tenants: GetConversation finds exactly the records the tenant created
//     and did not delete; others are ErrNotFound.
//  2. Records: a record keeps the Agent it was created with, and its Title
//     is the first non-empty one set; Messages is the number of messages in
//     the conversation's log; CreatedAt <= UpdatedAt.
//  3. ListConversations in both orders, paged, of every agent and of each
//     agent, gives the records once each, ordered by CreatedAt and ID (newest
//     first the reverse), each equal to what GetConversation returns.
func RunConversationIndex(t *testing.T, newStore func(t *testing.T, clk clock.Clock) IndexStore) {
	t.Helper()

	t.Run("CreateIsIdempotentAndScoped", func(t *testing.T) {
		t.Parallel()
		r := newIndexRig(t, newStore)
		ctx := r.ctx(tenantA)
		first := r.mustCreate(ctx, "c", "support", "")
		if !first.CreatedAt.Equal(r.clk.Now()) || !first.UpdatedAt.Equal(first.CreatedAt) {
			t.Errorf("new record has CreatedAt %v and UpdatedAt %v, want both the clock's %v", first.CreatedAt, first.UpdatedAt, r.clk.Now())
		}
		if first.Messages != 0 || first.Title != "" {
			t.Errorf("new record = %+v, want no messages and no title", first)
		}
		r.clk.Advance(time.Second)
		again, err := r.create(ctx, "c", "support", "a title the retry does not set")
		if err != nil || again != first {
			t.Errorf("retry with the same agent = %+v, %v; want the stored %+v", again, err, first)
		}
		if _, err := r.create(ctx, "c", "billing", ""); !errors.Is(err, store.ErrConflict) {
			t.Errorf("create with another agent = %v, want ErrConflict", err)
		}
		// Another tenant has its own record of the same id.
		other := r.mustCreate(r.ctx(tenantB), "c", "billing", "theirs")
		if other.Agent != "billing" || other.Title != "theirs" {
			t.Errorf("tenant B's record = %+v, want its own agent and title", other)
		}
		if _, err := r.s.GetConversation(r.ctx(tenantC), "c"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get in a third tenant = %v, want ErrNotFound", err)
		}
		if _, err := r.s.GetConversation(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get of a missing record = %v, want ErrNotFound", err)
		}
	})

	t.Run("InvalidInput", func(t *testing.T) {
		t.Parallel()
		r := newIndexRig(t, newStore)
		ctx := r.ctx(tenantA)
		for _, c := range []store.Conversation{
			{ID: "", Agent: "a"},
			{ID: "c\x00", Agent: "a"},
			{ID: "c\xff", Agent: "a"},
			{ID: "c", Agent: "a\x00"},
			{ID: "c", Agent: "a", Title: "t\xff"},
		} {
			if _, err := r.create(ctx, c.ID, c.Agent, c.Title); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("create %q agent %q title %q = %v, want ErrInvalid", c.ID, c.Agent, c.Title, err)
			}
		}
		r.mustCreate(ctx, "c", "a", "")
		if _, err := r.setTitle(ctx, "c", "bad\x00"); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("SetTitle with a NUL = %v, want ErrInvalid", err)
		}
		if _, err := r.s.CreateConversation(context.Background(), store.Conversation{ID: "c", Agent: "a"}); err == nil {
			t.Error("create with no tenant in ctx succeeded")
		}
	})

	t.Run("CountsTheLogsMessages", func(t *testing.T) {
		t.Parallel()
		r := newIndexRig(t, newStore)
		ctx := r.ctx(tenantA)
		// Messages logged before the record exists are counted when it is
		// created, but a compaction's.
		r.mustAppend(ctx, "c", promptAt(1, 1), turnAt(2, 1), compactionAt(3, 1))
		c := r.mustCreate(ctx, "c", "a", "")
		if want := int64(len(promptMessages(1)) + len(turnMessages(1))); c.Messages != want {
			t.Errorf("record over a logged prompt, turn and compaction counts %d messages, want %d", c.Messages, want)
		}
		r.clk.Advance(time.Second)
		r.mustAppend(ctx, "c", promptAt(4, 2))
		got := r.mustGet(ctx, "c")
		if !got.UpdatedAt.Equal(r.clk.Now()) {
			t.Errorf("UpdatedAt after an append = %v, want the clock's %v", got.UpdatedAt, r.clk.Now())
		}
		// An identical retry adds nothing; a conflicting one is refused and
		// adds nothing.
		r.mustAppend(ctx, "c", promptAt(4, 2))
		if err := r.s.Append(ctx, "c", promptAt(4, 7)); !errors.Is(err, store.ErrConflict) {
			t.Errorf("conflicting append = %v, want ErrConflict", err)
		}
		// A batch that retries its first entry and adds the rest.
		r.mustBatch(ctx, "c", promptAt(4, 2), turnAt(5, 2), compactionAt(6, 3))
		if err := r.s.AppendBatch(ctx, "c", []store.Entry{turnAt(7, 4), turnAt(9, 4)}); err == nil {
			t.Error("batch with a gap succeeded")
		}
		// A deleted-then-recreated conversation is checked by the invariants.
	})

	t.Run("UpdatedAtNeverDecreases", func(t *testing.T) {
		t.Parallel()
		r := newIndexRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.clk.Advance(time.Hour)
		c := r.mustCreate(ctx, "c", "a", "")
		r.clk.skew(-time.Minute) // a second writer whose clock is behind
		r.mustAppend(ctx, "c", promptAt(1, 1))
		if _, err := r.setTitle(ctx, "c", "t"); err != nil {
			t.Fatal(err)
		}
		if got := r.mustGet(ctx, "c"); got.UpdatedAt.Before(c.UpdatedAt) {
			t.Errorf("UpdatedAt went back from %v to %v when the clock did", c.UpdatedAt, got.UpdatedAt)
		}
	})

	t.Run("FirstTitleStays", func(t *testing.T) {
		t.Parallel()
		r := newIndexRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustCreate(ctx, "c", "a", "")
		r.mustCreate(ctx, "titled", "a", "given")
		if got, err := r.setTitle(ctx, "c", ""); err != nil || got.Title != "" {
			t.Errorf("SetTitle empty = %+v, %v; want no title and no error", got, err)
		}
		r.clk.Advance(time.Second)
		got, err := r.setTitle(ctx, "c", "first")
		if err != nil || got.Title != "first" || !got.UpdatedAt.Equal(r.clk.Now()) {
			t.Errorf("SetTitle first = %+v, %v; want title first, updated at %v", got, err, r.clk.Now())
		}
		if got, err := r.setTitle(ctx, "c", "second"); err != nil || got.Title != "first" {
			t.Errorf("SetTitle second = %+v, %v; want the first title kept", got, err)
		}
		if got, err := r.setTitle(ctx, "titled", "later"); err != nil || got.Title != "given" {
			t.Errorf("SetTitle on a record created with a title = %+v, %v; want it kept", got, err)
		}
		if _, err := r.setTitle(ctx, "missing", "t"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("SetTitle of a missing record = %v, want ErrNotFound", err)
		}
		if _, err := r.setTitle(r.ctx(tenantB), "c", "t"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("SetTitle from another tenant = %v, want ErrNotFound", err)
		}
	})

	t.Run("ListOrderAndPaging", func(t *testing.T) {
		t.Parallel()
		r := newIndexRig(t, newStore)
		ctx := r.ctx(tenantA)
		// Two records share each creation time, so ID breaks the tie.
		for i := range 7 {
			if i%2 == 0 {
				r.clk.Advance(time.Second)
			}
			agent := "a"
			if i%3 == 0 {
				agent = "b"
			}
			r.mustCreate(ctx, fmt.Sprintf("c%d", 9-i), agent, "")
		}
		r.mustCreate(r.ctx(tenantB), "other", "a", "")
		t0 := r.clk.Now().Add(-2 * time.Second) // the third creation time
		asc := listConversations(ctx, t, r.s, "", store.Page{Limit: 2})
		var from []store.Conversation
		for _, c := range asc {
			if !c.CreatedAt.Before(t0) {
				from = append(from, c)
			}
		}
		if got := listConversations(ctx, t, r.s, "", store.Page{Limit: 2, FromTime: t0}); !slices.Equal(ids(got), ids(from)) {
			t.Errorf("list from %v = %v, want %v", t0, ids(got), ids(from))
		}
		var until []store.Conversation
		for _, c := range slices.Backward(asc) {
			if !c.CreatedAt.After(t0) {
				until = append(until, c)
			}
		}
		if got := listConversations(ctx, t, r.s, "", store.Page{Limit: 3, FromTime: t0, Order: store.Descending}); !slices.Equal(ids(got), ids(until)) {
			t.Errorf("newest-first list from %v = %v, want %v", t0, ids(got), ids(until))
		}
		page, err := r.s.ListConversations(ctx, "", store.Page{Limit: 2})
		if err != nil || page.Next == "" {
			t.Fatalf("first page = %+v, %v; want a Next", page, err)
		}
		for _, p := range []store.Page{
			{FromSeq: 1},
			{Cursor: "not a cursor"},
			{Cursor: page.Next, Order: store.Descending},
			{Cursor: page.Next, FromTime: t0},
		} {
			if _, err := r.s.ListConversations(ctx, "", p); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("list %+v = %v, want ErrInvalid", p, err)
			}
		}
		// A run cursor is not a conversation cursor.
		if _, err := r.s.Create(ctx, "r1", "c9"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.s.Transition(ctx, "r1", store.StatusRunning, store.StatusCompleted, store.Details{}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.s.Create(ctx, "r2", "c9"); err != nil {
			t.Fatal(err)
		}
		runs, err := r.s.List(ctx, "c9", store.Page{Limit: 1})
		if err != nil || runs.Next == "" {
			t.Fatalf("run page = %+v, %v; want a Next", runs, err)
		}
		if _, err := r.s.ListConversations(ctx, "", store.Page{Cursor: runs.Next}); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("list with a run cursor = %v, want ErrInvalid", err)
		}
	})

	t.Run("DeleteRemovesTheConversation", func(t *testing.T) {
		t.Parallel()
		r := newIndexRig(t, newStore)
		ctx := r.ctx(tenantA)
		for _, conv := range []string{"c", "kept"} {
			r.mustCreate(ctx, conv, "a", "")
			r.mustAppend(ctx, conv, promptAt(1, 1), turnAt(2, 1))
			run := conv + "-done"
			if _, err := r.s.Create(ctx, run, conv); err != nil {
				t.Fatal(err)
			}
			if _, err := r.s.Transition(ctx, run, store.StatusRunning, store.StatusCompleted, store.Details{}); err != nil {
				t.Fatal(err)
			}
			if err := r.s.AppendEvents(ctx, run, eventsFrom(1, 2, 1)); err != nil {
				t.Fatal(err)
			}
			if err := r.s.Save(ctx, run, checkpoint(run, "s")); err != nil {
				t.Fatal(err)
			}
			// An unfinished run whose lease has expired.
			live := conv + "-live"
			if _, err := r.s.Create(ctx, live, conv); err != nil {
				t.Fatal(err)
			}
			if _, err := r.s.Claim(ctx, live, "p1", time.Minute); err != nil {
				t.Fatal(err)
			}
		}
		r.mustCreate(r.ctx(tenantB), "c", "a", "")

		// While a process holds the unfinished run's lease, nothing goes.
		if err := r.delete(ctx, "c"); !errors.Is(err, store.ErrLeased) {
			t.Fatalf("delete while a run is leased = %v, want ErrLeased", err)
		}
		r.mustGet(ctx, "c")
		if _, err := r.s.Get(ctx, "c-done"); err != nil {
			t.Errorf("a refused delete removed a run: %v", err)
		}
		r.clk.Advance(2 * time.Minute)
		if err := r.delete(ctx, "c"); err != nil {
			t.Fatalf("delete after the lease expired = %v", err)
		}
		if err := r.delete(ctx, "c"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("second delete = %v, want ErrNotFound", err)
		}
		if got := readAll(ctx, t, r.s, "c", store.Page{}); len(got) != 0 {
			t.Errorf("deleted conversation's log has %d entries", len(got))
		}
		for _, run := range []string{"c-done", "c-live"} {
			if _, err := r.s.Get(ctx, run); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("Get of deleted run %q = %v, want ErrNotFound", run, err)
			}
		}
		if got := readEvents(ctx, t, r.s, "c-done", 0, 0); len(got) != 0 {
			t.Errorf("deleted run has %d events", len(got))
		}
		if _, err := r.s.Load(ctx, "c-done"); !errors.Is(err, flow.ErrNotFound) {
			t.Errorf("Load of a deleted run's checkpoint = %v, want ErrNotFound", err)
		}
		if _, err := r.s.Unfinished(ctx, "c"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Unfinished of the deleted conversation = %v, want ErrNotFound", err)
		}
		// The other conversation, and the same id in another tenant, stay.
		if got := readAll(ctx, t, r.s, "kept", store.Page{}); len(got) != 2 {
			t.Errorf("kept conversation's log has %d entries, want 2", len(got))
		}
		if got := readEvents(ctx, t, r.s, "kept-done", 0, 0); len(got) != 2 {
			t.Errorf("kept run has %d events, want 2", len(got))
		}
		if _, err := r.s.Load(ctx, "kept-done"); err != nil {
			t.Errorf("Load of the kept run's checkpoint = %v", err)
		}
		r.mustGet(r.ctx(tenantB), "c")
		if err := r.delete(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("delete of a missing record = %v, want ErrNotFound", err)
		}

		// The id starts afresh.
		r.clk.Advance(time.Second)
		again := r.mustCreate(ctx, "c", "other", "")
		if again.Messages != 0 || !again.CreatedAt.Equal(r.clk.Now()) {
			t.Errorf("recreated record = %+v, want no messages, created now", again)
		}
		r.mustAppend(ctx, "c", promptAt(1, 5))
		if _, err := r.s.Create(ctx, "c-live", "c"); err != nil {
			t.Errorf("recreating a deleted run's id = %v", err)
		}
	})

	t.Run("ConcurrentCreatesAgree", func(t *testing.T) {
		t.Parallel()
		r := newIndexRig(t, newStore)
		ctx := r.ctx(tenantA)
		const n = 8
		got := make([]store.Conversation, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() { got[i], errs[i] = r.create(ctx, "c", "a", fmt.Sprint(i)) })
		}
		wg.Wait()
		for i := range n {
			if errs[i] != nil || got[i] != got[0] {
				t.Errorf("create %d = %+v, %v; want the record every create got, %+v", i, got[i], errs[i], got[0])
			}
		}
	})

	t.Run("ConcurrentAppendsAreAllCounted", func(t *testing.T) {
		t.Parallel()
		r := newIndexRig(t, newStore)
		ctx := r.ctx(tenantA)
		const n = 6
		var wg sync.WaitGroup
		for i := range n {
			conv := fmt.Sprint("c", i%2)
			wg.Go(func() {
				if i < 2 {
					if _, err := r.create(ctx, conv, "a", ""); err != nil {
						t.Error(err)
					}
				}
			})
		}
		wg.Wait()
		for _, conv := range []string{"c0", "c1"} {
			wg.Go(func() {
				for seq := int64(1); seq <= 20; seq++ {
					if err := r.s.Append(ctx, conv, promptAt(seq, int(seq))); err != nil {
						t.Error(err)
						return
					}
				}
			})
			wg.Go(func() {
				for range 20 {
					if _, err := r.s.SetTitle(ctx, conv, "t"); err != nil {
						t.Error(err)
						return
					}
				}
			})
		}
		wg.Wait()
		r.mu.Lock()
		r.logs["c0"], r.logs["c1"] = true, true
		r.titles[logKey{tenantA, "c0"}], r.titles[logKey{tenantA, "c1"}] = "t", "t"
		r.mu.Unlock()
	})
}

// indexRig is the one helper every test of RunConversationIndex goes
// through; see the invariants on RunConversationIndex.
type indexRig struct {
	t   *testing.T
	s   IndexStore
	clk *skewedClock

	mu      sync.Mutex
	agents  map[logKey]string // (tenant, id): the records that exist
	titles  map[logKey]string
	logs    map[string]bool // conversation ids appended to, in any tenant
	tenants map[string]bool
}

func newIndexRig(t *testing.T, newStore func(t *testing.T, clk clock.Clock) IndexStore) *indexRig {
	t.Helper()
	clk := &skewedClock{Fake: clocktest.NewFake(time.Time{})}
	r := &indexRig{
		t:       t,
		s:       newStore(t, clk),
		clk:     clk,
		agents:  map[logKey]string{},
		titles:  map[logKey]string{},
		logs:    map[string]bool{},
		tenants: map[string]bool{unusedTenant: true},
	}
	t.Cleanup(r.check)
	return r
}

func (r *indexRig) ctx(id string) context.Context {
	r.mu.Lock()
	r.tenants[id] = true
	r.mu.Unlock()
	return tenantCtx(r.t.Context(), r.t, id)
}

func (r *indexRig) key(ctx context.Context, id string) logKey {
	tn, _ := tenancy.From(ctx)
	return logKey{tn.ID(), id}
}

func (r *indexRig) create(ctx context.Context, id, agent, title string) (store.Conversation, error) {
	c, err := r.s.CreateConversation(ctx, store.Conversation{ID: id, Agent: agent, Title: title, Messages: 99})
	if err != nil {
		return c, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := r.key(ctx, id)
	if _, ok := r.agents[k]; !ok {
		r.agents[k], r.titles[k] = agent, c.Title
	}
	return c, nil
}

func (r *indexRig) mustCreate(ctx context.Context, id, agent, title string) store.Conversation {
	r.t.Helper()
	c, err := r.create(ctx, id, agent, title)
	if err != nil {
		r.t.Fatalf("CreateConversation %q: %v", id, err)
	}
	if c.ID != id || c.Agent != agent {
		r.t.Fatalf("CreateConversation %q, %q = %+v", id, agent, c)
	}
	return c
}

func (r *indexRig) mustGet(ctx context.Context, id string) store.Conversation {
	r.t.Helper()
	c, err := r.s.GetConversation(ctx, id)
	if err != nil {
		r.t.Fatalf("GetConversation %q: %v", id, err)
	}
	return c
}

func (r *indexRig) setTitle(ctx context.Context, id, title string) (store.Conversation, error) {
	c, err := r.s.SetTitle(ctx, id, title)
	if err == nil {
		r.mu.Lock()
		if k := r.key(ctx, id); r.titles[k] == "" {
			r.titles[k] = title
		}
		r.mu.Unlock()
	}
	return c, err
}

func (r *indexRig) delete(ctx context.Context, id string) error {
	err := r.s.DeleteConversation(ctx, id)
	if err == nil {
		r.mu.Lock()
		delete(r.agents, r.key(ctx, id))
		delete(r.titles, r.key(ctx, id))
		r.mu.Unlock()
	}
	return err
}

func (r *indexRig) mustAppend(ctx context.Context, conv string, es ...store.Entry) {
	r.t.Helper()
	r.mu.Lock()
	r.logs[conv] = true
	r.mu.Unlock()
	for _, e := range es {
		if err := r.s.Append(ctx, conv, e); err != nil {
			r.t.Fatalf("Append %q seq %d: %v", conv, e.Seq, err)
		}
	}
}

func (r *indexRig) mustBatch(ctx context.Context, conv string, es ...store.Entry) {
	r.t.Helper()
	r.mu.Lock()
	r.logs[conv] = true
	r.mu.Unlock()
	if err := r.s.AppendBatch(ctx, conv, es); err != nil {
		r.t.Fatalf("AppendBatch %q: %v", conv, err)
	}
}

func (r *indexRig) check() {
	t := r.t
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tenant := range sortedKeys(r.tenants) {
		// t.Context is done once cleanup runs.
		ctx := tenantCtx(context.Background(), t, tenant)
		var want []string
		agents := map[string]bool{}
		for k, agent := range r.agents {
			if k.tenant == tenant {
				want = append(want, k.conv)
				agents[agent] = true
			}
		}
		slices.Sort(want)
		all := listConversations(ctx, t, r.s, "", store.Page{Limit: 2})
		if got := slices.Sorted(slices.Values(ids(all))); !slices.Equal(got, want) {
			t.Errorf("tenant %s lists records %v, want %v", tenant, got, want)
		}
		if !slices.IsSortedFunc(all, cmpConversation) {
			t.Errorf("tenant %s lists records out of order: %v", tenant, ids(all))
		}
		desc := listConversations(ctx, t, r.s, "", store.Page{Limit: 3, Order: store.Descending})
		if !slices.Equal(ids(desc), slices.Collect(func(yield func(string) bool) {
			for _, c := range slices.Backward(all) {
				if !yield(c.ID) {
					return
				}
			}
		})) {
			t.Errorf("tenant %s lists newest first %v, want the reverse of %v", tenant, ids(desc), ids(all))
		}
		for agent := range agents {
			of := listConversations(ctx, t, r.s, agent, store.Page{Limit: 1})
			want := slices.DeleteFunc(slices.Clone(all), func(c store.Conversation) bool { return c.Agent != agent })
			if !slices.Equal(ids(of), ids(want)) {
				t.Errorf("tenant %s lists records of agent %s %v, want %v", tenant, agent, ids(of), ids(want))
			}
		}
		for _, c := range all {
			k := logKey{tenant, c.ID}
			if got := r.mustGetIn(ctx, c.ID); got != c {
				t.Errorf("tenant %s lists %+v, Get returns %+v", tenant, c, got)
			}
			if c.Agent != r.agents[k] || c.Title != r.titles[k] {
				t.Errorf("tenant %s record %q has agent %q title %q, want %q and %q", tenant, c.ID, c.Agent, c.Title, r.agents[k], r.titles[k])
			}
			if c.UpdatedAt.Before(c.CreatedAt) {
				t.Errorf("tenant %s record %q was updated at %v, before its creation at %v", tenant, c.ID, c.UpdatedAt, c.CreatedAt)
			}
			var n int64
			for _, e := range readAll(ctx, t, r.s, c.ID, store.Page{}) {
				if e.Kind != store.KindCompaction {
					n += int64(len(e.Messages))
				}
			}
			if c.Messages != n {
				t.Errorf("tenant %s record %q counts %d messages, its log holds %d", tenant, c.ID, c.Messages, n)
			}
		}
		for conv := range r.logs {
			if _, ok := r.agents[logKey{tenant, conv}]; ok {
				continue
			}
			if _, err := r.s.GetConversation(ctx, conv); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("tenant %s Get of %q, which has no record, = %v, want ErrNotFound", tenant, conv, err)
			}
		}
	}
}

func (r *indexRig) mustGetIn(ctx context.Context, id string) store.Conversation {
	c, err := r.s.GetConversation(ctx, id)
	if err != nil {
		r.t.Errorf("GetConversation %q: %v", id, err)
	}
	return c
}

func cmpConversation(a, b store.Conversation) int {
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	switch {
	case a.ID < b.ID:
		return -1
	case a.ID > b.ID:
		return 1
	}
	return 0
}

func ids(cs []store.Conversation) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func listConversations(ctx context.Context, t *testing.T, s store.ConversationIndex, agent string, p store.Page) []store.Conversation {
	t.Helper()
	limit := pageLimit(p.Limit)
	var out []store.Conversation
	for range 100_000 {
		page, err := s.ListConversations(ctx, agent, p)
		if err != nil {
			t.Fatalf("ListConversations %q %+v: %v", agent, p, err)
		}
		if page.Next != "" && len(page.Conversations) != limit {
			t.Fatalf("ListConversations: a page with a Next holds %d records, want the limit %d", len(page.Conversations), limit)
		}
		out = append(out, page.Conversations...)
		if page.Next == "" {
			return out
		}
		p = store.Page{Cursor: page.Next, Limit: p.Limit, Order: p.Order}
	}
	t.Fatal("ListConversations never ended")
	return nil
}
