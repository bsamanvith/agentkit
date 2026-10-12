package memory

import (
	"context"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

// AppendEvents implements [store.EventLog].
func (s *Store) AppendEvents(ctx context.Context, runID string, events []store.Event) error {
	k, err := scope(ctx, runID)
	if err != nil {
		return err
	}
	if err := backend.CheckID("run", runID); err != nil {
		return err
	}
	batch, err := backend.CheckEvents(events)
	if err != nil || len(batch) == 0 {
		return err
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.events[k]
	last := int64(len(log))
	var taken []store.Event
	if first := batch[0].Seq; first <= last {
		taken = log[first-1 : min(last, batch[len(batch)-1].Seq)]
	}
	add, err := backend.NewEvents(runID, batch, last, taken)
	if err != nil || len(add) == 0 {
		return err
	}
	if last > 0 && now.Before(log[last-1].CreatedAt) {
		now = log[last-1].CreatedAt // times never go backwards along the log
	}
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return err
	}
	for i := range add {
		add[i].CreatedAt = now
	}
	s.events[k] = append(log, add...)
	return nil
}

// EventsAfter implements [store.EventLog].
func (s *Store) EventsAfter(ctx context.Context, runID string, after int64, limit int) ([]store.Event, error) {
	k, err := scope(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := backend.CheckID("run", runID); err != nil {
		return nil, err
	}
	n, err := backend.EventLimit(after, limit)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.events[k]
	if after >= int64(len(log)) {
		return nil, nil
	}
	out := slices.Clone(log[after:min(int64(len(log)), after+int64(n))])
	for i := range out {
		out[i].Data = slices.Clone(out[i].Data)
	}
	return out, nil
}

// LastEventSeq implements [store.EventLog].
func (s *Store) LastEventSeq(ctx context.Context, runID string) (int64, error) {
	k, err := scope(ctx, runID)
	if err != nil {
		return 0, err
	}
	if err := backend.CheckID("run", runID); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.events[k])), nil
}
