package memory

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// ListRuns implements [store.RunIndex].
func (s *Store) ListRuns(ctx context.Context, f store.RunFilter, p store.Page) (store.RunPage, error) {
	k, err := scope(ctx, "")
	if err != nil {
		return store.RunPage{}, err
	}
	if err := backend.CheckRunFilter(f); err != nil {
		return store.RunPage{}, err
	}
	return s.page(k.tenant, p, func(r store.Run) bool {
		c, ok := s.records[key{k.tenant, r.ConversationID}]
		return backend.MatchesFilter(f, r, c.Agent, ok)
	})
}

// CreateNotification implements [store.NotificationLog].
func (s *Store) CreateNotification(ctx context.Context, runID, url string) (store.Notification, error) {
	k, err := scope(ctx, runID)
	if err != nil {
		return store.Notification{}, err
	}
	if err := backend.CheckNotificationURL(url); err != nil {
		return store.Notification{}, err
	}
	now := s.now()
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.Notification{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.run(k); err != nil {
		return store.Notification{}, err
	}
	if n, ok := s.notifications[k]; ok {
		if n.URL != url {
			return store.Notification{}, fmt.Errorf("%w: run %q is told another URL", store.ErrConflict, runID)
		}
		return cloneNotification(n), nil
	}
	n := store.Notification{RunID: runID, URL: url, State: store.NotificationPending, CreatedAt: now}
	s.notifications[k] = n
	return cloneNotification(n), nil
}

// GetNotification implements [store.NotificationLog].
func (s *Store) GetNotification(ctx context.Context, runID string) (store.Notification, error) {
	k, err := scope(ctx, runID)
	if err != nil {
		return store.Notification{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.notifications[k]
	if !ok {
		return store.Notification{}, fmt.Errorf("%w: notification of run %q", store.ErrNotFound, runID)
	}
	return cloneNotification(n), nil
}

func cloneNotification(n store.Notification) store.Notification {
	n.Attempts = slices.Clone(n.Attempts)
	return n
}

// ClaimNotification implements [store.Admin]: under the store's lock, the
// first due notification in claim order gets owner's lease.
func (a admin) ClaimNotification(ctx context.Context, owner string, ttl time.Duration) (store.TenantNotification, error) {
	if err := ctx.Err(); err != nil {
		return store.TenantNotification{}, err
	}
	if err := backend.CheckLease(owner, ttl); err != nil {
		return store.TenantNotification{}, err
	}
	now := a.s.now()
	if err := backend.CheckTime("lease expiry", now.Add(ttl)); err != nil {
		return store.TenantNotification{}, err
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	var (
		best  key
		found bool
	)
	for k, n := range a.s.notifications {
		r, ok := a.s.runs[k]
		if !ok || !backend.NotificationDue(n, r, now) {
			continue
		}
		if !found || backend.NotificationOrder(k.tenant.ID(), n, best.tenant.ID(), a.s.notifications[best]) < 0 {
			best, found = k, true
		}
	}
	if !found {
		return store.TenantNotification{}, fmt.Errorf("%w: no notification is due", store.ErrNotFound)
	}
	n := a.s.notifications[best]
	n.Lease = store.Lease{Owner: owner, Expires: now.Add(ttl)}
	a.s.notifications[best] = n
	return store.TenantNotification{Tenant: best.tenant, Notification: cloneNotification(n)}, nil
}

// RecordDelivery implements [store.Admin].
func (a admin) RecordDelivery(ctx context.Context, tenant tenancy.Tenant, runID, owner string, d store.Delivery) (store.Notification, error) {
	if err := ctx.Err(); err != nil {
		return store.Notification{}, err
	}
	if err := backend.CheckOwner(owner); err != nil {
		return store.Notification{}, err
	}
	if err := backend.CheckDelivery(d); err != nil {
		return store.Notification{}, err
	}
	now := a.s.now()
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	k := key{tenant, runID}
	n, ok := a.s.notifications[k]
	if !ok {
		return store.Notification{}, fmt.Errorf("%w: notification of run %q", store.ErrNotFound, runID)
	}
	if err := backend.DeliveryError(n, owner, now); err != nil {
		return store.Notification{}, err
	}
	n.Attempts = append(slices.Clone(n.Attempts), backend.Attempt(d.Attempt))
	n.State, n.NextAttemptAt, n.Lease = d.State, d.NextAttemptAt.Round(0), store.Lease{}
	a.s.notifications[k] = n
	return cloneNotification(n), nil
}
