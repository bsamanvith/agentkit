package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// SimulateEvents runs one seeded simulation against a fresh log from newLog:
// writers with stale views appending batches, retrying whole and partly
// stored batches, conflicting, leaving gaps and sending invalid events, reads
// from random points with random limits, a clock that steps back, and calls
// with no tenant or a cancelled context, over two tenants that use the same
// run ids. Every call's result is predicted by an independent model of the
// logs, and the invariants listed on eventRig are checked at the end. A
// failure names its seed and step; the same seed replays the same calls.
//
// RunEventLog runs a range of seeds. A backend's fuzz target can call it with
// the fuzzer's seed.
func SimulateEvents(t *testing.T, newLog func(t *testing.T, clk clock.Clock) store.EventLog, seed uint64) {
	t.Helper()
	r := newEventRig(t, newLog)
	s := &eventSim{
		t:     t,
		r:     r,
		rng:   rand.New(rand.NewPCG(seed, 0x2545f4914f6cdd1d)),
		seed:  seed,
		ctxs:  map[string]context.Context{tenantA: r.ctx(tenantA), tenantB: r.ctx(tenantB)},
		model: map[logKey][]eventSnap{},
	}
	for range 3 {
		s.writers = append(s.writers, &eventWriter{view: map[logKey]int64{}})
	}
	steps := 40 + s.rng.IntN(160)
	for s.step = 0; s.step < steps; s.step++ {
		s.do()
	}
}

type eventSim struct {
	t     *testing.T
	r     *eventRig
	rng   *rand.Rand
	seed  uint64
	step  int
	n     int // makes every generated event unique
	ctxs  map[string]context.Context
	model map[logKey][]eventSnap

	writers []*eventWriter
}

// eventWriter is a writer such as the runner: it knows how long each log was
// when it last looked, which may be stale.
type eventWriter struct {
	view     map[logKey]int64
	accepted *eventBatch // the last batch it appended, retried as is
	lost     *eventBatch // the last batch that conflicted
}

type eventBatch struct {
	k      logKey
	events []store.Event
}

func (s *eventSim) fatalf(format string, args ...any) {
	s.t.Helper()
	s.t.Fatalf("seed %d step %d: %s", s.seed, s.step, fmt.Sprintf(format, args...))
}

func (s *eventSim) pickKey() logKey {
	tenant := tenantA
	if s.rng.IntN(2) == 1 {
		tenant = tenantB
	}
	return logKey{tenant, fmt.Sprintf("r%d", s.rng.IntN(2))}
}

func (s *eventSim) do() {
	w := s.writers[s.rng.IntN(len(s.writers))]
	k := s.pickKey()
	switch op := s.rng.IntN(100); {
	case op < 35:
		s.appendFromView(w, k)
	case op < 42:
		if w.accepted != nil {
			s.expect("retry", w.accepted.k, w.accepted.events, nil)
		}
	case op < 46:
		if w.lost != nil {
			s.expect("retry of a lost batch", w.lost.k, w.lost.events, store.ErrConflict)
		}
	case op < 52:
		s.appendOverlapping(k)
	case op < 56:
		s.expect("gap", k, s.batch(int64(len(s.model[k]))+2+int64(s.rng.IntN(3)), 1+s.rng.IntN(2)), store.ErrGap)
	case op < 60:
		s.appendInvalid(k)
	case op < 72:
		s.lookAt(w, k)
	case op < 88:
		s.read(k)
	case op < 94:
		s.r.clk.Advance(time.Duration(s.rng.IntN(3)) * time.Second)
		if s.rng.IntN(4) == 0 {
			s.r.clk.skew(-time.Duration(s.rng.IntN(120)) * time.Minute)
		} else {
			s.r.clk.skew(0)
		}
	default:
		s.badContext(k)
	}
}

// appendFromView appends a batch after the writer's view of the log. A
// stale view conflicts, unless the events at the taken positions happen to
// be identical (two events of one type with no data).
func (s *eventSim) appendFromView(w *eventWriter, k logKey) {
	b := s.batch(w.view[k]+1, 1+s.rng.IntN(3))
	if s.expect("append", k, b, predictEvents(s.model[k], b)) {
		w.accepted = &eventBatch{k, b}
		w.view[k] = b[len(b)-1].Seq
	} else {
		w.lost = &eventBatch{k, b}
	}
}

// appendOverlapping appends a batch whose start is a copy of stored events
// and whose end is new: the retry of a batch a crash cut short.
func (s *eventSim) appendOverlapping(k logKey) {
	log := s.model[k]
	if len(log) == 0 {
		return
	}
	from := 1 + s.rng.IntN(len(log))
	var b []store.Event
	for seq := from; seq <= len(log); seq++ {
		e := log[seq-1]
		b = append(b, store.Event{Seq: int64(seq), Type: e.Type, Data: dataOf(e)})
	}
	b = append(b, s.batch(int64(len(log))+1, s.rng.IntN(3))...)
	if s.rng.IntN(3) == 0 { // and one stored event changed
		i := s.rng.IntN(len(log) - from + 1)
		b[i].Type += "-changed"
		s.expect("overlap with a change", k, b, store.ErrConflict)
		return
	}
	s.expect("overlap", k, b, nil)
}

