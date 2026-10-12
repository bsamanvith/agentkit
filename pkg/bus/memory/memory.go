// Package memory is the in-process [bus.Bus]: a bounded ring of messages per
// topic, for a single process (agentkit serve with no Redis) and for tests.
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/bus/internal/stream"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// readBatch is the most entries one read hands to a subscriber.
const readBatch = 256

// sweepEvery is how often (by the bus's clock) a publish drops the topics
// whose history expired, so that a topic nobody reads again is not kept.
const sweepEvery = time.Minute

// Bus is the in-process bus. Make one with [New].
type Bus struct {
	clock clock.Clock
	trim  func(bus.Topic) bus.Trim
	subs  stream.Group

	mu        sync.Mutex
	topics    map[string]*topic
	lastSweep time.Time
}

var _ bus.Bus = (*Bus)(nil)

// Option configures a [Bus].
type Option func(*Bus)

// WithClock sets the clock that expires topics (default [clock.Real]).
func WithClock(c clock.Clock) Option { return func(b *Bus) { b.clock = c } }

// WithTrim sets the trim policy per topic (default [bus.DefaultTrim]).
func WithTrim(trim func(bus.Topic) bus.Trim) Option { return func(b *Bus) { b.trim = trim } }

// New returns an empty bus.
func New(opts ...Option) *Bus {
	b := &Bus{clock: clock.Real(), trim: bus.DefaultTrim, topics: map[string]*topic{}}
	for _, o := range opts {
		o(b)
	}
	return b
}

type topic struct {
	trim bus.Trim
	// ring holds the newest entries, oldest first from start.
	ring  []stream.Entry
	start int
	// last is the newest ID published since the history was (re)started,
	// zero if none.
	last bus.ID
	// failed is the newest ID after last whose publish failed (its context
	// was done), zero if none: the next message must not follow last as if
	// nothing were missing, unless it is that message retried.
	failed  bus.ID
	touched time.Time // last publish
	// changed is closed and replaced on every publish.
	changed chan struct{}
	readers int
}

// Publish implements [bus.Bus].
func (b *Bus) Publish(ctx context.Context, t bus.Topic, msg bus.Message) error {
	if t.IsZero() || msg.ID.IsZero() {
		return fmt.Errorf("%w: publish needs a topic and a non-zero id", bus.ErrInvalid)
	}
	trim := b.trim(t)
	if err := trim.Validate(); err != nil {
		return err
	}
	if b.subs.Closed() {
		return bus.ErrClosed
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()
	if err := ctx.Err(); err != nil {
		// Not published, but the caller goes on without it: a gap all the
		// same, as when a Redis publish fails.
		if tp := b.topics[t.String()]; tp != nil && !tp.expired(now) && !tp.last.IsZero() &&
			msg.ID.Compare(tp.last) > 0 && msg.ID.Compare(tp.failed) > 0 {
			tp.failed = msg.ID
		}
		return err
	}
	tp := b.topic(t.String(), trim, now)
	if !tp.last.IsZero() && msg.ID.Compare(tp.last) <= 0 {
		return fmt.Errorf("%w: %s after %s on %s", bus.ErrStale, msg.ID, tp.last, t)
	}
	prev := tp.last
	if msg.ID.Compare(tp.failed) > 0 && !tp.failed.IsZero() {
		prev = tp.failed // never stored: a subscriber at last gets ErrGap
	}
	e := stream.Entry{ID: msg.ID, Data: clone(msg.Data), Prev: prev, Fresh: prev.IsZero()}
	if len(tp.ring) < tp.trim.MaxLen {
		tp.ring = append(tp.ring, e)
	} else {
		tp.ring[tp.start] = e
		tp.start = (tp.start + 1) % len(tp.ring)
	}
	tp.last = msg.ID
	if tp.failed.Compare(msg.ID) <= 0 {
		tp.failed = bus.ID{}
	}
	tp.touched = now
	close(tp.changed)
	tp.changed = make(chan struct{})
	b.sweep(now)
	return nil
}

// Subscribe implements [bus.Bus].
func (b *Bus) Subscribe(ctx context.Context, t bus.Topic, after bus.ID) (bus.Subscription, error) {
	if t.IsZero() {
		return nil, fmt.Errorf("%w: subscribe needs a topic", bus.ErrInvalid)
	}
	trim := b.trim(t)
	if err := trim.Validate(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	tp := b.topic(t.String(), trim, b.clock.Now())
	tp.readers++
	b.mu.Unlock()
	return b.subs.Start(ctx, &reader{b: b, name: t.String(), trim: trim}, after)
}

// Close implements [bus.Bus].
func (b *Bus) Close() error {
	b.subs.Close()
	return nil
}

// topic returns the topic called name, creating it, or restarting its history
// when it expired. Call with b.mu held.
func (b *Bus) topic(name string, trim bus.Trim, now time.Time) *topic {
	tp := b.topics[name]
	if tp == nil {
		tp = &topic{trim: trim, changed: make(chan struct{})}
		b.topics[name] = tp
	}
	if tp.expired(now) {
		tp.ring, tp.start, tp.last, tp.failed = nil, 0, bus.ID{}, bus.ID{}
	}
	return tp
}

func (tp *topic) expired(now time.Time) bool {
	return !tp.last.IsZero() && now.Sub(tp.touched) >= tp.trim.MaxAge
}

// sweep drops expired topics that nobody reads. Call with b.mu held.
func (b *Bus) sweep(now time.Time) {
	if now.Sub(b.lastSweep) < sweepEvery {
		return
	}
	b.lastSweep = now
	for name, tp := range b.topics {
		if tp.readers == 0 && (tp.last.IsZero() || tp.expired(now)) {
			delete(b.topics, name)
		}
	}
}

// after copies up to max entries with IDs after id, oldest first.
func (tp *topic) after(id bus.ID, max int) []stream.Entry {
	n := len(tp.ring)
	at := func(i int) stream.Entry { return tp.ring[(tp.start+i)%n] }
	i := sort.Search(n, func(i int) bool { return at(i).ID.Compare(id) > 0 })
	var out []stream.Entry
	for ; i < n && len(out) < max; i++ {
		e := at(i)
		e.Data = clone(e.Data)
		out = append(out, e)
	}
	return out
}

type reader struct {
	b    *Bus
	name string
	trim bus.Trim
}

func (r *reader) Read(ctx context.Context, after bus.ID) ([]stream.Entry, error) {
	for {
		r.b.mu.Lock()
		tp := r.b.topic(r.name, r.trim, r.b.clock.Now())
		entries := tp.after(after, readBatch)
		changed := tp.changed
		r.b.mu.Unlock()
		if len(entries) > 0 {
			return entries, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (r *reader) Close() {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	if tp := r.b.topics[r.name]; tp != nil {
		tp.readers--
	}
}

func clone(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append(make([]byte, 0, len(b)), b...)
}
