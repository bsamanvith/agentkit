package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/stream"
)

// A run's events across replicas (E18, T090). The process running a run
// writes each event through a [stream.RunStream]: every event but text_delta
// is stored in the store's event log and published on the bus, and its ID is
// its Seq in the log ("12"); a text_delta is only published, with the ID
// "12.3". Any replica serves a run's events from the log and the bus
// ([stream.Watch]); the one running it serves them from its hub.
//
// An execution that ends without the run being final (it was interrupted,
// stopped, cut by a shutdown or lost its lease) does not store its last
// event, which is only published: the run's events go on when it is resumed,
// and a stored run_error would end every later stream there. A reader that
// finds no last event in the log takes it from the run's status ([ending]).

// streamedOnly reports whether events of type typ are published and never
// stored.
func streamedOnly(typ string) bool { return typ == TypeTextDelta }

// isTerminal reports whether an event of type typ ends a run's stream.
func isTerminal(typ string) bool { return typ == TypeRunDone || typ == TypeRunError }

// eventID is e's ID; the zero ID for one that has none.
func eventID(e Event) bus.ID {
	id, err := bus.ParseID(e.ID)
	if err != nil {
		return bus.ID{}
	}
	return id
}

// withID returns e with the ID id.
func withID(e Event, id bus.ID) Event {
	e.ID, e.Seq = id.String(), int(id.Seq)
	return e
}

// endingID is the ID of a run's last event that the log does not have,
// after its stored event seq: after every event the run streamed since, and
// before its next stored event, should it be resumed.
func endingID(seq int64) bus.ID { return bus.ID{Seq: uint64(seq), N: math.MaxUint32} }

// payload is e as the log and the bus carry it: without its ID, which is
// its position.
func payload(e Event) (json.RawMessage, error) {
	e.ID, e.Seq = "", 0
	return json.Marshal(e)
}

// decodeEvent is the event the log or the bus carried, with its ID.
func decodeEvent(id bus.ID, data json.RawMessage) (Event, error) {
	var e Event
	if err := json.Unmarshal(data, &e); err != nil {
		return Event{}, fmt.Errorf("event %s: %w", id, err)
	}
	return withID(e, id), nil
}

// storedEvents reads the run's stored events, in order.
func (s *Server) storedEvents(ctx context.Context, runID string) ([]Event, error) {
	var out []Event
	var after int64
	for {
		page, err := s.store.EventsAfter(ctx, runID, after, store.MaxPageLimit)
		if err != nil {
			return nil, fmt.Errorf("reading the events of run %q: %w", runID, err)
		}
		for _, se := range page {
			e, err := decodeEvent(bus.ID{Seq: uint64(se.Seq)}, se.Data)
			if err != nil {
				return nil, fmt.Errorf("run %q: %w", runID, err)
			}
			out = append(out, e)
			after = se.Seq
		}
		if len(page) < store.MaxPageLimit {
			return out, nil
		}
	}
}

// history is a run's events so far, from the log, with what they add up to.
// A run whose log is empty while it wrote entries ran before the log (T037):
// its events are replayed from its entries ([replay]), and with backfill
// stored, so that the run's events go on from them.
func (s *Server) history(ctx context.Context, run store.Run, backfill bool) (replayed, error) {
	evs, err := s.storedEvents(ctx, run.ID)
	if err != nil {
		return replayed{}, err
	}
	if len(evs) > 0 {
		rp := summarize(evs)
		if backfill {
			err = s.kept(ctx, run, &rp)
		}
		return rp, err
	}
	entries, err := s.runEntries(ctx, run)
	if err != nil || len(entries) == 0 {
		return replayed{}, err
	}
	rp, err := replay(run, entries)
	if err != nil {
		return replayed{}, err
	}
	rp.events = slices.DeleteFunc(rp.events, func(e Event) bool { return streamedOnly(e.Type) })
	stored := make([]store.Event, len(rp.events))
	for i := range rp.events {
		rp.events[i] = withID(rp.events[i], bus.ID{Seq: uint64(i + 1)})
		data, err := payload(rp.events[i])
		if err != nil {
			return replayed{}, err
		}
		stored[i] = store.Event{Seq: int64(i + 1), Type: rp.events[i].Type, Data: data}
	}
	if backfill {
		if err := s.store.AppendEvents(ctx, run.ID, stored); err != nil {
			return replayed{}, fmt.Errorf("storing the replayed events of run %q: %w", run.ID, err)
		}
	}
	return rp, nil
}

// summarize adds up a run's events: its turns (those a rewind kept), their
// usage and the last reply.
func summarize(evs []Event) replayed {
	type turn struct {
		usage llm.Usage
		reply llm.Message
	}
	var turns []turn
	for _, e := range evs {
		switch {
		case e.Type == TypeTurnDone && e.Record != nil:
			var t turn
			t.usage = e.Record.Usage
			if len(e.Record.Messages) > 0 {
				t.reply = e.Record.Messages[0].Message
			}
			turns = append(turns, t)
		case e.Type == TypeRewind && e.Turns < len(turns):
			turns = turns[:max(e.Turns, 0)]
		}
	}
	r := replayed{events: evs, turns: len(turns)}
	for _, t := range turns {
		r.usage = r.usage.Add(t.usage)
	}
	if n := len(turns); n > 0 {
		reply := turns[n-1].reply
		r.final = &reply
	}
	return r
}

