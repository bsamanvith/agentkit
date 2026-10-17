package agentapi

import (
	"context"
	"slices"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Shared watches (T103). A run another process holds is followed here
// through one watch per run, whoever asks for it: the event streams of its
// viewers (GET /v1/runs/{id}/events) and the stops and cancels sent through
// this server. The watch holds the one bus subscription and the one
// held-check ([Server.watchRun]); each viewer reads it through a bounded
// buffer of its own, from its own position:
//
//   - the store's events up to where the watch's memory starts, when the
//     viewer starts before it (with the text of the turns they hold, as a
//     run read from the store streams it);
//   - the events the watch has kept since (its ring);
//   - then the watch's new events, as they come.
//
// A viewer whose buffer is full when an event comes is dropped from the
// watch and joins it again from the last event it sent, so a slow viewer
// never holds up the others, and gets every durable event once, in order.
// The watch stops when its last viewer leaves, and when the run ends, is
// let go without a last event, or the watch fails.

const (
	// defaultWatchBuffer is how many events a viewer of a shared watch can
	// be behind before it is dropped to the log.
	defaultWatchBuffer = 256
	// maxWatchRing is how many events a shared watch keeps for viewers that
	// join or come back; past it the oldest turns go, and are read from the
	// store.
	maxWatchRing = 4096
)

// sharedWatch is the one watch of a run on this server.
type sharedWatch struct {
	key    hubKey
	buffer int
	cancel context.CancelFunc
	exited chan struct{} // closed when the watch's goroutine has returned

	mu        sync.Mutex
	ring      []Event // the events after ringStart, in order
	ringStart bus.ID
	viewers   map[*viewer]struct{} // the viewers it sends to
	members   int                  // viewers that joined and have not left, those dropped included
	done      bool                 // the watch ended, or is ending; viewers is empty
	finished  bool                 // it ended with the run's last event, which ring holds
}

// viewer is one reader of a shared watch.
type viewer struct {
	ch      chan Event
	dropped bool // set, under the watch's mu, before ch is closed for falling behind
}

func newSharedWatch(key hubKey, buffer int, start bus.ID) *sharedWatch {
	return &sharedWatch{key: key, buffer: buffer, ringStart: start, viewers: map[*viewer]struct{}{}, exited: make(chan struct{})}
}

// join adds a viewer. It returns false if the watch has ended without the
// run's last event; else the viewer, where the ring starts, and the ring's
// events so far. Events that come later go to the viewer's channel, closed
// at once if the watch has ended: a viewer that fell behind as the run
// ended still reads it from the ring.
func (w *sharedWatch) join() (*viewer, bus.ID, []Event, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done && !w.finished {
		return nil, bus.ID{}, nil, false
	}
	v := &viewer{ch: make(chan Event, w.buffer)}
	if w.done {
		close(v.ch)
	} else {
		w.viewers[v] = struct{}{}
		w.members++
	}
	return v, w.ringStart, w.ring[:len(w.ring):len(w.ring)], true
}

// leave removes v, and reports whether the watch is left without viewers
// while it goes on: then it is ending, and nobody joins it any more. A
// viewer dropped for falling behind is still one until it leaves, so the
// watch goes on while it joins again.
func (w *sharedWatch) leave(v *viewer) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return false
	}
	delete(w.viewers, v)
	w.members--
	if w.members > 0 {
		return false
	}
	w.done = true
	for v := range w.viewers {
		close(v.ch)
	}
	clear(w.viewers)
	return true
}

// wasDropped reports whether v's channel was closed for falling behind.
func (w *sharedWatch) wasDropped(v *viewer) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return v.dropped
}

// publish keeps e, whose ID comes after every event's so far, and sends it
// to every viewer, dropping those whose buffer is full.
func (w *sharedWatch) publish(e Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ring = append(w.ring, e)
	w.finished = isTerminal(e.Type)
	if len(w.ring) > maxWatchRing {
		w.trim()
	}
	for v := range w.viewers {
		select {
		case v.ch <- e:
		default:
			v.dropped = true
			close(v.ch)
			delete(w.viewers, v)
		}
	}
}

// trim drops the older half of the ring, up to a stored event a turn starts
// after where there is one, so that the store has the text of the turns
// dropped. Call with w.mu held.
func (w *sharedWatch) trim() {
	cut := len(w.ring) / 2
	if i := slices.IndexFunc(w.ring[cut:], func(e Event) bool { return bounds(e, eventID(e)) }); i >= 0 {
		cut += i
	}
	w.ringStart = eventID(w.ring[cut])
	w.ring = slices.Clone(w.ring[cut+1:])
}

// end closes every viewer's channel: the watch is over.
func (w *sharedWatch) end() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return
	}
	w.done = true
	for v := range w.viewers {
		close(v.ch)
	}
	clear(w.viewers)
}

