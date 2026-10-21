package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// ListRuns implements [store.RunIndex]: a keyset query on the runs_tenant
// index (migration 0011).
func (s *Store) ListRuns(ctx context.Context, f store.RunFilter, p store.Page) (store.RunPage, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.RunPage{}, err
	}
	if err := backend.CheckRunFilter(f); err != nil {
		return store.RunPage{}, err
	}
	conds, args := []string{"tenant = ?"}, []any{tenant}
	if f.Status != "" {
		conds, args = append(conds, "status = ?"), append(args, string(f.Status))
	}
	switch f.Kind {
	case store.BackgroundRuns:
		conds = append(conds, "interactive = 0")
	case store.InteractiveRuns:
		conds = append(conds, "interactive = 1")
	}
	if f.Agent != "" {
		conds = append(conds, "conversation IN (SELECT id FROM conversations WHERE tenant = ? AND agent = ?)")
		args = append(args, tenant, f.Agent)
	}
	return s.page(ctx, p, strings.Join(conds, " AND "), args...)
}

const notificationColumns = `run_id, url, state, attempts, next_attempt_at, created_at, lease_owner, lease_expires`

// CreateNotification implements [store.NotificationLog].
func (s *Store) CreateNotification(ctx context.Context, runID, url string) (store.Notification, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Notification{}, err
	}
	if err := backend.CheckNotificationURL(url); err != nil {
		return store.Notification{}, err
	}
	now := s.now()
	created, err := storeTime(now)
	if err != nil {
		return store.Notification{}, err
	}
	var out store.Notification
	err = s.write(ctx, func(tx *txn) error {
		if _, err := getRun(ctx, tx, tenant, runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notifications (tenant, run_id, url, state, created_at)
			VALUES (?, ?, ?, 'pending', ?) ON CONFLICT (tenant, run_id) DO NOTHING`,
			tenant, runID, url, created); err != nil {
			return fmt.Errorf("sqlite: create notification: %w", err)
		}
		n, err := getNotification(ctx, tx, tenant, runID)
		if err != nil {
			return err
		}
		if n.URL != url {
			return fmt.Errorf("%w: run %q is told another URL", store.ErrConflict, runID)
		}
		out = n
		return nil
	})
	return out, err
}

// GetNotification implements [store.NotificationLog].
func (s *Store) GetNotification(ctx context.Context, runID string) (store.Notification, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Notification{}, err
	}
	return getNotification(ctx, s.r, tenant, runID)
}

func getNotification(ctx context.Context, q querier, tenant, runID string) (store.Notification, error) {
	n, err := scanNotification(q.QueryRowContext(ctx, `SELECT `+notificationColumns+` FROM notifications
		WHERE tenant = ? AND run_id = ?`, tenant, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Notification{}, fmt.Errorf("%w: notification of run %q", store.ErrNotFound, runID)
	}
	if err != nil {
		return store.Notification{}, fmt.Errorf("sqlite: get notification: %w", err)
	}
	return n, nil
}

// notificationDue is backend.NotificationDue in SQL, over notifications n
// joined with their runs r, with now as ?1.
const notificationDue = `n.state = 'pending' AND (n.lease_owner = '' OR n.lease_expires <= ?1)
	AND (n.next_attempt_at IS NULL OR n.next_attempt_at <= ?1)
	AND (r.status IN ` + final + ` OR (r.status = 'failed' AND r.retryable = 0))`

// ClaimNotification implements [store.Admin]. In one write transaction
// (which holds the database's write lock, so claimers take turns) it reads
// the first due notification in claim order and leases it.
func (a admin) ClaimNotification(ctx context.Context, owner string, ttl time.Duration) (store.TenantNotification, error) {
	if err := ctx.Err(); err != nil {
		return store.TenantNotification{}, err
	}
	if err := backend.CheckLease(owner, ttl); err != nil {
		return store.TenantNotification{}, err
	}
	now := a.s.now()
	expires, err := storeTime(now.Add(ttl))
	if err != nil {
		return store.TenantNotification{}, err
	}
	var out store.TenantNotification
	err = a.s.write(ctx, func(tx *txn) error {
		var tenant, runID string
		// NULLs (no attempt yet) sort first.
		err := tx.QueryRowContext(ctx, `SELECT n.tenant, n.run_id FROM notifications n
			JOIN runs r ON r.tenant = n.tenant AND r.id = n.run_id
			WHERE `+notificationDue+`
			ORDER BY n.next_attempt_at, n.created_at, n.tenant, n.run_id LIMIT 1`, argTime(now)).Scan(&tenant, &runID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: no notification is due", store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("sqlite: claim notification: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notifications SET lease_owner = ?, lease_expires = ?
			WHERE tenant = ? AND run_id = ?`, owner, expires, tenant, runID); err != nil {
			return fmt.Errorf("sqlite: claim notification: %w", err)
		}
		n, err := getNotification(ctx, tx, tenant, runID)
		if err != nil {
			return err
		}
		t, err := tenancy.NewTenant(tenant)
		if err != nil {
			return fmt.Errorf("sqlite: notification of run %q has a bad tenant: %w", runID, err)
		}
		out = store.TenantNotification{Tenant: t, Notification: n}
		return nil
	})
	return out, err
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
	next, err := storeTime(d.NextAttemptAt)
	if err != nil {
		return store.Notification{}, err
	}
	now := a.s.now()
	var out store.Notification
	err = a.s.write(ctx, func(tx *txn) error {
		n, err := getNotification(ctx, tx, tenant.ID(), runID)
		if err != nil {
			return err
		}
		if err := backend.DeliveryError(n, owner, now); err != nil {
			return err
		}
		attempts := backend.EncodeAttempts(append(n.Attempts, backend.Attempt(d.Attempt)))
		if _, err := tx.ExecContext(ctx, `UPDATE notifications SET state = ?, attempts = ?, next_attempt_at = ?,
			lease_owner = '', lease_expires = NULL WHERE tenant = ? AND run_id = ?`,
			string(d.State), attempts, next, tenant.ID(), runID); err != nil {
			return fmt.Errorf("sqlite: record delivery: %w", err)
		}
		out, err = getNotification(ctx, tx, tenant.ID(), runID)
		return err
	})
	return out, err
}

// scanNotification reads one row of notificationColumns.
func scanNotification(row scanner) (store.Notification, error) {
	var (
		n                      store.Notification
		state, attempts        string
		next, created, expires sql.NullInt64
	)
	if err := row.Scan(&n.RunID, &n.URL, &state, &attempts, &next, &created, &n.Lease.Owner, &expires); err != nil {
		return store.Notification{}, err
	}
	n.State = store.NotificationState(state)
	n.NextAttemptAt, n.CreatedAt, n.Lease.Expires = loadTime(next), loadTime(created), loadTime(expires)
	as, err := backend.DecodeAttempts(attempts)
	if err != nil {
		return store.Notification{}, fmt.Errorf("notification of run %q: %w", n.RunID, err)
	}
	n.Attempts = as
	return n, nil
}