// kept compares a run's stored events with the turns the run kept (its
// entries), for an execution about to resume it: when the events go further
// (the process that wrote them stopped before it kept its last turns, or in
// the middle of a turn), it sets the rewind to send first.
func (s *Server) kept(ctx context.Context, run store.Run, rp *replayed) error {
	entries, err := s.runEntries(ctx, run)
	if err != nil {
		return err
	}
	kept := 0
	for _, e := range entries {
		if e.Kind != store.KindTurn {
			continue
		}
		if rec, err := runner.DecodeRecord(e); err == nil && rec.Canceled != nil {
			continue
		}
		kept++
	}
	partial := false // stored events of a turn not done
	for _, e := range rp.events {
		switch e.Type {
		case TypeToolCall, TypeToolResult, TypeToolPolicy, TypeToolFlagged:
			partial = true
		case TypeTurnDone, TypeRetrying, TypeRewind:
			partial = false
		}
	}
	if rp.turns <= kept && !partial {
		return nil
	}
	kept = min(kept, rp.turns)
	// The kept turns are the first ones (a rewind sets them back too).
	k := summarize(append(slices.Clone(rp.events), Event{Type: TypeRewind, Turns: kept}))
	rp.turns, rp.usage, rp.final = k.turns, k.usage, k.final
	rp.rewind = &Event{V: WireVersion, Type: TypeRewind, RunID: run.ID, At: s.cfg.clock.Now(), Turns: kept}
	return nil
}

// lastSeq is the Seq of the last stored event of evs, or 0.
func lastSeq(evs []Event) int64 {
	if len(evs) == 0 {
		return 0
	}
	return int64(evs[len(evs)-1].Seq)
}

// emitter gives an execution's events their IDs and sends them to the log,
// the bus and the hub, in order. Without a run stream (opening it failed),
// the events get IDs after the run's stored ones and reach the hub only.
type emitter struct {
	log *slog.Logger
	rs  *stream.RunStream // nil: hub only
	h   *hub
	seq int64  // without rs: the last stored event's Seq
	n   uint64 // without rs: the streamed events since
}

// emit sends e, stored (store) or only streamed, and returns it with its ID.
// A store or encoding failure is logged, and e is streamed instead: a reader
// that missed it gets the run's state from its status and its entries.
func (em *emitter) emit(ctx context.Context, e Event, store bool) Event {
	ctx = context.WithoutCancel(ctx) // the last events go out after the execution's context ends
	data, err := payload(e)
	var id bus.ID
	switch {
	case err != nil:
		em.log.Error("encoding a run's event", "run_id", em.h.runID, "type", e.Type, "err", err)
	case em.rs == nil:
		err = errNoStream
	case store:
		if id, err = em.rs.Durable(ctx, e.Type, data); err != nil {
			em.log.Error("storing a run's event; streaming it only", "run_id", em.h.runID, "type", e.Type, "err", err)
			id, err = em.rs.Live(ctx, e.Type, data)
		}
	default:
		id, err = em.rs.Live(ctx, e.Type, data)
	}
	if err != nil {
		if em.rs != nil {
			last := em.rs.Last()
			em.seq, em.n = int64(last.Seq), last.N
		}
		if store && em.rs == nil {
			em.seq, em.n = em.seq+1, 0
		} else {
			em.n++
		}
		id = bus.ID{Seq: uint64(em.seq), N: em.n}
	}
	e = withID(e, id)
	em.h.append(e)
	return e
}

var errNoStream = errors.New("no run stream")

// replayedText is where the N of the text_delta events a stored turn's text
// is replayed as starts, after every text_delta the turn streamed.
const replayedText = 1 << 31

// withText returns the stored events evs, to be sent after the ID after,
// with the text of each turn whose streamed text_delta events the client has
// none of, as text_delta events: a client that reads a run from the store
// sees its text as one that followed it did. A turn streams its text before
// its tool calls, after the stored event before the turn (Seq B): its
// replayed text_delta events are B.2147483648 on, after any it streamed.
func withText(evs []Event, after bus.ID) []Event {
	texts := map[int64][]Event{} // by B
	var before int64
	for _, e := range evs {
		id := eventID(e)
		if e.Type == TypeTurnDone && e.Record != nil && len(e.Record.Messages) > 0 &&
			after.Compare(bus.ID{Seq: uint64(before), N: 1}) < 0 {
			n := uint64(replayedText)
			for i, b := range e.Record.Messages[0].Message.Content {
				if t, ok := b.(llm.Text); ok {
					texts[before] = append(texts[before], withID(Event{
						V: WireVersion, Type: TypeTextDelta, RunID: e.RunID, At: e.Record.FirstEventAt,
						Turn: e.Record.Turn, Block: i, Text: t.Text,
					}, bus.ID{Seq: uint64(before), N: n}))
					n++
				}
			}
		}
		if bounds(e, id) {
			before = int64(id.Seq)
		}
	}
	if len(texts) == 0 {
		return evs
	}
	out := make([]Event, 0, len(evs))
	for _, e := range evs {
		out = append(out, e)
		if id := eventID(e); bounds(e, id) {
			out = append(out, texts[int64(id.Seq)]...)
		}
	}
	return out
}

// bounds reports whether e, with the ID id, is a stored event a turn
// starts after.
func bounds(e Event, id bus.ID) bool {
	return id.N == 0 && (e.Type == TypeTurnDone || e.Type == TypeRunStart || e.Type == TypeRetrying)
}
