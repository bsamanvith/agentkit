package memory

import (
	"context"
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

// SendCommand implements [store.CommandLog].
func (s *Store) SendCommand(ctx context.Context, runID string, c store.Command) (store.Command, error) {
	k, err := scope(ctx, runID)
	if err != nil {
		return store.Command{}, err
	}
	if err := backend.CheckID("run", runID); err != nil {
		return store.Command{}, err
	}
	c, err = backend.CheckCommand(c)
	if err != nil {
		return store.Command{}, err
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.commands[k]
	if i := slices.IndexFunc(log, func(o store.Command) bool { return o.Key == c.Key }); i >= 0 {
		stored, err := backend.SentCommand(runID, c, log[i])
		return cloneCommand(stored), err
	}
	r, err := s.run(k)
	if err != nil {
		return store.Command{}, err
	}
	if r.Status.Final() {
		return store.Command{}, fmt.Errorf("%w: run %q is %s", store.ErrFinal, runID, r.Status)
	}
	if n := len(log); n > 0 && now.Before(log[n-1].CreatedAt) {
		now = log[n-1].CreatedAt // times never go backwards along the log
	}
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.Command{}, err
	}
	c.Seq, c.CreatedAt = int64(len(log))+1, now
	s.commands[k] = append(log, c)
	return cloneCommand(c), nil
}

// PendingCommands implements [store.CommandLog].
func (s *Store) PendingCommands(ctx context.Context, runID string, limit int) ([]store.Command, error) {
	k, err := scope(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := backend.CheckID("run", runID); err != nil {
		return nil, err
	}
	n, err := backend.Limit(store.Page{Limit: limit})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.Command
	for _, c := range s.commands[k][s.consumed[k]:] {
		if len(out) == n {
			break
		}
		out = append(out, cloneCommand(c))
	}
	return out, nil
}

// ConsumeCommands implements [store.CommandLog].
func (s *Store) ConsumeCommands(ctx context.Context, runID, owner string, through int64) error {
	k, err := scope(ctx, runID)
	if err != nil {
		return err
	}
	if err := backend.CheckID("run", runID); err != nil {
		return err
	}
	if err := backend.CheckConsume(owner, through); err != nil {
		return err
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.run(k)
	if err != nil {
		return err
	}
	log := s.commands[k]
	if err := backend.ConsumeError(r, owner, through, int64(len(log)), now); err != nil {
		return err
	}
	if through > s.consumed[k] {
		s.consumed[k] = through
	}
	return nil
}

func cloneCommand(c store.Command) store.Command {
	c.Data = slices.Clone(c.Data)
	return c
}