func (s *eventSim) appendInvalid(k logKey) {
	b := s.batch(int64(len(s.model[k]))+1, 1+s.rng.IntN(3))
	i := s.rng.IntN(len(b))
	switch s.rng.IntN(5) {
	case 0:
		b[i].Type = ""
	case 1:
		b[i].Data = json.RawMessage(`{"open":`)
	case 2:
		b[i].Data = json.RawMessage("\"\xfe\"")
	case 3:
		b[i].Type = "nul\x00"
	case 4:
		if len(b) < 2 {
			b = append(b, s.batch(b[0].Seq+1, 1)...)
		}
		b[1].Seq += 1 + int64(s.rng.IntN(2))
	}
	s.expect("invalid", k, b, store.ErrInvalid)
}

// expect appends b to log k, checks the result is want, and updates the
// model. It reports whether the append succeeded.
func (s *eventSim) expect(op string, k logKey, b []store.Event, want error) bool {
	s.t.Helper()
	if got := predictEvents(s.model[k], b); !isErr(got, want) {
		s.fatalf("%s: the model says %v, the step expects %v (a bug in the simulation)", op, got, want)
	}
	err := s.r.append(s.ctxs[k.tenant], k.conv, b...)
	if !isErr(err, want) {
		s.fatalf("%s %s/%s %v: AppendEvents = %v, want %v", op, k.tenant, k.conv, eventSeqs(b), err, want)
	}
	if err != nil {
		return false
	}
	for _, e := range b {
		if e.Seq > int64(len(s.model[k])) {
			s.model[k] = append(s.model[k], snapEvent(e))
		}
	}
	return true
}

// predictEvents restates the rules of AppendEvents on the model: invalid
// first, then a gap, then a conflict at a taken position.
func predictEvents(log []eventSnap, b []store.Event) error {
	for i, e := range b {
		if e.Seq < 1 || (i > 0 && e.Seq != b[i-1].Seq+1) || e.Type == "" || !textOK(e.Type) ||
			(len(e.Data) > 0 && !json.Valid(e.Data)) || !textOK(string(e.Data)) {
			return store.ErrInvalid
		}
	}
	if len(b) == 0 {
		return nil
	}
	if b[0].Seq > int64(len(log))+1 {
		return store.ErrGap
	}
	for _, e := range b {
		if e.Seq <= int64(len(log)) && log[e.Seq-1] != snapEvent(e) {
			return store.ErrConflict
		}
	}
	return nil
}

// lookAt brings a writer's view up to date.
func (s *eventSim) lookAt(w *eventWriter, k logKey) {
	last, err := s.r.s.LastEventSeq(s.ctxs[k.tenant], k.conv)
	if err != nil || last != int64(len(s.model[k])) {
		s.fatalf("LastEventSeq %s/%s = %d, %v; want %d", k.tenant, k.conv, last, err, len(s.model[k]))
	}
	w.view[k] = last
}

// read reads from a random point with a random limit and compares with the
// model.
func (s *eventSim) read(k logKey) {
	log := s.model[k]
	after := int64(s.rng.IntN(len(log) + 3))
	limit := []int{0, 1, 2, 5, store.MaxPageLimit + 1}[s.rng.IntN(5)]
	got, err := s.r.s.EventsAfter(s.ctxs[k.tenant], k.conv, after, limit)
	if err != nil {
		s.fatalf("EventsAfter %s/%s %d %d: %v", k.tenant, k.conv, after, limit, err)
	}
	var want []eventSnap
	if after < int64(len(log)) {
		want = log[after:min(int64(len(log)), after+int64(pageLimit(limit)))]
	}
	if len(got) != len(want) {
		s.fatalf("EventsAfter %s/%s %d %d: %d events, want %d", k.tenant, k.conv, after, limit, len(got), len(want))
	}
	for i, e := range got {
		if e.Seq != after+int64(i)+1 || snapEvent(e) != want[i] {
			s.fatalf("EventsAfter %s/%s %d %d: event %d is %d %+v, want %d %+v", k.tenant, k.conv, after, limit, i, e.Seq, snapEvent(e), after+int64(i)+1, want[i])
		}
	}
}

func (s *eventSim) badContext(k logKey) {
	ctx, want := s.r.t.Context(), tenancy.ErrNoTenant
	if s.rng.IntN(2) == 0 {
		c, cancel := context.WithCancel(s.ctxs[k.tenant])
		cancel()
		ctx, want = c, context.Canceled
	}
	b := s.batch(int64(len(s.model[k]))+1, 1)
	if err := s.r.append(ctx, k.conv, b...); !errors.Is(err, want) {
		s.fatalf("AppendEvents with a bad context = %v, want %v", err, want)
	}
	if _, err := s.r.s.EventsAfter(ctx, k.conv, 0, 0); !errors.Is(err, want) {
		s.fatalf("EventsAfter with a bad context = %v, want %v", err, want)
	}
}

// batch is n new events from seq.
func (s *eventSim) batch(seq int64, n int) []store.Event {
	out := make([]store.Event, n)
	for i := range out {
		s.n++
		out[i] = eventAt(seq+int64(i), s.n)
		if s.rng.IntN(5) == 0 {
			out[i].Data = nil
		}
	}
	return out
}

func dataOf(e eventSnap) json.RawMessage {
	if e.Data == "" {
		return nil
	}
	return json.RawMessage(e.Data)
}
