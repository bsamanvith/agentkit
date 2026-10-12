// Package stream is the subscriber side shared by the bus backends: it reads a
// topic's log after a cursor, checks that nothing is missing, and delivers in
// order. A backend only stores entries and reads them back, so the gap rule
// and the delivery loop exist once.
package stream

import (
	"context"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/bus"
)

// Entry is a stored message and what came before it.
type Entry struct {
	ID   bus.ID
	Data []byte
	// Prev is the ID of the entry published just before this one, when
	// Fresh is false.
	Prev bus.ID
	// Fresh means nothing is known before this entry: it is the first of
	// its topic's history, or the history before it was lost.
	Fresh bool
}

// Reader reads one topic's log.
type Reader interface {
	// Read returns entries after the given ID in order, at least one,
	// waiting for them if there are none yet. It returns only the context's
	// error when ctx is done, and retries transient failures itself.
	Read(ctx context.Context, after bus.ID) ([]Entry, error)
	// Close releases what the reader holds. It is called once, when the
	// subscription ends.
	Close()
}

// Follows reports whether e can be delivered right after cursor with nothing
// missing in between: the entry before e is at or before cursor. An entry
// with no known predecessor follows only the zero cursor (the start).
func Follows(cursor bus.ID, e Entry) bool {
	if e.Fresh {
		return cursor.IsZero()
	}
	return e.Prev.Compare(cursor) <= 0
}

// Group runs the subscriptions of one bus and stops them all on Close.
type Group struct {
	mu     sync.Mutex
	closed bool
	cancel map[*subscription]context.CancelCauseFunc
	wg     sync.WaitGroup
}

// Start delivers what r reads after the given ID until ctx is done, the group
// is closed or the log has a gap. r is closed when the subscription ends. It
// returns [bus.ErrClosed] (and closes r) if the group is closed.
func (g *Group) Start(ctx context.Context, r Reader, after bus.ID) (bus.Subscription, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	s := &subscription{c: make(chan bus.Message), done: make(chan struct{})}

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		cancel(nil)
		r.Close()
		return nil, bus.ErrClosed
	}
	if g.cancel == nil {
		g.cancel = map[*subscription]context.CancelCauseFunc{}
	}
	g.cancel[s] = cancel
	g.wg.Add(1)
	g.mu.Unlock()

	go func() {
		defer g.wg.Done()
		err := deliver(ctx, r, after, s.c)
		r.Close()
		g.mu.Lock()
		delete(g.cancel, s)
		g.mu.Unlock()
		cancel(nil)
		s.err = err
		close(s.done)
		close(s.c)
	}()
	return s, nil
}

// Close ends every subscription with [bus.ErrClosed] and waits for them. Later
// Starts fail. It is safe to call more than once.
func (g *Group) Close() {
	g.mu.Lock()
	g.closed = true
	for _, cancel := range g.cancel {
		cancel(bus.ErrClosed)
	}
	g.mu.Unlock()
	g.wg.Wait()
}

// Closed reports whether Close was called.
func (g *Group) Closed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed
}

func deliver(ctx context.Context, r Reader, cursor bus.ID, out chan<- bus.Message) error {
	for {
		entries, err := r.Read(ctx, cursor)
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.ID.Compare(cursor) <= 0 {
				continue // a backend never returns these; never deliver one twice
			}
			if !Follows(cursor, e) {
				return bus.ErrGap
			}
			select {
			case out <- bus.Message{ID: e.ID, Data: e.Data}:
			case <-ctx.Done():
				return context.Cause(ctx)
			}
			cursor = e.ID
		}
	}
}

type subscription struct {
	c    chan bus.Message
	done chan struct{} // closed after err is written, before c is closed
	err  error
}

func (s *subscription) C() <-chan bus.Message { return s.c }

func (s *subscription) Err() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}
