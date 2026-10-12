package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// RunConversationStore runs the conformance suite for a
// [store.ConversationStore]. newStore must return an empty store that reads
// entry times from clk (a fake clock the suite advances).
//
// Every test goes through one helper that checks the invariants listed on
// convRig when the test ends, and the suite ends with a seeded simulation
// ([SimulateConversations]) through the same helper.
func RunConversationStore(t *testing.T, newStore func(t *testing.T, clk clock.Clock) store.ConversationStore) {
	t.Helper()

	t.Run("MessageJSONRoundTripsByteForByte", func(t *testing.T) {
		t.Parallel()
		r := newConvRig(t, newStore)
		ctx := r.ctx(tenantA)
		in := []store.Entry{promptAt(1, 1), turnAt(2, 2), compactionAt(3, 3), turnAt(4, 4)}
		// Invalid UTF-8 in text is replaced when marshaled; what is stored is
		// what Append marshaled, and that is what must come back.
		in[3].Messages = append(in[3].Messages, llm.Assistant(llm.Text{Text: "bad \xff utf-8"}))
		want := make(map[int64]snapshot)
		for _, e := range in {
			want[e.Seq] = mustSnap(t, e)
		}
		r.mustAppend(ctx, "c", in...)

		check := func(read string, es []store.Entry) {
			t.Helper()
			for _, e := range es {
				if got := mustSnap(t, e); got != want[e.Seq] {
					t.Errorf("%s entry %d:\n got  %s %s\n want %s %s", read, e.Seq, got.Messages, got.Record, want[e.Seq].Messages, want[e.Seq].Record)
				}
			}
		}
		check("History ascending", readAll(ctx, t, r.s, "c", store.Page{Order: store.Ascending}))
		check("History descending", readAll(ctx, t, r.s, "c", store.Page{Order: store.Descending, Limit: 1}))
		view, err := r.s.Context(ctx, "c")
		if err != nil {
			t.Fatalf("Context: %v", err)
		}
		check("Context", view.Entries)
		// The Opaque data itself, not only its encoding, is unchanged.
		got := view.Entries[len(view.Entries)-1].Messages[0].Content[0]
		if o, ok := got.(llm.Opaque); !ok || string(o.Data) != string(opaque(4).Data) {
			t.Errorf("Opaque block read back as %#v, want data %s", got, opaque(4).Data)
		}
	})

	t.Run("AppendIsCreateIfAbsent", func(t *testing.T) {
		t.Parallel()

		t.Run("IdenticalRetryIsANoOp", func(t *testing.T) {
			t.Parallel()
			r := newConvRig(t, newStore)
			ctx := r.ctx(tenantA)
			r.mustAppend(ctx, "c", promptAt(1, 1), turnAt(2, 2))
			first := readAll(ctx, t, r.s, "c", store.Page{})
			r.clk.Advance(time.Hour)

			// A retry rebuilt from scratch (new slices, a CreatedAt of its
			// own) is the same entry.
			retry := turnAt(2, 2)
			retry.CreatedAt = r.clk.Now()
			for _, e := range []store.Entry{retry, promptAt(1, 1), retry} {
				if err := r.append(ctx, "c", e); err != nil {
					t.Fatalf("retry of entry %d: %v, want nil", e.Seq, err)
				}
			}
			if got := readAll(ctx, t, r.s, "c", store.Page{}); !sameEntries(t, got, first) {
				t.Errorf("after retries History is %v, want it unchanged, created at the first write", seqs(got))
			}
		})

		t.Run("DifferentEntryIsErrConflict", func(t *testing.T) {
			t.Parallel()
			r := newConvRig(t, newStore)
			ctx := r.ctx(tenantA)
			r.mustAppend(ctx, "c", promptAt(1, 1), turnAt(2, 2))
			for name, change := range map[string]func(*store.Entry){
				"messages": func(e *store.Entry) { e.Messages = turnMessages(3) },
				"one opaque byte": func(e *store.Entry) {
					e.Messages[0].Content[0] = llm.Opaque{Provider: "anthropic", Data: json.RawMessage(string(opaque(2).Data) + " ")}
				},
				"kind":                    func(e *store.Entry) { e.Kind = store.KindPrompt },
				"run id":                  func(e *store.Entry) { e.RunID = "other" },
				"record bytes, same JSON": func(e *store.Entry) { e.Record = json.RawMessage(`{"z":2,"a":1.50}`) },
				"no record":               func(e *store.Entry) { e.Record = nil },
			} {
				e := turnAt(2, 2)
				change(&e)
				if err := r.append(ctx, "c", e); !errors.Is(err, store.ErrConflict) {
					t.Errorf("entry 2 with other %s: Append = %v, want ErrConflict", name, err)
				}
			}
			c := compactionAt(2, 9)
			if err := r.append(ctx, "c", c); !errors.Is(err, store.ErrConflict) {
				t.Errorf("compaction at taken entry 2: Append = %v, want ErrConflict", err)
			}
		})

		t.Run("GapIsErrGap", func(t *testing.T) {
			t.Parallel()
			r := newConvRig(t, newStore)
			ctx := r.ctx(tenantA)
			if err := r.append(ctx, "c", promptAt(2, 1)); !errors.Is(err, store.ErrGap) {
				t.Errorf("entry 2 of an empty conversation: Append = %v, want ErrGap", err)
			}
			r.mustAppend(ctx, "c", promptAt(1, 1))
			for _, seq := range []int64{3, 4, 100} {
				if err := r.append(ctx, "c", turnAt(seq, 2)); !errors.Is(err, store.ErrGap) {
					t.Errorf("entry %d after 1 entry: Append = %v, want ErrGap", seq, err)
				}
			}
			r.mustAppend(ctx, "c", turnAt(2, 2))
		})
	})

	t.Run("InvalidEntryIsErrInvalid", func(t *testing.T) {
		t.Parallel()
		r := newConvRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustAppend(ctx, "c", promptAt(1, 1), turnAt(2, 2))
		for name, e := range map[string]store.Entry{
			"seq 0":                       withSeq(turnAt(3, 3), 0),
			"negative seq":                withSeq(turnAt(3, 3), -1),
			"unknown kind":                withKind(turnAt(3, 3), "note"),
			"empty kind":                  withKind(turnAt(3, 3), ""),
			"no messages":                 {Seq: 3, Kind: store.KindTurn},
			"a nil block":                 {Seq: 3, Kind: store.KindTurn, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{nil}}}},
			"unknown role":                {Seq: 3, Kind: store.KindTurn, Messages: []llm.Message{{Role: "system", Content: []llm.Block{llm.Text{Text: "x"}}}}},
			"a block of a newer version":  {Seq: 3, Kind: store.KindTurn, Messages: []llm.Message{llm.Assistant(llm.Text{Text: "x"}, llm.Unknown{Type: "image", Data: json.RawMessage(`{"type":"image"}`)})}},
			"record not JSON":             withRecord(turnAt(3, 3), `{"a":`),
			"turn that covers":            withCovers(turnAt(3, 3), 2),
			"prompt that covers":          withCovers(promptAt(3, 3), 2),
			"compaction covering less":    withCovers(compactionAt(3, 3), 1),
			"compaction covering itself":  withCovers(compactionAt(3, 3), 3),
			"compaction covering nothing": withCovers(compactionAt(3, 3), 0),
		} {
			if err := r.append(ctx, "c", e); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: Append = %v, want ErrInvalid", name, err)
			}
		}
		if err := r.append(ctx, "c", compactionAt(1, 1)); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("compaction at entry 1: Append = %v, want ErrInvalid", err)
		}
		if err := r.s.Append(ctx, "", promptAt(1, 1)); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("empty conversation id: Append = %v, want ErrInvalid", err)
		}
		if _, err := r.s.History(ctx, "", store.Page{}); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("empty conversation id: History = %v, want ErrInvalid", err)
		}
		if _, err := r.s.Context(ctx, ""); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("empty conversation id: Context = %v, want ErrInvalid", err)
		}
	})

	t.Run("RacingWritersExactlyOneWins", func(t *testing.T) {
		t.Parallel()

		// Two writers build their next entry from the same view. Whichever
		// appends second conflicts, then catches up and goes on. The order
		// is fixed by the test, both ways round.
		t.Run("FromTheSameView", func(t *testing.T) {
			t.Parallel()
			for _, aFirst := range []bool{true, false} {
				r := newConvRig(t, newStore)
				ctx := r.ctx(tenantA)
				r.mustAppend(ctx, "c", promptAt(1, 1))
				view, err := r.s.Context(ctx, "c")
				if err != nil {
					t.Fatalf("Context: %v", err)
				}
				a, b := turnAt(view.Seq+1, 10), turnAt(view.Seq+1, 20)
				first, second := a, b
				if !aFirst {
					first, second = b, a
				}
				r.mustAppend(ctx, "c", first)
				if err := r.append(ctx, "c", second); !errors.Is(err, store.ErrConflict) {
					t.Fatalf("second writer: Append = %v, want ErrConflict", err)
				}
				view, err = r.s.Context(ctx, "c")
				if err != nil {
					t.Fatalf("Context: %v", err)
				}
				if view.Seq != 2 || mustSnap(t, view.Entries[1]) != mustSnap(t, first) {
					t.Fatalf("after the race Context is %v, want the first writer's entry at 2", seqs(view.Entries))
				}
				second.Seq = view.Seq + 1
				r.mustAppend(ctx, "c", second)
			}
		})

		// Many writers released at once: whatever the schedule, exactly one
		// different entry wins and the others conflict; identical entries all
		// succeed and are stored once.
		t.Run("Concurrently", func(t *testing.T) {
			t.Parallel()
			r := newConvRig(t, newStore)
			ctx := r.ctx(tenantA)
			const writers = 8
			for round := range 10 {
				conv := fmt.Sprintf("race-%d", round)
				r.mustAppend(ctx, conv, promptAt(1, 0))
				errs := race(writers, func(w int) error { return r.append(ctx, conv, turnAt(2, w+1)) })
				var won []int
				for w, err := range errs {
					switch {
					case err == nil:
						won = append(won, w)
					case !errors.Is(err, store.ErrConflict):
						t.Errorf("round %d writer %d: Append = %v, want nil or ErrConflict", round, w, err)
					}
				}
				if len(won) != 1 {
					t.Fatalf("round %d: %d writers won seq 2 (%v), want exactly one", round, len(won), won)
				}
				got := readAll(ctx, t, r.s, conv, store.Page{})
				if len(got) != 2 || mustSnap(t, got[1]) != mustSnap(t, turnAt(2, won[0]+1)) {
					t.Errorf("round %d: stored entry 2 is not the winner's", round)
				}

				errs = race(writers, func(int) error { return r.append(ctx, conv, turnAt(3, 99)) })
				for w, err := range errs {
					if err != nil {
						t.Errorf("round %d writer %d: identical Append = %v, want nil", round, w, err)
					}
				}
			}
		})
	})

	t.Run("HistoryPagesVisitEveryEntryOnce", func(t *testing.T) {
		t.Parallel()
		for _, order := range []store.Order{store.Ascending, store.Descending} {
			for _, limit := range []int{1, 2, 3, 7, 0} {
				t.Run(fmt.Sprintf("order=%d/limit=%d", order, limit), func(t *testing.T) {
					t.Parallel()
					r := newConvRig(t, newStore)
					ctx := r.ctx(tenantA)
					n := 10
					if limit == 0 {
						n = 2*store.DefaultPageLimit + 5
					}
					for i := range n {
						r.mustAppend(ctx, "c", nextEntry(int64(i+1), i))
					}
					// Read page by page, appending an entry between the first
					// pages (all of them would keep a page of 1 from ever
					// catching up).
					initial, lastRead := n, n
					var visited []int64
					p := store.Page{Limit: limit, Order: order}
					for i := 0; ; i++ {
						lastRead = n
						page, err := r.s.History(ctx, "c", p)
						if err != nil {
							t.Fatalf("History: %v", err)
						}
						if page.Next != "" && len(page.Entries) != pageLimit(limit) {
							t.Fatalf("page with a Next holds %d entries, want %d", len(page.Entries), pageLimit(limit))
						}
						visited = append(visited, seqs(page.Entries)...)
						if i < 4 {
							n++
							r.mustAppend(ctx, "c", nextEntry(int64(n), n))
						}
						if page.Next == "" {
							break
						}
						p = store.Page{Cursor: page.Next, Limit: limit, Order: order}
					}
					// Ascending reaches every entry appended before the last
					// page was read; descending starts at the newest entry of
					// the first read and goes back to 1.
					want := descendingFrom(int64(initial))
					if order == store.Ascending {
						want = descendingFrom(int64(lastRead))
						slices.Reverse(want)
					}
					if !slices.Equal(visited, want) {
						t.Errorf("pages visited %v, want %v", visited, want)
					}
				})
			}
		}
	})

	t.Run("HistoryLimitAboveMaximumIsLowered", func(t *testing.T) {
		t.Parallel()
		r := newConvRig(t, newStore)
		ctx := r.ctx(tenantA)
		for i := range store.MaxPageLimit + 1 {
			r.mustAppend(ctx, "c", nextEntry(int64(i+1), i))
		}
		page, err := r.s.History(ctx, "c", store.Page{Limit: store.MaxPageLimit + 50})
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(page.Entries) != store.MaxPageLimit || page.Next == "" {
			t.Errorf("History with a limit above the maximum gave %d entries (next %q), want %d and a next page", len(page.Entries), page.Next, store.MaxPageLimit)
		}
	})

	t.Run("HistorySeeksBySeqAndTime", func(t *testing.T) {
		t.Parallel()
		r := newConvRig(t, newStore)
		ctx := r.ctx(tenantA)
		// Entries 3 and 4 are created at the same time.
		for i := 1; i <= 6; i++ {
			if i != 4 {
				r.clk.Advance(time.Second)
			}
			r.mustAppend(ctx, "c", nextEntry(int64(i), i))
		}
		log := readAll(ctx, t, r.s, "c", store.Page{})
		at := func(seq int64) time.Time { return log[seq-1].CreatedAt }
		between := at(2).Add(at(3).Sub(at(2)) / 2)

		for _, tc := range []struct {
			name string
			p    store.Page
			want []int64
		}{
			{"from 3 up", store.Page{FromSeq: 3}, []int64{3, 4, 5, 6}},
			{"from 3 down", store.Page{FromSeq: 3, Order: store.Descending}, []int64{3, 2, 1}},
			{"from past the end up", store.Page{FromSeq: 9}, nil},
			{"from past the end down", store.Page{FromSeq: 9, Order: store.Descending}, []int64{6, 5, 4, 3, 2, 1}},
			{"from 2 up in pages of 2", store.Page{FromSeq: 2, Limit: 2}, []int64{2, 3, 4, 5, 6}},
			{"from the time of 3 up", store.Page{FromTime: at(3)}, []int64{3, 4, 5, 6}},
			{"from the time of 3 down", store.Page{FromTime: at(3), Order: store.Descending}, []int64{4, 3, 2, 1}},
			{"from between 2 and 3 up", store.Page{FromTime: between}, []int64{3, 4, 5, 6}},
			{"from between 2 and 3 down", store.Page{FromTime: between, Order: store.Descending}, []int64{2, 1}},
			{"from before the first down", store.Page{FromTime: at(1).Add(-time.Second), Order: store.Descending}, nil},
			{"from after the last up", store.Page{FromTime: at(6).Add(time.Second)}, nil},
			{"from the time of 5 down in pages of 1", store.Page{FromTime: at(5), Order: store.Descending, Limit: 1}, []int64{5, 4, 3, 2, 1}},
		} {
			if got := seqs(readAll(ctx, t, r.s, "c", tc.p)); !slices.Equal(got, tc.want) {
				t.Errorf("%s: History visited %v, want %v", tc.name, got, tc.want)
			}
		}
	})

	t.Run("HistoryRefusesBadPages", func(t *testing.T) {
		t.Parallel()
		r := newConvRig(t, newStore)
		ctx := r.ctx(tenantA)
		for i := 1; i <= 3; i++ {
			r.mustAppend(ctx, "c", nextEntry(int64(i), i))
		}
		page, err := r.s.History(ctx, "c", store.Page{Limit: 1})
		if err != nil || page.Next == "" {
			t.Fatalf("History = %v, next %q; want a next page", err, page.Next)
		}
		for name, p := range map[string]store.Page{
			"negative limit":            {Limit: -1},
			"unknown order":             {Order: store.Order(7)},
			"garbage cursor":            {Cursor: "not a cursor"},
			"cursor of the other order": {Cursor: page.Next, Order: store.Descending},
			"cursor and seq":            {Cursor: page.Next, FromSeq: 1},
			"cursor and time":           {Cursor: page.Next, FromTime: r.clk.Now()},
			"seq and time":              {FromSeq: 1, FromTime: r.clk.Now()},
			"negative seq":              {FromSeq: -1},
		} {
			if _, err := r.s.History(ctx, "c", p); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: History = %v, want ErrInvalid", name, err)
			}
		}
	})

	t.Run("ContextStartsAtTheLatestCompaction", func(t *testing.T) {
		t.Parallel()
		r := newConvRig(t, newStore)
		ctx := r.ctx(tenantA)
		check := func(wantSeq int64, want ...int64) {
			t.Helper()
			view, err := r.s.Context(ctx, "c")
			if err != nil {
				t.Fatalf("Context: %v", err)
			}
			if view.Seq != wantSeq || !slices.Equal(seqs(view.Entries), want) {
				t.Errorf("Context = entries %v at seq %d, want %v at %d", seqs(view.Entries), view.Seq, want, wantSeq)
			}
		}
		check(0)
		r.mustAppend(ctx, "c", promptAt(1, 1), turnAt(2, 2), turnAt(3, 3))
		check(3, 1, 2, 3)
		r.mustAppend(ctx, "c", compactionAt(4, 4))
		check(4, 4)
		r.mustAppend(ctx, "c", promptAt(5, 5), turnAt(6, 6))
		check(6, 4, 5, 6)
		r.mustAppend(ctx, "c", compactionAt(7, 7), compactionAt(8, 8))
		check(8, 8)
		r.mustAppend(ctx, "c", turnAt(9, 9))
		check(9, 8, 9)
	})

	t.Run("TenantIsolation", func(t *testing.T) {
		t.Parallel()
		r := newConvRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)

		none := t.Context()
		if err := r.append(none, "c", promptAt(1, 1)); !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("Append without a tenant = %v, want ErrNoTenant", err)
		}
		if _, err := r.s.History(none, "c", store.Page{}); !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("History without a tenant = %v, want ErrNoTenant", err)
		}
		if _, err := r.s.Context(none, "c"); !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("Context without a tenant = %v, want ErrNoTenant", err)
		}

		// The same conversation id in two tenants is two conversations:
		// neither conflicts with nor sees the other.
		r.mustAppend(a, "shared", promptAt(1, 1), turnAt(2, 2), compactionAt(3, 3))
		r.mustAppend(b, "shared", promptAt(1, 100))
		r.mustAppend(a, "only-a", promptAt(1, 1))
		if err := r.append(b, "only-a", turnAt(2, 2)); !errors.Is(err, store.ErrGap) {
			t.Errorf("tenant B appending entry 2 to A's conversation = %v, want ErrGap (B's is empty)", err)
		}
		if got := readAll(b, t, r.s, "only-a", store.Page{}); len(got) != 0 {
			t.Errorf("tenant B sees %d entries of A's conversation", len(got))
		}
		if view, err := r.s.Context(b, "shared"); err != nil || view.Seq != 1 || len(view.Entries) != 1 {
			t.Errorf("tenant B's Context of its own conversation = %v at seq %d (%v), want its one entry", seqs(view.Entries), view.Seq, err)
		}
		// The invariant check reads every conversation through every tenant.
	})

	t.Run("CancelledContextIsReported", func(t *testing.T) {
		t.Parallel()
		r := newConvRig(t, newStore)
		ctx, cancel := context.WithCancel(r.ctx(tenantA))
		cancel()
		if err := r.append(ctx, "c", promptAt(1, 1)); !errors.Is(err, context.Canceled) {
			t.Errorf("Append = %v, want context.Canceled", err)
		}
		if _, err := r.s.History(ctx, "c", store.Page{}); !errors.Is(err, context.Canceled) {
			t.Errorf("History = %v, want context.Canceled", err)
		}
		if _, err := r.s.Context(ctx, "c"); !errors.Is(err, context.Canceled) {
			t.Errorf("Context = %v, want context.Canceled", err)
		}
	})

	t.Run("KeepsCopies", func(t *testing.T) {
		t.Parallel()
		r := newConvRig(t, newStore)
		ctx := r.ctx(tenantA)
		e := turnAt(1, 1)
		r.mustAppend(ctx, "c", e)
		// Scribbling over what was appended changes nothing stored. (The rig
		// took its snapshot before the append.)
		scribble(e)

		page, err := r.s.History(ctx, "c", store.Page{})
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		view, err := r.s.Context(ctx, "c")
		if err != nil {
			t.Fatalf("Context: %v", err)
		}
		// Neither does scribbling over what was read.
		for _, got := range append(page.Entries, view.Entries...) {
			scribble(got)
		}
	})

	t.Run("Simulation", func(t *testing.T) {
		t.Parallel()
		for seed := range uint64(48) {
			t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
				t.Parallel()
				SimulateConversations(t, newStore, seed)
			})
		}
	})
}

