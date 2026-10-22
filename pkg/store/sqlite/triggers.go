package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

const triggerColumns = `tenant, id, data, hook_id, paused, next_fire_at, version, created_at, updated_at`

const firingColumns = `id, seq, scheduled_for, at, state, run_id, reason, ended_at`

// TriggerAdmin returns the [store.TriggerAdmin] view of s.
func TriggerAdmin(s *Store) store.TriggerAdmin { return admin{s} }

// PutTrigger implements [store.TriggerStore]: one write transaction (which
// holds the database's write lock) reads the stored version and the hook's
// owner, compares, then inserts or updates.
func (s *Store) PutTrigger(ctx context.Context, t store.TriggerRecord, ifVersion int64) (store.TriggerRecord, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.TriggerRecord{}, err
	}
	t, err = backend.CheckTrigger(t, ifVersion)
	if err != nil {
		return store.TriggerRecord{}, err
	}
	now := s.now().UTC()
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.TriggerRecord{}, err
	}
	next, err := storeTime(t.NextFireAt)
	if err != nil {
		return store.TriggerRecord{}, err
	}
	err = s.write(ctx, func(tx *txn) error {
		old, found, err := getTrigger(ctx, tx, tenant, t.ID)
		if err != nil {
			return fmt.Errorf("sqlite: put trigger: %w", err)
		}
		if err := backend.PutTriggerError(t.ID, ifVersion, old.Trigger.Version); err != nil {
			return err
		}
		if t.HookID != "" {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM triggers WHERE hook_id = ? AND NOT (tenant = ? AND id = ?)`,
				t.HookID, tenant, t.ID).Scan(&n); err != nil {
				return fmt.Errorf("sqlite: put trigger: %w", err)
			}
			if n > 0 {
				return backend.HookConflict(t.ID, t.HookID)
			}
		}
		t.Version, t.CreatedAt, t.UpdatedAt = old.Trigger.Version+1, now, now
		if !found {
			_, err = tx.ExecContext(ctx, `INSERT INTO triggers (tenant, id, data, hook_id, paused, next_fire_at, version, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, tenant, t.ID, string(t.Data), t.HookID, t.Paused, next, t.Version, now.UnixNano(), now.UnixNano())
		} else {
			t.CreatedAt = old.Trigger.CreatedAt
			if now.Before(old.Trigger.UpdatedAt) {
				t.UpdatedAt = old.Trigger.UpdatedAt // times never go backwards
			}
			_, err = tx.ExecContext(ctx, `UPDATE triggers SET data = ?, hook_id = ?, paused = ?, next_fire_at = ?, version = ?, updated_at = ?
				WHERE tenant = ? AND id = ?`, string(t.Data), t.HookID, t.Paused, next, t.Version, t.UpdatedAt.UnixNano(), tenant, t.ID)
		}
		if err != nil {
			return fmt.Errorf("sqlite: put trigger: %w", err)
		}
		return nil
	})
	if err != nil {
		return store.TriggerRecord{}, err
	}
	return t, nil
}

// GetTrigger implements [store.TriggerStore].
func (s *Store) GetTrigger(ctx context.Context, id string) (store.TriggerRecord, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.TriggerRecord{}, err
	}
	t, found, err := getTrigger(ctx, s.r, tenant, id)
	if err != nil {
		return store.TriggerRecord{}, fmt.Errorf("sqlite: get trigger: %w", err)
	}
	if !found {
		return store.TriggerRecord{}, fmt.Errorf("%w: trigger %q", store.ErrNotFound, id)
	}
	return t.Trigger, nil
}

// ListTriggers implements [store.TriggerStore].
func (s *Store) ListTriggers(ctx context.Context) ([]store.TriggerRecord, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	ts, err := queryTriggers(ctx, s.r, `WHERE tenant = ? ORDER BY id`, tenant)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list triggers: %w", err)
	}
	out := make([]store.TriggerRecord, len(ts))
	for i, t := range ts {
		out[i] = t.Trigger
	}
	return out, nil
}

// DeleteTrigger implements [store.TriggerStore].
func (s *Store) DeleteTrigger(ctx context.Context, id string, ifVersion int64) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	if err := backend.CheckDeleteTrigger(id, ifVersion); err != nil {
		return err
	}
	return s.write(ctx, func(tx *txn) error {
		old, _, err := getTrigger(ctx, tx, tenant, id)
		if err != nil {
			return fmt.Errorf("sqlite: delete trigger: %w", err)
		}
		if err := backend.DeleteTriggerError(id, ifVersion, old.Trigger.Version); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM firings WHERE tenant = ? AND trigger_id = ?`, tenant, id); err != nil {
			return fmt.Errorf("sqlite: delete trigger: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM triggers WHERE tenant = ? AND id = ?`, tenant, id); err != nil {
			return fmt.Errorf("sqlite: delete trigger: %w", err)
		}
		return nil
	})
}

// StartFiring implements [store.TriggerStore]: in one write transaction, the
// trigger's due time and the firing's ID are compared, the firing written,
// the trigger moved on, and the oldest firings beyond store.MaxFirings
// dropped.
func (s *Store) StartFiring(ctx context.Context, triggerID string, f store.Firing, due, next time.Time) (store.Firing, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Firing{}, err
	}
	f, due, next, err = backend.CheckStartFiring(triggerID, f, due, next)
	if err != nil {
		return store.Firing{}, err
	}
	now := s.now().UTC()
	at, err := storeTime(now)
	if err != nil {
		return store.Firing{}, err
	}
	sched, _ := storeTime(f.ScheduledFor) // checked
	nextAt, _ := storeTime(next)
	err = s.write(ctx, func(tx *txn) error {
		t, found, err := getTrigger(ctx, tx, tenant, triggerID)
		if err != nil {
			return fmt.Errorf("sqlite: start firing: %w", err)
		}
		if !found {
			return fmt.Errorf("%w: trigger %q", store.ErrNotFound, triggerID)
		}
		old, err := getFiring(ctx, tx, tenant, triggerID, f.ID)
		exists := err == nil
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := backend.StartFiringError(t.Trigger, f.ID, exists, old.State, due); err != nil {
			return err
		}
		f.Seq, f.At = t.lastSeq+1, now
		if _, err := tx.ExecContext(ctx, `INSERT INTO firings (tenant, trigger_id, id, seq, scheduled_for, at, state)
			VALUES (?, ?, ?, ?, ?, ?, 'pending')
			ON CONFLICT (tenant, trigger_id, id) DO UPDATE SET seq = excluded.seq, scheduled_for = excluded.scheduled_for,
				at = excluded.at, state = 'pending', run_id = '', reason = '', ended_at = NULL`,
			tenant, triggerID, f.ID, f.Seq, sched, at); err != nil {
			return fmt.Errorf("sqlite: start firing: %w", err)
		}
		set := `UPDATE triggers SET last_firing_seq = ? WHERE tenant = ? AND id = ?`
		args := []any{f.Seq, tenant, triggerID}
		if !due.IsZero() {
			set = `UPDATE triggers SET last_firing_seq = ?, next_fire_at = ? WHERE tenant = ? AND id = ?`
			args = []any{f.Seq, nextAt, tenant, triggerID}
		}
		if _, err := tx.ExecContext(ctx, set, args...); err != nil {
			return fmt.Errorf("sqlite: start firing: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM firings WHERE tenant = ? AND trigger_id = ? AND seq <= ?`,
			tenant, triggerID, f.Seq-store.MaxFirings); err != nil {
			return fmt.Errorf("sqlite: start firing: %w", err)
		}
		return nil
	})
	if err != nil {
		return store.Firing{}, err
	}
	return f, nil
}

// EndFiring implements [store.TriggerStore].
func (s *Store) EndFiring(ctx context.Context, triggerID, firingID string, end store.FiringEnd) (store.Firing, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Firing{}, err
	}
	if err := backend.CheckEndFiring(triggerID, firingID, end); err != nil {
		return store.Firing{}, err
	}
	now := s.now().UTC()
	var out store.Firing
	err = s.write(ctx, func(tx *txn) error {
		f, err := getFiring(ctx, tx, tenant, triggerID, firingID)
		if err != nil {
			return err
		}
		if err := backend.EndFiringError(triggerID, firingID, f.State); err != nil {
			return err
		}
		f.State, f.RunID, f.Reason, f.EndedAt = end.State, end.RunID, end.Reason, now
		if f.EndedAt.Before(f.At) {
			f.EndedAt = f.At // times never go backwards
		}
		if _, err := tx.ExecContext(ctx, `UPDATE firings SET state = ?, run_id = ?, reason = ?, ended_at = ?
			WHERE tenant = ? AND trigger_id = ? AND id = ?`,
			string(f.State), f.RunID, f.Reason, f.EndedAt.UnixNano(), tenant, triggerID, firingID); err != nil {
			return fmt.Errorf("sqlite: end firing: %w", err)
		}
		out = f
		return nil
	})
	return out, err
}

// ListFirings implements [store.TriggerStore].
func (s *Store) ListFirings(ctx context.Context, triggerID string, limit int) ([]store.Firing, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	n, err := backend.FiringLimit(limit)
	if err != nil {
		return nil, err
	}
	if _, found, err := getTrigger(ctx, s.r, tenant, triggerID); err != nil {
		return nil, fmt.Errorf("sqlite: list firings: %w", err)
	} else if !found {
		return nil, fmt.Errorf("%w: trigger %q", store.ErrNotFound, triggerID)
	}
	out, err := queryFirings(ctx, s.r, `WHERE tenant = ? AND trigger_id = ? ORDER BY seq DESC LIMIT ?`, tenant, triggerID, n)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list firings: %w", err)
	}
	return out, nil
}

// DueTriggers implements [store.TriggerAdmin]: a query on the triggers_due
// index (migration 0016).
func (a admin) DueTriggers(ctx context.Context, now time.Time, limit int) ([]store.TenantTrigger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n, err := backend.FiringLimit(limit)
	if err != nil {
		return nil, err
	}
	ts, err := queryTriggers(ctx, a.s.r, `WHERE paused = 0 AND next_fire_at IS NOT NULL AND next_fire_at <= ?
		ORDER BY next_fire_at, tenant, id LIMIT ?`, argTime(now), n)
	if err != nil {
		return nil, fmt.Errorf("sqlite: due triggers: %w", err)
	}
	return tenantTriggers(ts)
}

// TriggerByHook implements [store.TriggerAdmin].
func (a admin) TriggerByHook(ctx context.Context, hookID string) (store.TenantTrigger, error) {
	if err := ctx.Err(); err != nil {
		return store.TenantTrigger{}, err
	}
	var ts []tenantTrigger
	if hookID != "" && backend.TextOK(hookID) {
		var err error
		if ts, err = queryTriggers(ctx, a.s.r, `WHERE hook_id = ?`, hookID); err != nil {
			return store.TenantTrigger{}, fmt.Errorf("sqlite: trigger by hook: %w", err)
		}
	}
	if len(ts) == 0 {
		return store.TenantTrigger{}, fmt.Errorf("%w: no trigger has hook %q", store.ErrNotFound, hookID)
	}
	out, err := tenantTriggers(ts[:1])
	if err != nil {
		return store.TenantTrigger{}, err
	}
	return out[0], nil
}

// tenantTrigger is a stored trigger with its tenant's id and its last
// firing's Seq.
type tenantTrigger struct {
	tenant  string
	Trigger store.TriggerRecord
	lastSeq int64
}

func tenantTriggers(ts []tenantTrigger) ([]store.TenantTrigger, error) {
	out := make([]store.TenantTrigger, len(ts))
	for i, t := range ts {
		tn, err := tenancy.NewTenant(t.tenant)
		if err != nil {
			return nil, fmt.Errorf("sqlite: a stored tenant: %w", err)
		}
		out[i] = store.TenantTrigger{Tenant: tn, Trigger: t.Trigger}
	}
	return out, nil
}

func getTrigger(ctx context.Context, q querier, tenant, id string) (tenantTrigger, bool, error) {
	ts, err := queryTriggers(ctx, q, `WHERE tenant = ? AND id = ?`, tenant, id)
	if err != nil || len(ts) == 0 {
		return tenantTrigger{}, false, err
	}
	return ts[0], true, nil
}

func queryTriggers(ctx context.Context, q querier, where string, args ...any) ([]tenantTrigger, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+triggerColumns+`, last_firing_seq FROM triggers `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []tenantTrigger{}
	for rows.Next() {
		var (
			t                      tenantTrigger
			data                   string
			next, created, updated sql.NullInt64
		)
		if err := rows.Scan(&t.tenant, &t.Trigger.ID, &data, &t.Trigger.HookID, &t.Trigger.Paused, &next, &t.Trigger.Version, &created, &updated, &t.lastSeq); err != nil {
			return nil, err
		}
		t.Trigger.Data = []byte(data)
		t.Trigger.NextFireAt, t.Trigger.CreatedAt, t.Trigger.UpdatedAt = loadTime(next), loadTime(created), loadTime(updated)
		out = append(out, t)
	}
	return out, rows.Err()
}

func getFiring(ctx context.Context, q querier, tenant, triggerID, id string) (store.Firing, error) {
	fs, err := queryFirings(ctx, q, `WHERE tenant = ? AND trigger_id = ? AND id = ?`, tenant, triggerID, id)
	if err != nil {
		return store.Firing{}, fmt.Errorf("sqlite: get firing: %w", err)
	}
	if len(fs) == 0 {
		return store.Firing{}, fmt.Errorf("%w: firing %q of trigger %q", store.ErrNotFound, id, triggerID)
	}
	return fs[0], nil
}

func queryFirings(ctx context.Context, q querier, where string, args ...any) ([]store.Firing, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+firingColumns+` FROM firings `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Firing{}
	for rows.Next() {
		var (
			f                  store.Firing
			state              string
			sched, at, endedAt sql.NullInt64
		)
		if err := rows.Scan(&f.ID, &f.Seq, &sched, &at, &state, &f.RunID, &f.Reason, &endedAt); err != nil {
			return nil, err
		}
		f.State = store.FiringState(state)
		f.ScheduledFor, f.At, f.EndedAt = loadTime(sched), loadTime(at), loadTime(endedAt)
		out = append(out, f)
	}
	return out, rows.Err()
}
