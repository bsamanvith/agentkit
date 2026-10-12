package storetest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// eventRig is the one helper every event log test and the simulation go
// through. It records every event the store accepted and, when the test ends,
// checks the event log invariants on every run the test touched:
//
//  1. Dense: each run's events are numbered 1..n with no gap, and the accepted
//     events are exactly those positions.
//  2. Unchanged: every event reads back with the Type and Data bytes it was
//     first accepted with.
//  3. Times: CreatedAt is set and never decreases along the log.
//  4. EventsAfter, paged from 0 and from points along the log, gives the
//     same events; LastEventSeq is the log's length.
//  5. Tenants: every run is read through every tenant the test used plus one
//     that never wrote; each sees only its own events.
type eventRig struct {
	t   *testing.T
	s   store.EventLog
	clk *skewedClock

	mu       sync.Mutex
	accepted map[logKey]map[int64]eventSnap // logKey.conv is the run id
	runs     map[string]bool
	tenants  map[string]bool
}

// eventSnap is what an event must read back as: Seq and CreatedAt aside, its
// bytes.
type eventSnap struct{ Type, Data string }

func snapEvent(e store.Event) eventSnap { return eventSnap{e.Type, string(e.Data)} }

func newEventRig(t *testing.T, newLog func(t *testing.T, clk clock.Clock) store.EventLog) *eventRig {
	t.Helper()
	clk := &skewedClock{Fake: clocktest.NewFake(time.Time{})}
	r := &eventRig{
		t:        t,
		s:        newLog(t, clk),
		clk:      clk,
		accepted: map[logKey]map[int64]eventSnap{},
		runs:     map[string]bool{},
		tenants:  map[string]bool{unusedTenant: true},
	}
	t.Cleanup(r.check)
	return r
}

// ctx returns a context of tenant id, derived from the test's context.
func (r *eventRig) ctx(id string) context.Context {
	r.mu.Lock()
	r.tenants[id] = true
	r.mu.Unlock()
	return tenantCtx(r.t.Context(), r.t, id)
}

// append calls AppendEvents and records the accepted events. It is safe for
// concurrent use.
func (r *eventRig) append(ctx context.Context, run string, events ...store.Event) error {
	want := make([]eventSnap, len(events)) // before the call: the caller may scribble later
	for i, e := range events {
		want[i] = snapEvent(e)
	}
	err := r.s.AppendEvents(ctx, run, events)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[run] = true
	if err != nil || len(events) == 0 {
		return err
	}
	tn, ok := tenancy.From(ctx)
	if !ok {
		r.t.Errorf("AppendEvents to %q succeeded without a tenant", run)
		return err
	}
	k := logKey{tn.ID(), run}
	if r.accepted[k] == nil {
		r.accepted[k] = map[int64]eventSnap{}
	}
	for i, e := range events {
		if prev, ok := r.accepted[k][e.Seq]; ok && prev != want[i] {
			r.t.Errorf("tenant %s run %q: AppendEvents accepted a different event at taken seq %d", k.tenant, run, e.Seq)
		} else if !ok {
			r.accepted[k][e.Seq] = want[i]
		}
	}
	return err
}

// mustAppend appends one batch and fails the test on an error.
func (r *eventRig) mustAppend(ctx context.Context, run string, events ...store.Event) {
	r.t.Helper()
	if err := r.append(ctx, run, events...); err != nil {
		r.t.Fatalf("AppendEvents %q %v: %v", run, eventSeqs(events), err)
	}
}

// readEvents reads a run's log after after, limit events at a time, checking
// each read's shape on the way.
func readEvents(ctx context.Context, t *testing.T, s store.EventLog, run string, after int64, limit int) []store.Event {
	t.Helper()
	n := pageLimit(limit)
	var out []store.Event
	for range 100_000 {
		got, err := s.EventsAfter(ctx, run, after, limit)
		if err != nil {
			t.Fatalf("EventsAfter %q %d %d: %v", run, after, limit, err)
		}
		if len(got) > n {
			t.Fatalf("EventsAfter %q %d %d: %d events, more than the limit %d", run, after, limit, len(got), n)
		}
		for i, e := range got {
			if e.Seq != after+int64(i)+1 {
				t.Fatalf("EventsAfter %q %d: event %d has Seq %d (seqs %v)", run, after, i, e.Seq, eventSeqs(got))
			}
		}
		out = append(out, got...)
		if len(got) < n {
			return out
		}
		after += int64(len(got))
	}
	t.Fatalf("EventsAfter %q never ended", run)
	return nil
}

