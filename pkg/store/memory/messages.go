package memory

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

// MessageAdmin returns the [store.MessageAdmin] view of s: the messages due
// across every tenant, for the process that delivers them.
func MessageAdmin(s *Store) store.MessageAdmin { return admin{s} }

// SendMessage implements [store.MessageLog].
func (s *Store) SendMessage(ctx context.Context, m store.Message) (store.Message, error) {
	k, err := scope(ctx, m.ID)
	if err != nil {
		return store.Message{}, err
	}
	m, err = backend.CheckMessage(m)
	if err != nil {
		return store.Message{}, err
	}
	now := s.now()
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.Message{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored, ok := s.messages[k]; ok {
		out, err := backend.SentMessage(m, stored)
		return cloneMessage(out), err
	}
	tk := key{k.tenant, m.ThreadID}
	th, ok := s.threads[tk]
	if !ok {
		th = store.Thread{ID: m.ThreadID, Status: store.ThreadOpen, CreatedAt: now}
	}
	if err := backend.SendToThreadError(th, m.Kind); err != nil {
		return store.Message{}, err
	}
	if m.InReplyTo != "" {
		if r, ok := s.messages[key{k.tenant, m.InReplyTo}]; !ok || r.ThreadID != m.ThreadID {
			return store.Message{}, fmt.Errorf("%w: message %q answers %q, which is not in thread %q", store.ErrInvalid, m.ID, m.InReplyTo, m.ThreadID)
		}
	}
	th.Hops++
	th.UpdatedAt = now
	m.Hop, m.State, m.CreatedAt = th.Hops, store.MessageQueued, now
	s.threads[tk] = th
	s.messages[k] = m
	return cloneMessage(m), nil
}

// GetMessage implements [store.MessageLog].
func (s *Store) GetMessage(ctx context.Context, id string) (store.Message, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return store.Message{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[k]
	if !ok {
		return store.Message{}, fmt.Errorf("%w: message %q", store.ErrNotFound, id)
	}
	return cloneMessage(m), nil
}

// ThreadMessages implements [store.MessageLog].
func (s *Store) ThreadMessages(ctx context.Context, threadID string, after, limit int) ([]store.Message, error) {
	k, err := scope(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if limit, err = backend.CheckThreadMessages(after, limit); err != nil {
		return nil, err
	}
	s.mu.Lock()
	var out []store.Message
	for mk, m := range s.messages {
		if mk.tenant == k.tenant && m.ThreadID == threadID && m.Hop > after {
			out = append(out, cloneMessage(m))
		}
	}
	s.mu.Unlock()
	slices.SortFunc(out, func(a, b store.Message) int { return a.Hop - b.Hop })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetThread implements [store.MessageLog].
func (s *Store) GetThread(ctx context.Context, id string) (store.Thread, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return store.Thread{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	th, ok := s.threads[k]
	if !ok {
		return store.Thread{}, fmt.Errorf("%w: thread %q", store.ErrNotFound, id)
	}
	return th, nil
}

// ListThreads implements [store.MessageLog].
func (s *Store) ListThreads(ctx context.Context, p store.Page) (store.ThreadPage, error) {
	k, err := scope(ctx, "")
	if err != nil {
		return store.ThreadPage{}, err
	}
	limit, start, err := backend.ParseThreadPage(p)
	if err != nil {
		return store.ThreadPage{}, err
	}
	keyOf := func(t store.Thread) backend.RunKey { return backend.RunKey{Created: t.CreatedAt, ID: t.ID} }
	s.mu.Lock()
	var all []store.Thread
	for tk, t := range s.threads {
		if tk.tenant == k.tenant {
			all = append(all, t)
		}
	}
	s.mu.Unlock()
	slices.SortFunc(all, func(a, b store.Thread) int {
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
	after := func(t store.Thread) bool {
		switch {
		case start.HasAfter && p.Order == store.Ascending:
			return start.After.Less(keyOf(t))
		case start.HasAfter:
			return keyOf(t).Less(start.After)
		case start.FromTime.IsZero():
			return true
		case p.Order == store.Ascending:
			return !t.CreatedAt.Before(start.FromTime)
		default:
			return !t.CreatedAt.After(start.FromTime)
		}
	}
	i := slices.IndexFunc(all, after)
	if i < 0 {
		return store.ThreadPage{}, nil
	}
	end := min(i+limit, len(all))
	page := store.ThreadPage{Threads: all[i:end]}
	if end < len(all) {
		page.Next = backend.ThreadCursor(p.Order, keyOf(all[end-1]))
	}
	return page, nil
}

// StopThread implements [store.MessageLog].
func (s *Store) StopThread(ctx context.Context, id, code, reason string) (store.Thread, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return store.Thread{}, err
	}
	if err := backend.CheckStop(code, reason); err != nil {
		return store.Thread{}, err
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	th, ok := s.threads[k]
	if !ok {
		return store.Thread{}, fmt.Errorf("%w: thread %q", store.ErrNotFound, id)
	}
	if th.Status != store.ThreadStopped {
		th.Status, th.StopCode, th.StopReason, th.UpdatedAt = store.ThreadStopped, code, reason, now
		s.threads[k] = th
	}
	return th, nil
}

// ClaimMessage implements [store.MessageLog].
func (s *Store) ClaimMessage(ctx context.Context, id, owner string, ttl time.Duration) (store.Message, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return store.Message{}, err
	}
	if err := backend.CheckLease(owner, ttl); err != nil {
		return store.Message{}, err
	}
	now := s.now()
	if err := backend.CheckTime("lease expiry", now.Add(ttl)); err != nil {
		return store.Message{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[k]
	if !ok {
		return store.Message{}, fmt.Errorf("%w: message %q", store.ErrNotFound, id)
	}
	if err := backend.ClaimMessageError(m, owner, now); err != nil {
		return store.Message{}, err
	}
	m = backend.Claimed(m, owner, now, now.Add(ttl))
	s.messages[k] = m
	return cloneMessage(m), nil
}

// UpdateMessage implements [store.MessageLog].
func (s *Store) UpdateMessage(ctx context.Context, id, owner string, u store.MessageUpdate) (store.Message, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return store.Message{}, err
	}
	if err := backend.CheckOwner(owner); err != nil {
		return store.Message{}, err
	}
	if err := backend.CheckMessageUpdate(u); err != nil {
		return store.Message{}, err
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[k]
	if !ok {
		return store.Message{}, fmt.Errorf("%w: message %q", store.ErrNotFound, id)
	}
	if err := backend.UpdateMessageError(m, owner, u, now); err != nil {
		return store.Message{}, err
	}
	m = backend.Updated(m, u, now)
	s.messages[k] = m
	if u.Tokens > 0 {
		tk := key{k.tenant, m.ThreadID}
		th := s.threads[tk]
		th.Tokens += u.Tokens
		th.UpdatedAt = now
		s.threads[tk] = th
	}
	return cloneMessage(m), nil
}

// ClaimDueMessage implements [store.MessageAdmin]: under the store's lock,
// the first due message in claim order gets owner's lease.
func (a admin) ClaimDueMessage(ctx context.Context, owner string, ttl time.Duration) (store.TenantMessage, error) {
	if err := ctx.Err(); err != nil {
		return store.TenantMessage{}, err
	}
	if err := backend.CheckLease(owner, ttl); err != nil {
		return store.TenantMessage{}, err
	}
	now := a.s.now()
	if err := backend.CheckTime("lease expiry", now.Add(ttl)); err != nil {
		return store.TenantMessage{}, err
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	// The lowest queued hop of each mailbox (tenant, thread, conversation).
	type mailbox struct {
		k            key
		thread, conv string
	}
	first := map[mailbox]int{}
	for k, m := range a.s.messages {
		if m.State != store.MessageQueued {
			continue
		}
		mb := mailbox{key{k.tenant, ""}, m.ThreadID, m.ToConversation}
		if h, ok := first[mb]; !ok || m.Hop < h {
			first[mb] = m.Hop
		}
	}
	var (
		best  key
		found bool
	)
	for k, m := range a.s.messages {
		blocked := first[mailbox{key{k.tenant, ""}, m.ThreadID, m.ToConversation}] < m.Hop
		if !backend.MessageDue(m, now, blocked) {
			continue
		}
		if !found || backend.MessageOrder(k.tenant.ID(), m, best.tenant.ID(), a.s.messages[best]) < 0 {
			best, found = k, true
		}
	}
	if !found {
		return store.TenantMessage{}, fmt.Errorf("%w: no message is due", store.ErrNotFound)
	}
	m := backend.Claimed(a.s.messages[best], owner, now, now.Add(ttl))
	a.s.messages[best] = m
	return store.TenantMessage{Tenant: best.tenant, Message: cloneMessage(m)}, nil
}

func cloneMessage(m store.Message) store.Message {
	m.Data = slices.Clone(m.Data)
	return m
}
