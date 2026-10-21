package stream

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Watch streams the events of run runID in ctx's tenant after the given ID
// (the zero ID for all of them), in order, from any process:
//
//  1. it replays the stored events after it from log;
//  2. then follows the run's topic on b from where the replay ended, so an
//     event is never delivered twice, whether the replay or the bus had it;
//  3. on [bus.ErrGap] (the topic's history was trimmed, lost, or a publish
//     failed), it backfills the stored events from the log and follows the
//     bus again from the later of what it delivered and the topic's newest
//     message.
//
// Every durable event after the given ID is delivered exactly once. A live
// event can be skipped in a gap, never repeated. The sequence ends after the
// first event terminal accepts (nil accepts none), or with an error: ctx's
// when it is done, a store or bus error, or [ErrInvalid] for bad arguments.
// For a run that nobody runs any more and that has no terminal event, the
// sequence waits until ctx is done.
func Watch(ctx context.Context, log store.EventLog, b bus.Bus, runID string, after bus.ID, terminal func(typ string) bool, opts ...Option) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if log == nil || b == nil {
			yield(Event{}, fmt.Errorf("%w: nil event log or bus", ErrInvalid))
			return
		}
		t, err := tenancy.Require(ctx)
		if err != nil {
			yield(Event{}, err)
			return
		}
		topic, err := bus.RunTopic(t, runID)
		if err != nil {
			yield(Event{}, fmt.Errorf("%w: %w", ErrInvalid, err))
			return
		}
		w := &watcher{ctx: ctx, log: log, bus: b, runID: runID, topic: topic, cursor: after, terminal: terminal, yield: yield, cfg: newConfig(opts)}
		w.run()
	}
}

type watcher struct {
	ctx      context.Context
	log      store.EventLog
	bus      bus.Bus
	runID    string
	topic    bus.Topic
	cursor   bus.ID // the last ID delivered, or the one asked to start after
	terminal func(string) bool
	yield    func(Event, error) bool
	cfg      config
}

// errStop ends the watch without an error: the consumer stopped, or the
// terminal event went out.
var errStop = errors.New("stop")

// errBehind means the log has events the bus did not bring.
var errBehind = errors.New("behind the log")

func (w *watcher) run() {
	err := w.ended()
	if err == nil {
		err = w.backfill()
	}
	for err == nil {
		err = w.follow()
		if errors.Is(err, bus.ErrGap) || errors.Is(err, errBehind) {
			err = w.recover()
		}
	}
	if errors.Is(err, errStop) {
		return
	}
	if ctxErr := w.ctx.Err(); ctxErr != nil {
		err = ctxErr // whatever failed, it was because ctx is done
	}
	w.yield(Event{}, err)
}

// ended returns errStop when the event the watch starts after is the
// terminal one: a client reconnecting after it gets nothing, at once.
func (w *watcher) ended() error {
	if w.terminal == nil || w.cursor.Seq == 0 || w.cursor.N != 0 {
		return nil
	}
	events, err := w.log.EventsAfter(w.ctx, w.runID, int64(w.cursor.Seq)-1, 1)
	if err != nil {
		return fmt.Errorf("stream: replay run %q: %w", w.runID, err)
	}
	if len(events) == 1 && events[0].Seq == int64(w.cursor.Seq) && w.terminal(events[0].Type) {
		return errStop
	}
	return nil
}

// deliver yields e if it is after the cursor, and says whether to go on.
func (w *watcher) deliver(e Event) error {
	if e.ID.Compare(w.cursor) <= 0 {
		return nil
	}
	w.cursor = e.ID
	if !w.yield(e, nil) || (w.terminal != nil && w.terminal(e.Type)) {
		return errStop
	}
	return nil
}

// backfill delivers the stored events after the cursor.
func (w *watcher) backfill() error {
	for {
		events, err := w.log.EventsAfter(w.ctx, w.runID, int64(w.cursor.Seq), store.MaxPageLimit)
		if err != nil {
			return fmt.Errorf("stream: replay run %q: %w", w.runID, err)
		}
		for _, e := range events {
			if err := w.deliver(Event{ID: bus.ID{Seq: uint64(e.Seq)}, Type: e.Type, Data: e.Data}); err != nil {
				return err
			}
		}
		if len(events) < store.MaxPageLimit {
			return nil
		}
	}
}

// skips says whether delivering id next would skip a durable event: they are
// numbered densely, so the next one after the cursor is cursor.Seq+1, and a
// delta comes after its durable event. The bus reports most holes as gaps,
// but not all: one a crash left before a publish whose topic was lost, or an
// unpublished event after a failed one, look like no hole to it.
func (w *watcher) skips(id bus.ID) bool {
	next := w.cursor.Seq + 1
	if id.N != 0 {
		next = w.cursor.Seq
	}
	return id.Seq > next
}

