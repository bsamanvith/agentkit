package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

const triggerColumns = `tenant, id, data, hook_id, paused, next_fire_at, version, created_at, updated_at, last_firing_seq`

const firingColumns = `id, seq, scheduled_for, at, state, run_id, reason, ended_at`

// TriggerAdmin returns the [store.TriggerAdmin] view of s. Its role must
// bypass row-level security, as [Admin]'s.
func TriggerAdmin(s *Store) store.TriggerAdmin { return admin{s} }

// PutTrigger implements [store.TriggerStore]: one transaction locks the
// trigger's row and compares its version; a create inserts with ON CONFLICT
// DO NOTHING, so of two creates of one ID only one inserts. A hook ID another
// tenant's trigger has is the unique index's violation.
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
	var out store.TriggerRecord
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			old, found, err := getTrigger(ctx, tx, tenant, t.ID, true)
			if err != nil {
				return fail("put trigger", err)
			}
			if err := backend.PutTriggerError(t.ID, ifVersion, old.Trigger.Version); err != nil {
				return err
			}
			r := t
			r.Version, r.CreatedAt, r.UpdatedAt = old.Trigger.Version+1, now, now
			if !found {
				tag, err := tx.Exec(ctx, `INSERT INTO triggers (tenant, id, data, hook_id, paused, next_fire_at, version, created_at, updated_at)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8) ON CONFLICT (tenant, id) DO NOTHING`,
					tenant, r.ID, string(r.Data), r.HookID, r.Paused, next, r.Version, now.UnixNano())
				if err != nil {
					return hookError("put trigger", r, err)
				}
				if tag.RowsAffected() == 0 { // created since the read
					return fmt.Errorf("%w: trigger %q exists", store.ErrConflict, t.ID)
				}
				out = r
				return nil
			}
			r.CreatedAt = old.Trigger.CreatedAt
			if now.Before(old.Trigger.UpdatedAt) {
				r.UpdatedAt = old.Trigger.UpdatedAt // times never go backwards
			}
			if _, err := tx.Exec(ctx, `UPDATE triggers SET data = $1, hook_id = $2, paused = $3, next_fire_at = $4, version = $5, updated_at = $6
				WHERE tenant = $7 AND id = $8`,
				string(r.Data), r.HookID, r.Paused, next, r.Version, r.UpdatedAt.UnixNano(), tenant, r.ID); err != nil {
				return hookError("put trigger", r, err)
			}
			out = r
			return nil
		})
	})
	if err != nil {
		return store.TriggerRecord{}, err
	}
	return out, nil
}

// hookError is fail, with the hook's unique violation as a conflict.
func hookError(op string, t store.TriggerRecord, err error) error {
	if code, _ := sqlState(err); code == uniqueViolation {
		return backend.HookConflict(t.ID, t.HookID)
	}
	return fail(op, err)
}

// GetTrigger implements [store.TriggerStore].
func (s *Store) GetTrigger(ctx context.Context, id string) (store.TriggerRecord, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.TriggerRecord{}, err
	}
	t, found, err := getTrigger(ctx, s.pool, tenant, id, false)
	if err != nil {
		return store.TriggerRecord{}, fail("get trigger", err)
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
	ts, err := queryTriggers(ctx, s.pool, `WHERE tenant = $1 ORDER BY id`, tenant)
	if err != nil {
		return nil, fail("list triggers", err)
	}
	out := make([]store.TriggerRecord, len(ts))
	for i, t := range ts {
		out[i] = t.Trigger
	}
	return out, nil
}

// DeleteTrigger implements [store.TriggerStore]: one transaction locks the
// trigger's row, compares its version, and deletes it and its firings.
func (s *Store) DeleteTrigger(ctx context.Context, id string, ifVersion int64) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	if err := backend.CheckDeleteTrigger(id, ifVersion); err != nil {
		return err
	}
	return retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			old, _, err := getTrigger(ctx, tx, tenant, id, true)
			if err != nil {
				return fail("delete trigger", err)
			}
			if err := backend.DeleteTriggerError(id, ifVersion, old.Trigger.Version); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM firings WHERE tenant = $1 AND trigger_id = $2`, tenant, id); err != nil {
				return fail("delete trigger", err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM triggers WHERE tenant = $1 AND id = $2`, tenant, id); err != nil {
				return fail("delete trigger", err)
			}
			return nil
		})
	})
}

