// Package worktest has the conformance suite every work.Store passes and
// an in-memory Store for tests.
package worktest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/work"
)

// Memory is a work.Store in memory.
type Memory struct {
	mu   sync.Mutex
	evs  []work.Event
	seen map[string]bool
}

var _ work.Store = (*Memory)(nil)

// NewMemory returns an empty Memory.
func NewMemory() *Memory { return &Memory{seen: map[string]bool{}} }

// Append implements work.Store.
func (m *Memory) Append(_ context.Context, evs ...work.Event) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range evs {
		k := e.Farm + "\x00" + e.ID
		if m.seen[k] {
			continue
		}
		m.seen[k] = true
		m.evs = append(m.evs, e)
		n++
	}
	return n, nil
}

// Events implements work.Store.
func (m *Memory) Events(_ context.Context, farmID string) ([]work.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []work.Event
	for _, e := range m.evs {
		if e.Farm == farmID {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b work.Event) int { return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.ID, b.ID)) })
	return out, nil
}

// RunStore checks a Store: open returns a new, empty one.
func RunStore(t *testing.T, open func(t *testing.T) work.Store) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 7, 1, 6, 0, 0, 0, time.UTC)
	zone := 1
	ev := func(id, farm string, min int) work.Event {
		return work.Event{ID: id, Farm: farm, Task: "z0-maize-2026-06-20-weed-1", Kind: work.Done, Day: "2026-07-01", At: at.Add(time.Duration(min) * time.Minute), By: "Lakshmi"}
	}

	t.Run("round trip keeps every field", func(t *testing.T) {
		t.Parallel()
		s := open(t)
		e := work.Event{
			ID: "a1", Farm: "f1", Kind: work.Added, Day: "2026-07-03", At: at, By: "Ravi", Note: "ask the dealer",
			Title: "Buy urea", TaskKind: "fertilise", Zone: &zone, Actuals: &work.Actuals{Quantity: 2, Unit: "bags", CostINR: 534, Workers: 1},
		}
		if n, err := s.Append(ctx, e); err != nil || n != 1 {
			t.Fatalf("Append = %d, %v", n, err)
		}
		got, err := s.Events(ctx, "f1")
		if err != nil || len(got) != 1 {
			t.Fatalf("Events = %v, %v", got, err)
		}
		g := got[0]
		if g.ID != e.ID || g.Farm != e.Farm || g.Kind != e.Kind || g.Day != e.Day || !g.At.Equal(e.At) || g.By != e.By || g.Note != e.Note ||
			g.Title != e.Title || g.TaskKind != e.TaskKind || g.Zone == nil || *g.Zone != 1 || g.Actuals == nil || *g.Actuals != *e.Actuals {
			t.Errorf("got %+v, want %+v", g, e)
		}
	})

	t.Run("an event ID is recorded once", func(t *testing.T) {
		t.Parallel()
		s := open(t)
		if n, err := s.Append(ctx, ev("e1", "f1", 0), ev("e2", "f1", 1)); err != nil || n != 2 {
			t.Fatalf("Append = %d, %v", n, err)
		}
		changed := ev("e1", "f1", 5)
		changed.Kind = work.Skipped
		if n, err := s.Append(ctx, changed, ev("e2", "f1", 1), ev("e3", "f1", 2)); err != nil || n != 1 {
			t.Fatalf("resend = %d, %v; want 1 new", n, err)
		}
		got, _ := s.Events(ctx, "f1")
		if len(got) != 3 || got[0].Kind != work.Done {
			t.Errorf("events = %+v; want e1 unchanged and three in all", got)
		}
	})

	t.Run("an event ID is per farm", func(t *testing.T) {
		t.Parallel()
		s := open(t)
		if n, err := s.Append(ctx, ev("same", "f1", 0), ev("same", "f2", 0)); err != nil || n != 2 {
			t.Fatalf("Append = %d, %v; want 2: IDs are made on phones and only unique per farm", n, err)
		}
		for _, f := range []string{"f1", "f2"} {
			if got, _ := s.Events(ctx, f); len(got) != 1 {
				t.Errorf("%s: %d events; want 1", f, len(got))
			}
		}
	})

	t.Run("duplicates in one batch are recorded once", func(t *testing.T) {
		t.Parallel()
		s := open(t)
		if n, err := s.Append(ctx, ev("d1", "f1", 0), ev("d1", "f1", 0)); err != nil || n != 1 {
			t.Fatalf("Append = %d, %v; want 1", n, err)
		}
	})

	t.Run("events come back per farm in recorded order", func(t *testing.T) {
		t.Parallel()
		s := open(t)
		if _, err := s.Append(ctx, ev("c", "f1", 2), ev("b", "f2", 0), ev("a", "f1", 2), ev("z", "f1", 1)); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Events(ctx, "f1")
		var ids []string
		for _, e := range got {
			ids = append(ids, e.ID)
		}
		if !slices.Equal(ids, []string{"z", "a", "c"}) {
			t.Errorf("order = %v; want [z a c] (by time, then ID)", ids)
		}
		if none, _ := s.Events(ctx, "nobody"); len(none) != 0 {
			t.Errorf("unknown farm: %v", none)
		}
	})

	t.Run("concurrent appends of the same IDs record each once", func(t *testing.T) {
		t.Parallel()
		s := open(t)
		var wg sync.WaitGroup
		total := make(chan int, 8)
		for range 8 {
			wg.Go(func() {
				var evs []work.Event
				for i := range 10 {
					evs = append(evs, ev(fmt.Sprintf("p%d", i), "f1", i))
				}
				n, err := s.Append(ctx, evs...)
				if err != nil {
					t.Error(err)
				}
				total <- n
			})
		}
		wg.Wait()
		close(total)
		sum := 0
		for n := range total {
			sum += n
		}
		got, _ := s.Events(ctx, "f1")
		if sum != 10 || len(got) != 10 {
			t.Errorf("recorded %d, stored %d; want 10 each", sum, len(got))
		}
	})
}