// follow delivers the bus's messages after the cursor until the
// subscription ends, or a check of the log finds it behind.
func (w *watcher) follow() error {
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	sub, err := w.bus.Subscribe(ctx, w.topic, w.cursor)
	if err != nil {
		return fmt.Errorf("stream: follow run %q: %w", w.runID, err)
	}
	defer func() {
		cancel()
		for range sub.C() { // let the subscription stop
		}
	}()
	poll := w.cfg.clk.NewTicker(w.cfg.poll)
	defer poll.Stop()
	var stored int64 // the log's last Seq at the last poll
	for done := false; !done; {
		select {
		case m, ok := <-sub.C():
			if !ok {
				done = true
				break
			}
			e, err := decode(m)
			if err != nil {
				return err
			}
			if w.skips(e.ID) {
				if err := w.backfill(); err != nil {
					return err
				}
			}
			if err := w.deliver(e); err != nil {
				return err
			}
		case <-poll.C():
			// Behind only on what was stored a whole poll ago: the bus
			// brings an event a moment after it is stored.
			if stored > int64(w.cursor.Seq) {
				return errBehind
			}
			if stored, err = w.log.LastEventSeq(ctx, w.runID); err != nil {
				return fmt.Errorf("stream: check run %q: %w", w.runID, err)
			}
		}
	}
	if err := sub.Err(); err != nil {
		if errors.Is(err, bus.ErrGap) {
			return bus.ErrGap
		}
		if ctxErr := w.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("stream: follow run %q: %w", w.runID, err)
	}
	return w.ctx.Err()
}

// recover fills a gap: the stored events after the cursor, then the
// position to follow the bus from. Reading the topic's last ID before the
// log means every durable event up to it is in the log by then (events are
// stored before they are published), so following after it skips only live
// events. Without that, a topic whose history restarted after the cursor
// would report a gap at its first message on every attempt.
func (w *watcher) recover() error {
	last, err := w.bus.Last(w.ctx, w.topic)
	if err != nil {
		return fmt.Errorf("stream: recover run %q: %w", w.runID, err)
	}
	if err := w.backfill(); err != nil {
		return err
	}
	if last.Compare(w.cursor) > 0 {
		w.cursor = last
	}
	return nil
}

// Pending streams the messages run runID's topic on b has after the given
// ID, up to its newest when Pending is called, live ones included: what a
// watch that stops now has not delivered yet. A watch stopped because the
// run was let go reads them here before it turns to the log, which has the
// durable events but not the deltas (T090: a follower numbers a turn's text
// as its holder did only from the bus).
//
// The sequence ends without an error at the topic's newest message, after
// the first event terminal accepts, at a gap, or before a message that
// would skip a durable event (the log has it); with an error when ctx is
// done (it bounds the wait for a bus that is slow to deliver), on a bus
// error, or with [ErrInvalid].
func Pending(ctx context.Context, b bus.Bus, runID string, after bus.ID, terminal func(typ string) bool) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if b == nil {
			yield(Event{}, fmt.Errorf("%w: nil bus", ErrInvalid))
			return
		}
		t, err := tenancy.Require(ctx)
		if err != nil {
			yield(Event{}, err)
			return
		}
		topic, err := bus.RunTopic(t, runID)
		if err != nil {
			yield(Event{}, fmt.Errorf("%w: %w", ErrInvalid, err))
			return
		}
		last, err := b.Last(ctx, topic)
		if err != nil {
			yield(Event{}, fmt.Errorf("stream: pending run %q: %w", runID, err))
			return
		}
		if last.Compare(after) <= 0 {
			return
		}
		sctx, cancel := context.WithCancel(ctx)
		defer cancel()
		sub, err := b.Subscribe(sctx, topic, after)
		if err != nil {
			yield(Event{}, fmt.Errorf("stream: pending run %q: %w", runID, err))
			return
		}
		defer func() {
			cancel()
			for range sub.C() { // let the subscription stop
			}
		}()
		w := &watcher{cursor: after}
		for m := range sub.C() {
			e, err := decode(m)
			if err != nil {
				yield(Event{}, err)
				return
			}
			if w.skips(e.ID) {
				return
			}
			w.cursor = e.ID
			if !yield(e, nil) || (terminal != nil && terminal(e.Type)) || e.ID.Compare(last) >= 0 {
				return
			}
		}
		if err := sub.Err(); err != nil && !errors.Is(err, bus.ErrGap) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			}
			yield(Event{}, fmt.Errorf("stream: pending run %q: %w", runID, err))
		}
	}
}
