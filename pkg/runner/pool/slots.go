package pool

import "context"

// Slots is a pool's limit of runs going on at once ([WithLimit]): a run
// holds one of them from before it is created or claimed until its
// execution ends. A chat takes one with [Slots.TryAcquire] and is refused
// when none is free; a background pool's dispatcher waits for one with
// [Slots.Acquire] before it claims a run (it implements runner.Slots), so
// the pool's background runs wait in the store meanwhile. It is safe for
// concurrent use.
type Slots struct{ ch chan struct{} }

// NewSlots returns n slots, all free. n below 1 is taken as 1.
func NewSlots(n int) *Slots { return &Slots{ch: make(chan struct{}, max(n, 1))} }

// TryAcquire takes a free slot and reports whether there was one.
func (s *Slots) TryAcquire() bool {
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// Acquire takes a slot, waiting for one to be free until ctx is done.
func (s *Slots) Acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release gives back a slot taken by TryAcquire or Acquire. Releasing more
// than was taken panics: it is a bug that would raise the limit.
func (s *Slots) Release() {
	select {
	case <-s.ch:
	default:
		panic("pool: Slots.Release without a slot taken")
	}
}

// Limit is how many slots there are.
func (s *Slots) Limit() int { return cap(s.ch) }

// InUse is how many slots are taken now.
func (s *Slots) InUse() int { return len(s.ch) }
