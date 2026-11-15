// Package cycletest has the conformance suite every cycle.Store passes and
// an in-memory Store for tests.
package cycletest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// Memory is a cycle.Store in memory.
type Memory struct {
	mu   sync.Mutex
	recs map[string]cycle.Record
}

var _ cycle.Store = (*Memory)(nil)

// NewMemory returns an empty Memory.
func NewMemory() *Memory { return &Memory{recs: map[string]cycle.Record{}} }

// Get implements cycle.Store.
func (m *Memory) Get(_ context.Context, farmID string) (cycle.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[farmID]
	if !ok {
		return cycle.Record{Farm: farmID}, nil
	}
	return Clone(r), nil
}

// Put implements cycle.Store.
func (m *Memory) Put(_ context.Context, r cycle.Record) (cycle.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recs[r.Farm].Version != r.Version {
		return cycle.Record{}, cycle.ErrConflict
	}
	r = Clone(r)
	r.Version++
	m.recs[r.Farm] = r
	return Clone(r), nil
}

// Clone returns a deep copy of r.
func Clone(r cycle.Record) cycle.Record {
	b, err := json.Marshal(r)
	if err != nil {
		panic(err) // a Record always marshals
	}
	var out cycle.Record
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

// RunStore checks a Store: open returns a new, empty one.
func RunStore(t *testing.T, open func(t *testing.T) cycle.Store) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 15, 6, 0, 0, 0, time.UTC)
	ring := geo.Ring{{Lat: 14.47, Lng: 78.82}, {Lat: 14.47, Lng: 78.821}, {Lat: 14.471, Lng: 78.821}}
	full := func(farm string) cycle.Record {
		r := cycle.Record{Farm: farm, FutureOff: true, UpdatedAt: at}
		r.Slots[0] = cycle.Slot{Method: cycle.Organic, Sown: "2026-11-01", SownFor: "maize+@2026-10-28;"}
		r.Slots[1] = cycle.Slot{Plan: &agro.Plan{Zones: []agro.Zone{{Crop: "chickpea", Sow: "2027-03-01", Ring: ring, RowCM: 30}}}}
		r.Slots[2] = cycle.Slot{Method: cycle.Conventional}
		return r
	}

	t.Run("an unknown plot has an empty record", func(t *testing.T) {
		t.Parallel()
		s := open(t)
		r, err := s.Get(ctx, "f1")
		if err != nil || r.Farm != "f1" || r.Version != 0 || r.FutureOff || r.Slots != [cycle.MaxCycles]cycle.Slot{} {
			t.Errorf("Get = %+v, %v; want an empty record for f1", r, err)
		}
	})

	t.Run("round trip keeps every field and bumps the version", func(t *testing.T) {
		t.Parallel()
		s := open(t)
		in := full("f1")
		got, err := s.Put(ctx, in)
		if err != nil || got.Version != 1 {
			t.Fatalf("Put = %+v, %v; want version 1", got, err)
		}
		back, err := s.Get(ctx, "f1")
		if err != nil {
			t.Fatal(err)
		}
		in.Version = 1
		if !same(back, in) || !same(got, in) {
			t.Errorf("stored %+v, want %+v", back, in)
		}
	})

	t.Run("a stale version is a conflict and changes nothing", func(t *testing.T) {
		t.Parallel()
		s := open(t)
		first, err := s.Put(ctx, full("f1"))
		if err != nil {
			t.Fatal(err)
		}
		stale := full("f1") // version 0, but 1 is stored
		stale.FutureOff = false
		if _, err := s.Put(ctx, stale); !errors.Is(err, cycle.ErrConflict) {
			t.Errorf("stale Put = %v; want ErrConflict", err)
		}
		ahead := first
		ahead.Version = 7
		if _, err := s.Put(ctx, ahead); !errors.Is(err, cycle.ErrConflict) {
			t.Errorf("Put ahead = %v; want ErrConflict", err)
		}
		if back, _ := s.Get(ctx, "f1"); !same(back, first) {
			t.Errorf("after conflicts %+v; want %+v", back, first)
		}
		next := first
		next.FutureOff = false
		if got, err := s.Put(ctx, next); err != nil || got.Version != 2 || got.FutureOff {
			t.Errorf("Put at the stored version = %+v, %v", got, err)
		}
	})

	t.Run("records are per plot", func(t *testing.T) {
		t.Parallel()
		s := open(t)
		if _, err := s.Put(ctx, full("f1")); err != nil {
			t.Fatal(err)
		}
		if r, _ := s.Get(ctx, "f2"); r.Version != 0 || r.FutureOff {
			t.Errorf("f2 = %+v; want empty", r)
		}
		if _, err := s.Put(ctx, cycle.Record{Farm: "f2"}); err != nil {
			t.Errorf("f2's first Put = %v", err)
		}
	})

	t.Run("what comes back shares no memory", func(t *testing.T) {
		t.Parallel()
		s := open(t)
		in := full("f1")
		got, err := s.Put(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		in.Slots[1].Plan.Zones[0].Crop = "scribbled"
		got.Slots[1].Plan.Zones[0].Ring[0].Lat = 0
		back, _ := s.Get(ctx, "f1")
		back.Slots[1].Plan.Zones[0].Sow = "scribbled"
		again, _ := s.Get(ctx, "f1")
		z := again.Slots[1].Plan.Zones[0]
		if z.Crop != "chickpea" || z.Ring[0].Lat != 14.47 || z.Sow != "2027-03-01" {
			t.Errorf("stored zone changed by a caller: %+v", z)
		}
	})

	t.Run("concurrent writers at one version: one wins", func(t *testing.T) {
		t.Parallel()
		s := open(t)
		var wg sync.WaitGroup
		wins := make(chan int, 8)
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := cycle.Record{Farm: "f1"}
				r.Slots[2].Method = cycle.Organic
				if _, err := s.Put(ctx, r); err == nil {
					wins <- i
				} else if !errors.Is(err, cycle.ErrConflict) {
					t.Errorf("Put = %v", err)
				}
			}()
		}
		wg.Wait()
		close(wins)
		if n := len(wins); n != 1 {
			t.Errorf("%d writers won at version 0; want 1", n)
		}
		if r, _ := s.Get(ctx, "f1"); r.Version != 1 {
			t.Errorf("version %d; want 1", r.Version)
		}
	})
}

func same(a, b cycle.Record) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb) && a.UpdatedAt.Equal(b.UpdatedAt)
}
