package agentapi

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// hub is the event log of a run this server executes: the events of the run
// so far (replayed from the store for a resumed run, then the live ones), in
// memory, for every client that follows the run. It is how a client that
// went away comes back to a run within the reconnect grace, and how a run's
// events outlive its stream for GET /v1/runs/{id}/events.
//
// Single replica: a run executed by another process has no hub here, and is
// replayed from the store instead.
type hub struct {
	runID, convID string
	tenant        tenancy.Tenant
	interactive   bool // the grace applies

	mu       sync.Mutex
	events   []Event
	ids      []bus.ID      // events' IDs, increasing
	done     bool          // the execution ended; events holds its last event
	ended    chan struct{} // closed when done is set
	wake     chan struct{} // closed and replaced on every append
	subs     int           // clients following the run
	graceOff chan struct{} // closes the grace timer's goroutine; nil when it is not armed
	finished time.Time     // when done was set, for retention
}

func newHub(runID, convID string, tenant tenancy.Tenant, interactive bool, seed []Event) *hub {
	h := &hub{
		runID: runID, convID: convID, tenant: tenant, interactive: interactive,
		ended: make(chan struct{}), wake: make(chan struct{}),
	}
	for _, e := range seed {
		h.add(e)
	}
	return h
}

// append adds e, whose ID comes after every event's, to the log and wakes
// the followers.
func (h *hub) append(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.done {
		return
	}
	h.add(e)
	close(h.wake)
	h.wake = make(chan struct{})
}

// add adds e to the log. Call with h.mu held, or before h is shared.
func (h *hub) add(e Event) {
	if n := len(h.events); n > 0 && e.At.Before(h.events[n-1].At) {
		e.At = h.events[n-1].At // times never go back, whoever stamped them
	}
	h.events = append(h.events, e)
	h.ids = append(h.ids, eventID(e))
}

// finish ends the log.
func (h *hub) finish(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.done {
		return
	}
	h.done, h.finished = true, now
	if h.graceOff != nil {
		close(h.graceOff)
		h.graceOff = nil
	}
	close(h.ended)
	close(h.wake)
	h.wake = make(chan struct{})
}

// snapshot returns the events so far and whether the log has ended.
func (h *hub) snapshot() ([]Event, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.events[:len(h.events):len(h.events)], h.done
}

// isDone reports whether the execution has ended.
func (h *hub) isDone() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.done
}

// next waits for the events after the ID after: it returns them as soon as
// there are any, or nil and true once the log has ended without more, or nil
// and false when ctx is done.
func (h *hub) next(ctx context.Context, after bus.ID) ([]Event, bool) {
	for {
		h.mu.Lock()
		from := sort.Search(len(h.ids), func(i int) bool { return h.ids[i].Compare(after) > 0 })
		evs, done, wake := h.events[from:len(h.events):len(h.events)], h.done, h.wake
		h.mu.Unlock()
		switch {
		case len(evs) > 0:
			return evs, false
		case done:
			return nil, true
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return nil, false
		}
	}
}

// subscribe counts a client following the run, and disarms the grace.
func (h *hub) subscribe() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs++
	if h.graceOff != nil {
		close(h.graceOff)
		h.graceOff = nil
	}
}

// unsubscribe counts a client gone. When the last one goes while an
// interactive run is going on, it returns a channel the grace waits on
// (closed if a client comes back or the run ends), and true.
func (h *hub) unsubscribe() (<-chan struct{}, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs--
	if h.subs > 0 || h.done || !h.interactive || h.graceOff != nil {
		return nil, false
	}
	h.graceOff = make(chan struct{})
	return h.graceOff, true
}

// abandoned reports whether, with the grace over, nobody follows the run and
// it is still going on.
func (h *hub) abandoned() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.subs == 0 && !h.done
}
