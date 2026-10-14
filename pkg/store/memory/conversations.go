package memory

import (
	"context"
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

// CreateConversation implements [store.ConversationIndex].
func (s *Store) CreateConversation(ctx context.Context, c store.Conversation) (store.Conversation, error) {
	k, err := scope(ctx, c.ID)
	if err != nil {
		return store.Conversation{}, err
	}
	if err := backend.CheckConversation(c); err != nil {
		return store.Conversation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if had, ok := s.records[k]; ok {
		if had.Agent != c.Agent {
			return store.Conversation{}, fmt.Errorf("%w: conversation %q belongs to agent %q", store.ErrConflict, c.ID, had.Agent)
		}
		return had, nil
	}
	now := s.now()
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.Conversation{}, err
	}
	rec := store.Conversation{ID: c.ID, Agent: c.Agent, Title: c.Title, CreatedAt: now, UpdatedAt: now}
	for _, e := range s.convs[k] {
		rec.Messages += e.MessageCount()
	}
	s.records[k] = rec
	return rec, nil
}

// GetConversation implements [store.ConversationIndex].
func (s *Store) GetConversation(ctx context.Context, id string) (store.Conversation, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return store.Conversation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.records[k]
	if !ok {
		return store.Conversation{}, fmt.Errorf("%w: conversation %q", store.ErrNotFound, id)
	}
	return c, nil
}

// ListConversations implements [store.ConversationIndex].
func (s *Store) ListConversations(ctx context.Context, agent string, p store.Page) (store.ConversationPage, error) {
	k, err := scope(ctx, "")
	if err != nil {
		return store.ConversationPage{}, err
	}
	limit, start, err := backend.ParseConversationPage(p)
	if err != nil {
		return store.ConversationPage{}, err
	}
	keyOf := func(c store.Conversation) backend.RunKey { return backend.RunKey{Created: c.CreatedAt, ID: c.ID} }
	s.mu.Lock()
	var all []store.Conversation
	for rk, c := range s.records {
		if rk.tenant == k.tenant && (agent == "" || c.Agent == agent) {
			all = append(all, c)
		}
	}
	s.mu.Unlock()
	slices.SortFunc(all, func(a, b store.Conversation) int {
		switch ka, kb := keyOf(a), keyOf(b); {
		case ka.Less(kb):
			return -1
		case kb.Less(ka):
			return 1
		}
		return 0
	})
	if p.Order == store.Descending {
		slices.Reverse(all)
	}
	after := func(c store.Conversation) bool {
		switch {
		case start.HasAfter && p.Order == store.Ascending:
			return start.After.Less(keyOf(c))
		case start.HasAfter:
			return keyOf(c).Less(start.After)
		case start.FromTime.IsZero():
			return true
		case p.Order == store.Ascending:
			return !c.CreatedAt.Before(start.FromTime)
		default:
			return !c.CreatedAt.After(start.FromTime)
		}
	}
	i := slices.IndexFunc(all, after)
	if i < 0 {
		return store.ConversationPage{}, nil
	}
	end := min(i+limit, len(all))
	page := store.ConversationPage{Conversations: all[i:end]}
	if end < len(all) {
		page.Next = backend.ConversationCursor(p.Order, keyOf(all[end-1]))
	}
	return page, nil
}

// SetTitle implements [store.ConversationIndex].
func (s *Store) SetTitle(ctx context.Context, id, title string) (store.Conversation, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return store.Conversation{}, err
	}
	if err := backend.CheckText("conversation title", title); err != nil {
		return store.Conversation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.records[k]
	if !ok {
		return store.Conversation{}, fmt.Errorf("%w: conversation %q", store.ErrNotFound, id)
	}
	if c.Title != "" || title == "" {
		return c, nil
	}
	c.Title = title
	if now := s.now(); now.After(c.UpdatedAt) {
		c.UpdatedAt = now
	}
	s.records[k] = c
	return c, nil
}

// DeleteConversation implements [store.ConversationIndex].
func (s *Store) DeleteConversation(ctx context.Context, id string) error {
	k, err := scope(ctx, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[k]; !ok {
		return fmt.Errorf("%w: conversation %q", store.ErrNotFound, id)
	}
	now := s.now()
	var runs []key
	for rk, r := range s.runs {
		if rk.tenant != k.tenant || r.ConversationID != id {
			continue
		}
		if !r.Status.Final() && backend.Held(r.Lease, now) {
			return fmt.Errorf("%w: run %q of conversation %q is running", store.ErrLeased, r.ID, id)
		}
		runs = append(runs, rk)
	}
	for _, rk := range runs {
		delete(s.runs, rk)
		delete(s.events, rk)
		delete(s.checkpoints, rk)
	}
	delete(s.convs, k)
	delete(s.records, k)
	return nil
}
