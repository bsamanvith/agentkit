package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Publisher writes runs' events: durable ones to the store's event log and
// the bus, live ones to the bus only. It is safe for concurrent use.
type Publisher struct {
	log store.EventLog
	bus bus.Bus
	cfg config
}

// NewPublisher returns a publisher over log and b.
func NewPublisher(log store.EventLog, b bus.Bus, opts ...Option) (*Publisher, error) {
	if log == nil || b == nil {
		return nil, fmt.Errorf("%w: nil event log or bus", ErrInvalid)
	}
	return &Publisher{log: log, bus: b, cfg: newConfig(opts)}, nil
}

// Open returns the stream of run runID in ctx's tenant, positioned after its
// stored events, for the process that holds the run's lease. Only that
// process writes the run's events, so one RunStream per run at a time.
//
// It first republishes the stored events the bus lacks, those after the
// topic's last message: a crash between storing an event and publishing it,
// on this process or another, leaves no hole that readers on the bus cannot
// see. A failure to do so is logged, not returned. When the topic's last
// message is a live event after the last stored one (an earlier execution
// of the run streamed it), the stream's live events go on after it, so no
// two of the run's events share an ID.
func (p *Publisher) Open(ctx context.Context, runID string) (*RunStream, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return nil, err
	}
	topic, err := bus.RunTopic(t, runID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	seq, err := p.log.LastEventSeq(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("stream: open run %q: %w", runID, err)
	}
	s := &RunStream{p: p, runID: runID, topic: topic, seq: seq}
	if last, ok := s.republish(ctx); ok && last.Seq == uint64(seq) {
		s.n = last.N
	}
	return s, nil
}

// RunStream is one run's event stream, for the process running the run. It
// is safe for concurrent use; events get IDs in the order its methods are
// called.
type RunStream struct {
	p     *Publisher
	runID string
	topic bus.Topic

	mu  sync.Mutex
	seq int64  // the last durable event's Seq
	n   uint64 // live events since it
}

// Durable stores the event as the run's next durable event, then publishes
// it, and returns its ID. An error is the store's: nothing was published, and
// the stream stays where it was, so calling Durable again with the same event
// is safe (an identical event at the position is a no-op in the store). A
// failed publish is logged, not returned: readers fill the hole from the log.
func (s *RunStream) Durable(ctx context.Context, typ string, data json.RawMessage) (bus.ID, error) {
	msg, err := encode(typ, data)
	if err != nil {
		return bus.ID{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := s.seq + 1
	if err := s.p.log.AppendEvents(ctx, s.runID, []store.Event{{Seq: seq, Type: typ, Data: data}}); err != nil {
		return bus.ID{}, fmt.Errorf("stream: run %q: store event %d: %w", s.runID, seq, err)
	}
	s.seq, s.n = seq, 0
	id := bus.ID{Seq: uint64(seq)}
	s.publish(ctx, id, msg)
	return id, nil
}

// Live publishes a live-only event (a text delta) and returns its ID. It is
// never stored: a reader that misses it gets what it carried from a later
// durable event. A failed publish is logged, not returned.
func (s *RunStream) Live(ctx context.Context, typ string, data json.RawMessage) (bus.ID, error) {
	msg, err := encode(typ, data)
	if err != nil {
		return bus.ID{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	id := bus.ID{Seq: uint64(s.seq), N: s.n}
	s.publish(ctx, id, msg)
	return id, nil
}

// Last returns the ID of the stream's last event.
func (s *RunStream) Last() bus.ID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bus.ID{Seq: uint64(s.seq), N: s.n}
}

// publish publishes one message. Call with s.mu held. ErrStale means the
// message is already there (an earlier attempt landed), which is success.
func (s *RunStream) publish(ctx context.Context, id bus.ID, msg []byte) {
	err := s.p.bus.Publish(ctx, s.topic, bus.Message{ID: id, Data: msg})
	if err != nil && !errors.Is(err, bus.ErrStale) {
		s.p.cfg.log.WarnContext(ctx, "stream: publish failed; readers will fill it from the log",
			slog.String("run", s.runID), slog.String("id", id.String()), slog.Any("err", err))
	}
}

// republish publishes the stored events after the topic's last message,
// and returns that message's ID, with false when the bus could not say. A
// topic with no message needs none: the next message starts a new history,
// and a reader positioned before it gets ErrGap and reads the log.
func (s *RunStream) republish(ctx context.Context) (bus.ID, bool) {
	last, err := s.p.bus.Last(ctx, s.topic)
	if err != nil {
		s.p.cfg.log.WarnContext(ctx, "stream: reading the bus's last event failed; not republishing",
			slog.String("run", s.runID), slog.Any("err", err))
		return bus.ID{}, false
	}
	if last.IsZero() {
		return last, true
	}
	after := int64(last.Seq)
	for after < s.seq {
		events, err := s.p.log.EventsAfter(ctx, s.runID, after, store.MaxPageLimit)
		if err != nil {
			s.p.cfg.log.WarnContext(ctx, "stream: reading stored events to republish failed",
				slog.String("run", s.runID), slog.Any("err", err))
			return last, true
		}
		if len(events) == 0 {
			return last, true
		}
		for _, e := range events {
			msg, err := encode(e.Type, e.Data)
			if err != nil { // the store checked both
				continue
			}
			s.publish(ctx, bus.ID{Seq: uint64(e.Seq)}, msg)
			after = e.Seq
		}
	}
	return last, true
}