// StartFiring implements [store.TriggerStore]: one transaction locks the
// trigger's row, so the starters of one trigger go one at a time, compares
// its due time and the firing's ID, writes the firing, moves the trigger on
// and drops the oldest firings beyond store.MaxFirings.
func (s *Store) StartFiring(ctx context.Context, triggerID string, f store.Firing, due, next time.Time) (store.Firing, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Firing{}, err
	}
	f, due, next, err = backend.CheckStartFiring(triggerID, f, due, next)
	if err != nil {
		return store.Firing{}, err
	}
	sched, _ := storeTime(f.ScheduledFor) // checked
	nextAt, _ := storeTime(next)
	var out store.Firing
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			t, found, err := getTrigger(ctx, tx, tenant, triggerID, true)
			if err != nil {
				return fail("start firing", err)
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
			now := s.now().UTC()
			at, err := storeTime(now)
			if err != nil {
				return fmt.Errorf("postgres: start firing: the clock reads %w", err)
			}
			r := f
			r.Seq, r.At = t.lastSeq+1, now
			if _, err := tx.Exec(ctx, `INSERT INTO firings (tenant, trigger_id, id, seq, scheduled_for, at, state)
				VALUES ($1, $2, $3, $4, $5, $6, 'pending')
				ON CONFLICT (tenant, trigger_id, id) DO UPDATE SET seq = excluded.seq, scheduled_for = excluded.scheduled_for,
					at = excluded.at, state = 'pending', run_id = '', reason = '', ended_at = NULL`,
				tenant, triggerID, r.ID, r.Seq, sched, at); err != nil {
				return fail("start firing", err)
			}
			if due.IsZero() {
				_, err = tx.Exec(ctx, `UPDATE triggers SET last_firing_seq = $1 WHERE tenant = $2 AND id = $3`, r.Seq, tenant, triggerID)
			} else {
				_, err = tx.Exec(ctx, `UPDATE triggers SET last_firing_seq = $1, next_fire_at = $2 WHERE tenant = $3 AND id = $4`, r.Seq, nextAt, tenant, triggerID)
			}
			if err != nil {
				return fail("start firing", err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM firings WHERE tenant = $1 AND trigger_id = $2 AND seq <= $3`,
				tenant, triggerID, r.Seq-store.MaxFirings); err != nil {
				return fail("start firing", err)
			}
			out = r
			return nil
		})
	})
	if err != nil {
		return store.Firing{}, err
	}
	return out, nil
}

// EndFiring implements [store.TriggerStore]: one transaction that locks the
// firing's row.
func (s *Store) EndFiring(ctx context.Context, triggerID, firingID string, end store.FiringEnd) (store.Firing, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Firing{}, err
	}
	if err := backend.CheckEndFiring(triggerID, firingID, end); err != nil {
		return store.Firing{}, err
	}
	var out store.Firing
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			fs, err := queryFirings(ctx, tx, `WHERE tenant = $1 AND trigger_id = $2 AND id = $3 FOR UPDATE`, tenant, triggerID, firingID)
			if err != nil {
				return fail("end firing", err)
			}
			if len(fs) == 0 {
				return fmt.Errorf("%w: firing %q of trigger %q", store.ErrNotFound, firingID, triggerID)
			}
			f := fs[0]
			if err := backend.EndFiringError(triggerID, firingID, f.State); err != nil {
				return err
			}
			f.State, f.RunID, f.Reason, f.EndedAt = end.State, end.RunID, end.Reason, s.now().UTC()
			if f.EndedAt.Before(f.At) {
				f.EndedAt = f.At // times never go backwards
			}
			if _, err := tx.Exec(ctx, `UPDATE firings SET state = $1, run_id = $2, reason = $3, ended_at = $4
				WHERE tenant = $5 AND trigger_id = $6 AND id = $7`,
				string(f.State), f.RunID, f.Reason, f.EndedAt.UnixNano(), tenant, triggerID, firingID); err != nil {
				return fail("end firing", err)
			}
			out = f
			return nil
		})
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
	var out []store.Firing
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, found, err := getTrigger(ctx, tx, tenant, triggerID, false)
		if err != nil {
			return fail("list firings", err)
		}
		if !found {
			return fmt.Errorf("%w: trigger %q", store.ErrNotFound, triggerID)
		}
		out, err = queryFirings(ctx, tx, `WHERE tenant = $1 AND trigger_id = $2 ORDER BY seq DESC LIMIT $3`, tenant, triggerID, n)
		if err != nil {
			return fail("list firings", err)
		}
		return nil
	})
	return out, err
}

// DueTriggers implements [store.TriggerAdmin]: a query on the triggers_due
// index (migration 0017), across tenants.
func (a admin) DueTriggers(ctx context.Context, now time.Time, limit int) ([]store.TenantTrigger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n, err := backend.FiringLimit(limit)
	if err != nil {
		return nil, err
	}
	ctx = unscoped(ctx)
	if err := a.checkRole(ctx); err != nil {
		return nil, err
	}
	ts, err := queryTriggers(ctx, a.s.admin, `WHERE NOT paused AND next_fire_at IS NOT NULL AND next_fire_at <= $1
		ORDER BY next_fire_at, tenant, id LIMIT $2`, argTime(now), n)
	if err != nil {
		return nil, fail("due triggers", err)
	}
	return tenantTriggers(ts)
}

// TriggerByHook implements [store.TriggerAdmin].
func (a admin) TriggerByHook(ctx context.Context, hookID string) (store.TenantTrigger, error) {
	if err := ctx.Err(); err != nil {
		return store.TenantTrigger{}, err
	}
	ctx = unscoped(ctx)
	if err := a.checkRole(ctx); err != nil {
		return store.TenantTrigger{}, err
	}
	var ts []tenantTrigger
	if hookID != "" && backend.TextOK(hookID) {
		var err error
		if ts, err = queryTriggers(ctx, a.s.admin, `WHERE hook_id = $1`, hookID); err != nil {
			return store.TenantTrigger{}, fail("trigger by hook", err)
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
			return nil, fmt.Errorf("postgres: trigger %q has a bad tenant: %w", t.Trigger.ID, err)
		}
		out[i] = store.TenantTrigger{Tenant: tn, Trigger: t.Trigger}
	}
	return out, nil
}

func getTrigger(ctx context.Context, q querier, tenant, id string, lock bool) (tenantTrigger, bool, error) {
	if !backend.TextOK(id) {
		return tenantTrigger{}, false, nil
	}
	where := `WHERE tenant = $1 AND id = $2`
	if lock {
		where += ` FOR UPDATE`
	}
	ts, err := queryTriggers(ctx, q, where, tenant, id)
	if err != nil || len(ts) == 0 {
		return tenantTrigger{}, false, err
	}
	return ts[0], true, nil
}

func queryTriggers(ctx context.Context, q querier, where string, args ...any) ([]tenantTrigger, error) {
	rows, err := q.Query(ctx, `SELECT `+triggerColumns+` FROM triggers `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []tenantTrigger{}
	for rows.Next() {
		var (
			t                      tenantTrigger
			data                   string
			next, created, updated *int64
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
	fs, err := queryFirings(ctx, q, `WHERE tenant = $1 AND trigger_id = $2 AND id = $3`, tenant, triggerID, id)
	if err != nil {
		return store.Firing{}, fail("get firing", err)
	}
	if len(fs) == 0 {
		return store.Firing{}, fmt.Errorf("%w: firing %q of trigger %q", store.ErrNotFound, id, triggerID)
	}
	return fs[0], nil
}

func queryFirings(ctx context.Context, q querier, where string, args ...any) ([]store.Firing, error) {
	rows, err := q.Query(ctx, `SELECT `+firingColumns+` FROM firings `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Firing{}
	for rows.Next() {
		var (
			f                  store.Firing
			state              string
			sched, at, endedAt *int64
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