func (r *eventRig) check() {
	r.mu.Lock()
	defer r.mu.Unlock()
	runs := sortedKeys(r.runs)
	for _, tenant := range sortedKeys(r.tenants) {
		// t.Context is done once cleanup runs.
		ctx := tenantCtx(context.Background(), r.t, tenant)
		for _, run := range runs {
			r.checkLog(ctx, tenant, run)
		}
	}
}

func (r *eventRig) checkLog(ctx context.Context, tenant, run string) {
	t := r.t
	if run == "" || !textOK(run) {
		return // a run id no store can hold; the tests read it themselves
	}
	accepted := r.accepted[logKey{tenant, run}]
	for seq := range accepted {
		if seq < 1 || seq > int64(len(accepted)) {
			t.Errorf("tenant %s run %q: AppendEvents accepted event %d, leaving a gap (%d events accepted)", tenant, run, seq, len(accepted))
		}
	}
	log := readEvents(ctx, t, r.s, run, 0, 7)
	if len(log) != len(accepted) {
		t.Errorf("tenant %s run %q: the log has %d events, %d were accepted", tenant, run, len(log), len(accepted))
	}
	for i, e := range log {
		if want, ok := accepted[e.Seq]; !ok || snapEvent(e) != want {
			t.Errorf("tenant %s run %q: event %d reads back as %+v, want what was accepted %+v", tenant, run, e.Seq, snapEvent(e), want)
		}
		if e.CreatedAt.IsZero() {
			t.Errorf("tenant %s run %q: event %d has no CreatedAt", tenant, run, e.Seq)
		}
		if i > 0 && e.CreatedAt.Before(log[i-1].CreatedAt) {
			t.Errorf("tenant %s run %q: event %d was created at %v, before event %d at %v", tenant, run, e.Seq, e.CreatedAt, i, log[i-1].CreatedAt)
		}
	}
	for _, after := range []int64{1, int64(len(log)) / 2, int64(len(log)) - 1, int64(len(log)), int64(len(log)) + 5} {
		if after < 0 || after > int64(len(log))+5 {
			continue
		}
		want := log[min(after, int64(len(log))):]
		if got := readEvents(ctx, t, r.s, run, after, 0); !sameEvents(got, want) {
			t.Errorf("tenant %s run %q: EventsAfter %d gives %v, want %v", tenant, run, after, eventSeqs(got), eventSeqs(want))
		}
	}
	last, err := r.s.LastEventSeq(ctx, run)
	if err != nil || last != int64(len(log)) {
		t.Errorf("tenant %s run %q: LastEventSeq = %d, %v; want %d", tenant, run, last, err, len(log))
	}
}

// sameEvents compares events by Seq, CreatedAt and bytes.
func sameEvents(a, b []store.Event) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Seq != b[i].Seq || !a[i].CreatedAt.Equal(b[i].CreatedAt) || snapEvent(a[i]) != snapEvent(b[i]) {
			return false
		}
	}
	return true
}

func eventSeqs(es []store.Event) []int64 {
	out := make([]int64, len(es))
	for i, e := range es {
		out[i] = e.Seq
	}
	return out
}

// Event data is chosen so that any re-encoding shows in its bytes: unsorted
// keys, whitespace, number literals a generic decoder rewrites, escapes,
// HTML characters and non-ASCII text.
func eventData(i int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf("{ \"z\": %d,\n\t\"a\" : [1.50, 1e2, -0],  \"s\": \"<b>\\u00e9 é\\n\" }", i))
}

// eventAt is the i-th event of a test, at seq.
func eventAt(seq int64, i int) store.Event {
	types := []string{"tool_call", "interrupt", "turn", "run_end"}
	return store.Event{Seq: seq, Type: types[i%len(types)], Data: eventData(i)}
}

// eventsFrom is n consecutive events from seq, the i-th test events onwards.
func eventsFrom(seq int64, n, i int) []store.Event {
	out := make([]store.Event, n)
	for j := range out {
		out[j] = eventAt(seq+int64(j), i+j)
	}
	return out
}

// textOK mirrors the stores' rule on text: UTF-8 with no NUL byte.
func textOK(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }
