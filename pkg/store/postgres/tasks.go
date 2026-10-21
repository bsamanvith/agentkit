package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// ListRuns implements [store.RunIndex]: a keyset query on the runs_tenant
// index (migration 0012).
func (s *Store) ListRuns(ctx context.Context, f store.RunFilter, p store.Page) (store.RunPage, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.RunPage{}, err
	}
	if err := backend.CheckRunFilter(f); err != nil {
		return store.RunPage{}, err
	}
	conds, args := []string{"tenant = $1"}, []any{tenant}
	next := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if f.Status != "" {
		conds = append(conds, "status = "+next(string(f.Status)))
	}
	switch f.Kind {
	case store.BackgroundRuns:
		conds = append(conds, "NOT interactive")
	case store.InteractiveRuns:
		conds = append(conds, "interactive")
	}
	if f.Agent != "" {
		conds = append(conds, "conversation IN (SELECT id FROM conversations WHERE tenant = $1 AND agent = "+next(f.Agent)+")")
	}
	return s.page(ctx, p, strings.Join(conds, " AND "), args...)
}

const notificationColumns = `run_id, url, state, attempts, next_attempt_at, created_at, lease_owner, lease_expires`

// CreateNotification implements [store.NotificationLog]: one transaction
// that locks the run's row, so it cannot be deleted in between.
func (s *Store) CreateNotification(ctx context.Context, runID, url string) (store.Notification, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Notification{}, err
	}
	if err := backend.CheckNotificationURL(url); err != nil {
		return store.Notification{}, err
	}
	var out store.Notification
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if _, err := lockRun(ctx, tx, tenant, runID); err != nil {
				return err
			}
			created, err := storeTime(s.now())
			if err != nil {
				return fmt.Errorf("postgres: create notification: the clock reads %w", err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO notifications (tenant, run_id, url, state, created_at)
				VALUES ($1, $2, $3, 'pending', $4) ON CONFLICT (tenant, run_id) DO NOTHING`,
				tenant, runID, url, created); err != nil {
				return fail("create notification", err)
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
	})
	return out, err
}

// GetNotification implements [store.NotificationLog].
func (s *Store) GetNotification(ctx context.Context, runID string) (store.Notification, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Notification{}, err
	}
	return getNotification(ctx, s.pool, tenant, runID)
}

func getNotification(ctx context.Context, q querier, tenant, runID string) (store.Notification, error) {
	if !backend.TextOK(runID) {
		return store.Notification{}, fmt.Errorf("%w: notification of run %q", store.ErrNotFound, runID)
	}
	n, err := scanNotification(q.QueryRow(ctx, `SELECT `+notificationColumns+` FROM notifications
		WHERE tenant = $1 AND run_id = $2`, tenant, runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Notification{}, fmt.Errorf("%w: notification of run %q", store.ErrNotFound, runID)
	}
	if err != nil {
		return store.Notification{}, fail("get notification", err)
	}
	return n, nil
}

// claimNotification is ClaimNotification's statement: the first due
// notification in claim order whose row no other claimer has locked (FOR
// UPDATE SKIP LOCKED) is leased to $2 until $3. $1 is now.
const claimNotification = `WITH next AS (
	SELECT n.tenant AS t, n.run_id AS i FROM notifications n
	JOIN runs r ON r.tenant = n.tenant AND r.id = n.run_id
	WHERE n.state = 'pending' AND (n.lease_owner = '' OR n.lease_expires <= $1)
		AND (n.next_attempt_at IS NULL OR n.next_attempt_at <= $1)
		AND (r.status IN ` + final + ` OR (r.status = 'failed' AND NOT r.retryable))
	ORDER BY n.next_attempt_at NULLS FIRST, n.created_at, n.tenant, n.run_id
	LIMIT 1 FOR UPDATE OF n SKIP LOCKED
)
UPDATE notifications SET lease_owner = $2, lease_expires = $3
FROM next WHERE notifications.tenant = next.t AND notifications.run_id = next.i
RETURNING tenant, ` + notificationColumns

// ClaimNotification implements [store.Admin], in one statement
// (claimNotification).
func (a admin) ClaimNotification(ctx context.Context, owner string, ttl time.Duration) (store.TenantNotification, error) {
	if err := ctx.Err(); err != nil {
		return store.TenantNotification{}, err
	}
	if err := backend.CheckLease(owner, ttl); err != nil {
		return store.TenantNotification{}, err
	}
	ctx = unscoped(ctx)
	if err := a.checkRole(ctx); err != nil {
		return store.TenantNotification{}, err
	}
	var out store.TenantNotification
	err := retry(ctx, func() error {
		now := a.s.now()
		expires, err := storeTime(now.Add(ttl))
		if err != nil {
			return err
		}
		var tenant string
		n, err := scanNotification(a.s.admin.QueryRow(ctx, claimNotification, argTime(now), owner, expires), &tenant)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: no notification is due", store.ErrNotFound)
		}
		if err != nil {
			return fail("claim notification", err)
		}
		t, err := tenancy.NewTenant(tenant)
		if err != nil {
			return fmt.Errorf("postgres: notification of run %q has a bad tenant: %w", n.RunID, err)
		}
		out = store.TenantNotification{Tenant: t, Notification: n}
		return nil
	})
	return out, err
}

// RecordDelivery implements [store.Admin]: one transaction that locks the
// notification's row.
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
	ctx = unscoped(ctx)
	if err := a.checkRole(ctx); err != nil {
		return store.Notification{}, err
	}
	var out store.Notification
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, a.s.admin, func(tx pgx.Tx) error {
			if backend.TextOK(runID) {
				var one int
				err := tx.QueryRow(ctx, `SELECT 1 FROM notifications WHERE tenant = $1 AND run_id = $2 FOR UPDATE`,
					tenant.ID(), runID).Scan(&one)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return fail("record delivery", err)
				}
			}
			n, err := getNotification(ctx, tx, tenant.ID(), runID)
			if err != nil {
				return err
			}
			if err := backend.DeliveryError(n, owner, a.s.now()); err != nil {
				return err
			}
			attempts := backend.EncodeAttempts(append(n.Attempts, backend.Attempt(d.Attempt)))
			if _, err := tx.Exec(ctx, `UPDATE notifications SET state = $1, attempts = $2, next_attempt_at = $3,
				lease_owner = '', lease_expires = NULL WHERE tenant = $4 AND run_id = $5`,
				string(d.State), attempts, next, tenant.ID(), runID); err != nil {
				return fail("record delivery", err)
			}
			out, err = getNotification(ctx, tx, tenant.ID(), runID)
			return err
		})
	})
	return out, err
}

// scanNotification reads one row of notificationColumns, after the columns
// in pre.
func scanNotification(row pgx.Row, pre ...any) (store.Notification, error) {
	var (
		n                      store.Notification
		state, attempts        string
		next, created, expires *int64
	)
	if err := row.Scan(append(pre, &n.RunID, &n.URL, &state, &attempts, &next, &created, &n.Lease.Owner, &expires)...); err != nil {
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
