// Package flowtest provides a conformance suite for [flow.Checkpointer]
// implementations and a recorder for [flow.Event]s.
//
// Every Checkpointer (the in-memory one, the SQLite store) runs the same suite,
// so they behave the same way:
//
//	func TestConformance(t *testing.T) {
//		flowtest.RunCheckpointer(t, func(t *testing.T) flow.Checkpointer {
//			return newMyCheckpointer(t)
//		})
//	}
package flowtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/flow"
)

// Recorder collects events. Pass its Observe method to [flow.WithObserver].
type Recorder struct {
	mu     sync.Mutex
	events []flow.Event
}

// Observe records e.
func (r *Recorder) Observe(e flow.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// Events returns the events recorded so far, in order.
func (r *Recorder) Events() []flow.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]flow.Event(nil), r.events...)
}

// Summary returns the events as "kind path" strings, for comparing in tests.
func (r *Recorder) Summary() []string {
	var out []string
	for _, e := range r.Events() {
		out = append(out, fmt.Sprintf("%s %s", e.Kind, e.Path))
	}
	return out
}

func sample(runID string, n int) flow.Checkpoint {
	return flow.Checkpoint{
		Version: flow.CheckpointVersion,
		RunID:   runID,
		Status:  flow.StatusSuspended,
		Initial: json.RawMessage(`{"n":0}`),
		Steps: map[string]json.RawMessage{
			"main/a": json.RawMessage(fmt.Sprintf(`{"n":%d}`, n)),
			"main/b": json.RawMessage(`{"n":2,"s":"x"}`),
		},
		Rejected: map[string]string{"main/ok": "no"},
		Pending:  []string{"main/ask"},
	}
}

// RunCheckpointer runs the conformance suite. newCheckpointer must return an
// empty checkpointer for each call.
func RunCheckpointer(t *testing.T, newCheckpointer func(t *testing.T) flow.Checkpointer) {
	t.Helper()

	t.Run("SaveThenLoadRoundTrips", func(t *testing.T) {
		t.Parallel()
		cp := newCheckpointer(t)
		want := sample("r1", 1)
		if err := cp.Save(t.Context(), "r1", want); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := cp.Load(t.Context(), "r1")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !equal(got, want) {
			t.Errorf("Load = %+v, want %+v", got, want)
		}
	})

	t.Run("LoadMissingIsErrNotFound", func(t *testing.T) {
		t.Parallel()
		_, err := newCheckpointer(t).Load(t.Context(), "nope")
		if !errors.Is(err, flow.ErrNotFound) {
			t.Errorf("Load error = %v, want ErrNotFound", err)
		}
	})

	t.Run("SaveReplacesTheEarlierCheckpoint", func(t *testing.T) {
		t.Parallel()
		cp := newCheckpointer(t)
		first := sample("r1", 1)
		second := sample("r1", 2)
		second.Status = flow.StatusCompleted
		second.Rejected, second.Pending = nil, nil
		delete(second.Steps, "main/b")
		for _, c := range []flow.Checkpoint{first, second} {
			if err := cp.Save(t.Context(), "r1", c); err != nil {
				t.Fatalf("Save: %v", err)
			}
		}
		got, err := cp.Load(t.Context(), "r1")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !equal(got, second) {
			t.Errorf("Load = %+v, want the second checkpoint %+v", got, second)
		}
	})

	t.Run("RunsAreIndependent", func(t *testing.T) {
		t.Parallel()
		cp := newCheckpointer(t)
		a, b := sample("a", 1), sample("b", 2)
		for id, c := range map[string]flow.Checkpoint{"a": a, "b": b} {
			if err := cp.Save(t.Context(), id, c); err != nil {
				t.Fatalf("Save %s: %v", id, err)
			}
		}
		for id, want := range map[string]flow.Checkpoint{"a": a, "b": b} {
			got, err := cp.Load(t.Context(), id)
			if err != nil {
				t.Fatalf("Load %s: %v", id, err)
			}
			if !equal(got, want) {
				t.Errorf("Load %s = %+v, want %+v", id, got, want)
			}
		}
	})

	t.Run("StoresCopies", func(t *testing.T) {
		t.Parallel()
		cp := newCheckpointer(t)
		saved := sample("r1", 1)
		want := saved.Clone()
		if err := cp.Save(t.Context(), "r1", saved); err != nil {
			t.Fatalf("Save: %v", err)
		}
		// Changing what was saved must not change the stored checkpoint.
		saved.Steps["main/a"][0] = 'X'
		saved.Steps["main/new"] = json.RawMessage(`{}`)
		saved.Pending[0] = "changed"
		saved.Rejected["main/ok"] = "changed"

		loaded, err := cp.Load(t.Context(), "r1")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !equal(loaded, want) {
			t.Fatalf("modifying a saved checkpoint changed the store: %+v", loaded)
		}
		// Changing what was loaded must not change the stored checkpoint either.
		loaded.Steps["main/a"][0] = 'Y'
		loaded.Pending[0] = "changed"
		delete(loaded.Rejected, "main/ok")
		again, err := cp.Load(t.Context(), "r1")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !equal(again, want) {
			t.Errorf("modifying a loaded checkpoint changed the store: %+v", again)
		}
	})

	t.Run("ConcurrentSavesAndLoads", func(t *testing.T) {
		t.Parallel()
		cp := newCheckpointer(t)
		const n = 8
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := sample("r1", i)
				if err := cp.Save(t.Context(), "r1", c); err != nil {
					t.Errorf("Save %d: %v", i, err)
				}
				if _, err := cp.Load(t.Context(), "r1"); err != nil {
					t.Errorf("Load %d: %v", i, err)
				}
			}()
		}
		wg.Wait()
		got, err := cp.Load(t.Context(), "r1")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		// Whichever save won, the checkpoint must be one of them whole, not a
		// mixture.
		for i := range n {
			if equal(got, sample("r1", i)) {
				return
			}
		}
		t.Errorf("Load = %+v, which is none of the saved checkpoints", got)
	})

	t.Run("CancelledContextIsReported", func(t *testing.T) {
		t.Parallel()
		cp := newCheckpointer(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := cp.Save(ctx, "r1", sample("r1", 1)); !errors.Is(err, context.Canceled) {
			t.Errorf("Save error = %v, want context.Canceled", err)
		}
		if _, err := cp.Load(ctx, "r1"); !errors.Is(err, context.Canceled) {
			t.Errorf("Load error = %v, want context.Canceled", err)
		}
	})
}

// equal compares checkpoints by value, treating nil and empty collections as
// the same (a store may not preserve the difference).
func equal(a, b flow.Checkpoint) bool {
	norm := func(c flow.Checkpoint) flow.Checkpoint {
		c = c.Clone()
		if len(c.Rejected) == 0 {
			c.Rejected = nil
		}
		if len(c.Pending) == 0 {
			c.Pending = nil
		}
		return c
	}
	a, b = norm(a), norm(b)
	if a.Version != b.Version || a.RunID != b.RunID || a.Status != b.Status ||
		!reflect.DeepEqual(a.Rejected, b.Rejected) || !reflect.DeepEqual(a.Pending, b.Pending) ||
		len(a.Steps) != len(b.Steps) || !jsonEqual(a.Initial, b.Initial) {
		return false
	}
	for k, v := range a.Steps {
		w, ok := b.Steps[k]
		if !ok || !jsonEqual(v, w) {
			return false
		}
	}
	return true
}

// jsonEqual compares two JSON documents by value, so a store that reformats
// JSON (a database column, say) still passes.
func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