// nextEntry is the i-th entry of a simple log: a prompt first, then turns
// with an occasional compaction.
func nextEntry(seq int64, i int) store.Entry {
	switch {
	case seq == 1:
		return promptAt(seq, i)
	case seq%5 == 0:
		return compactionAt(seq, i)
	}
	return turnAt(seq, i)
}

func descendingFrom(n int64) []int64 {
	out := make([]int64, 0, n)
	for s := n; s >= 1; s-- {
		out = append(out, s)
	}
	return out
}

// race runs f for n workers released at the same moment and returns their
// errors. Any schedule is correct; the test checks what holds for all of them.
func race(n int, f func(w int) error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[w] = f(w)
		}()
	}
	close(start)
	wg.Wait()
	return errs
}

// scribble overwrites every byte and block an entry holds.
func scribble(e store.Entry) {
	for i := range e.Record {
		e.Record[i] = 'X'
	}
	for _, m := range e.Messages {
		for j, b := range m.Content {
			switch b := b.(type) {
			case llm.Opaque:
				for k := range b.Data {
					b.Data[k] = ' '
				}
			case llm.ToolUse:
				for k := range b.Input {
					b.Input[k] = ' '
				}
			}
			m.Content[j] = llm.Text{Text: "scribbled"}
		}
	}
}

func withSeq(e store.Entry, seq int64) store.Entry     { e.Seq = seq; return e }
func withKind(e store.Entry, k store.Kind) store.Entry { e.Kind = k; return e }
func withCovers(e store.Entry, c int64) store.Entry    { e.Covers = c; return e }
func withRecord(e store.Entry, rec string) store.Entry { e.Record = json.RawMessage(rec); return e }
