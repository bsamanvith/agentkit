package backend

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/store"
)

// CheckEvents checks a batch for [store.EventLog.AppendEvents]: each event
// against the rules on [store.Event], and their Seqs consecutive. It returns
// copies with empty Data as nil and CreatedAt zero, sharing no memory with
// events. Errors match [store.ErrInvalid].
func CheckEvents(events []store.Event) ([]store.Event, error) {
	out := make([]store.Event, len(events))
	for i, e := range events {
		if e.Seq < 1 {
			return nil, fmt.Errorf("%w: event seq %d is not positive", store.ErrInvalid, e.Seq)
		}
		if i > 0 && e.Seq != events[i-1].Seq+1 {
			return nil, fmt.Errorf("%w: event %d follows event %d; a batch's seqs are consecutive", store.ErrInvalid, e.Seq, events[i-1].Seq)
		}
		if e.Type == "" {
			return nil, fmt.Errorf("%w: event %d has no type", store.ErrInvalid, e.Seq)
		}
		if err := CheckText(fmt.Sprintf("event %d's type", e.Seq), e.Type); err != nil {
			return nil, err
		}
		if len(e.Data) > 0 && !json.Valid(e.Data) {
			return nil, fmt.Errorf("%w: event %d: data is not valid JSON", store.ErrInvalid, e.Seq)
		}
		if err := CheckTextBytes(fmt.Sprintf("event %d's data", e.Seq), e.Data); err != nil {
			return nil, err
		}
		out[i] = store.Event{Seq: e.Seq, Type: e.Type, Data: clone(e.Data)}
	}
	return out, nil
}

// SameEvent reports whether two events are identical, which makes an append
// at a taken position a no-op. Seq and CreatedAt are not compared.
func SameEvent(a, b store.Event) bool {
	return a.Type == b.Type && bytes.Equal(a.Data, b.Data)
}

// NewEvents decides a batch that [CheckEvents] accepted against its run's log.
// last is the log's last Seq, and taken holds the stored events at the
// batch's positions up to last, in order (none if the batch starts after
// last). It returns the events to write, which start at last+1, or
// [store.ErrGap] or [store.ErrConflict].
func NewEvents(runID string, events []store.Event, last int64, taken []store.Event) ([]store.Event, error) {
	if len(events) == 0 {
		return nil, nil
	}
	first := events[0].Seq
	if first > last+1 {
		return nil, fmt.Errorf("%w: run %q has %d events, got event %d", store.ErrGap, runID, last, first)
	}
	n := int(min(last-first+1, int64(len(events))))
	if n < 0 {
		n = 0
	}
	if len(taken) != n {
		return nil, fmt.Errorf("event log of run %q: read %d stored events at %d, want %d", runID, len(taken), first, n)
	}
	for i, e := range events[:n] {
		if !SameEvent(e, taken[i]) {
			return nil, fmt.Errorf("%w: run %q already has a different event %d", store.ErrConflict, runID, e.Seq)
		}
	}
	return events[n:], nil
}

// EventLimit reads an [store.EventLog.EventsAfter] limit as [Limit] reads a
// page's, and checks after.
func EventLimit(after int64, limit int) (int, error) {
	if after < 0 {
		return 0, fmt.Errorf("%w: negative event seq %d to read after", store.ErrInvalid, after)
	}
	return Limit(store.Page{Limit: limit})
}