// joinWatch adds a viewer to the run's shared watch, starting the watch
// after the ID after if the run has none going on.
func (s *Server) joinWatch(ctx context.Context, run store.Run, after bus.ID) (*sharedWatch, *viewer, bus.ID, []Event) {
	t, _ := tenancy.From(ctx)
	key := hubKey{t.ID(), run.ID}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if w := s.watches[key]; w != nil {
		if v, start, ring, ok := w.join(); ok {
			return w, v, start, ring
		}
	}
	// From the stored event the viewer is at: the bus then brings the live
	// events after it, which a viewer that joins later from there may want.
	start := bus.ID{Seq: after.Seq}
	w := newSharedWatch(key, s.cfg.watchBuffer, start)
	wctx, cancel := context.WithCancel(tenancy.With(context.Background(), t))
	w.cancel = cancel
	s.watches[key] = w
	v, _, _, _ := w.join()
	go func() {
		defer close(w.exited)
		defer cancel()
		s.watchRun(wctx, run, start, w.publish)
		w.end()
		s.wmu.Lock()
		if s.watches[key] == w {
			delete(s.watches, key)
		}
		s.wmu.Unlock()
	}()
	return w, v, start, nil
}

// leaveWatch removes v from w, and stops w if v was its last viewer.
func (s *Server) leaveWatch(w *sharedWatch, v *viewer) {
	s.wmu.Lock()
	last := w.leave(v)
	if last && s.watches[w.key] == w {
		delete(s.watches, w.key)
	}
	s.wmu.Unlock()
	if last {
		w.cancel()
		<-w.exited
	}
}

// stopWatches stops every shared watch and waits for them.
func (s *Server) stopWatches() {
	s.wmu.Lock()
	ws := make([]*sharedWatch, 0, len(s.watches))
	for _, w := range s.watches {
		ws = append(ws, w)
	}
	clear(s.watches)
	s.wmu.Unlock()
	for _, w := range ws {
		w.cancel()
		<-w.exited
	}
}

// watchView is a viewer joined to a run's shared watch that has not read
// it yet.
type watchView struct {
	run   store.Run
	after bus.ID
	w     *sharedWatch
	v     *viewer
	start bus.ID
	ring  []Event
}

// joinView joins a viewer from after the ID after to the run's shared watch,
// starting the watch if the run has none going on. Join before the request
// is answered or acted on: from then on the viewer keeps the watch going, so
// the run cannot end, and its watch with it, before the viewer reads it,
// which would have it start a new watch after the live events it missed.
// Then read it ([Server.readView]), or leave it ([Server.leaveView]).
func (s *Server) joinView(ctx context.Context, run store.Run, after bus.ID) *watchView {
	w, v, start, ring := s.joinWatch(ctx, run, after)
	return &watchView{run: run, after: after, w: w, v: v, start: start, ring: ring}
}

// leaveView leaves a view without reading it.
func (s *Server) leaveView(jv *watchView) {
	s.leaveWatch(jv.w, jv.v)
}

// readView sends emit the events of a run another process holds, after the
// ID the view was joined from, through the run's shared watch, until the
// run's last event, the watch's end, or ctx is done; then it leaves the
// watch. It is what watchRun does for one reader, at the cost of one watch
// per run.
func (s *Server) readView(ctx context.Context, jv *watchView, emit func(Event)) {
	run, after := jv.run, jv.after
	ended := false
	send := func(e Event) {
		if ended || eventID(e).Compare(after) <= 0 {
			return
		}
		emit(e)
		after = eventID(e)
		ended = isTerminal(e.Type)
	}
	w, v, start, ring := jv.w, jv.v, jv.start, jv.ring
	for {
		if !s.view(ctx, w, v, start, ring, &after, &ended, send) {
			s.leaveWatch(w, v)
			return
		}
		// Back to the watch it fell behind on, which may have ended since,
		// and left the map: its ring still has the run's end.
		prev, pv := w, v
		var ok bool
		v, start, ring, ok = prev.join()
		if !ok {
			w, v, start, ring = s.joinWatch(ctx, run, after)
		}
		// Left only now that the viewer is back: had it been the last,
		// leaving first would stop the watch, and a new one would skip the
		// live events before the stored ones it starts after.
		s.leaveWatch(prev, pv)
	}
}

// view sends a viewer's events: the stored ones before the watch's ring
// starts, the ring's, then the watch's new ones. It reports whether the
// viewer fell behind and joins again.
func (s *Server) view(ctx context.Context, w *sharedWatch, v *viewer, start bus.ID, ring []Event, after *bus.ID, ended *bool, send func(Event)) bool {
	if after.Compare(start) < 0 {
		evs, err := s.storedEvents(ctx, w.key.runID)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Error("reading a run's events for a viewer", "run_id", w.key.runID, "err", err)
			}
			return false
		}
		for _, e := range withText(evs, *after) {
			if *ended || eventID(e).Compare(start) > 0 {
				break
			}
			send(e)
		}
	}
	for _, e := range ring {
		if *ended {
			return false
		}
		send(e)
	}
	for !*ended {
		select {
		case e, ok := <-v.ch:
			if !ok {
				return w.wasDropped(v) // else the watch is over
			}
			send(e)
		case <-ctx.Done():
			return false
		}
	}
	return false
}
