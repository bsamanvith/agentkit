package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

const runColumns = `id, conversation, status,
	input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
	error, retryable, next_attempt_at, attempts, pending,
	created_at, finished_at, lease_owner, lease_expires, interactive,
	lane, priority, start_at, pool, agent_version, model, user_id`

// final is the SQL list of final statuses (store.Status.Final).
const final = `('completed', 'rejected', 'canceled')`

// resumable is backend.Resumable in SQL, with :now as ?1.
const resumable = `(interactive = 0 AND start_at <= ?1 AND ((status = 'running' AND (lease_owner = '' OR lease_expires <= ?1))
	OR (status = 'failed' AND retryable = 1 AND (next_attempt_at IS NULL OR next_attempt_at <= ?1))))`

// queued is backend.Queued in SQL, and due is store.Run.Due: the predicate
// and the expression of the runs_queue index (migration 0009), which a query
// must repeat word for word for SQLite to use it.
const (
	queued = `interactive = 0 AND (status = 'running' OR (status = 'failed' AND retryable = 1))`
	due    = `max(start_at, coalesce(lease_expires, start_at), coalesce(next_attempt_at, start_at))`
)

// Create implements [store.RunStore].
//
// The INSERT comes first; the primary key and the partial unique index on
// unfinished runs decide, and when it fails the stored rows say why: the same
// run (a retry), the id in another conversation, or another unfinished run.
func (s *Store) Create(ctx context.Context, id, conversationID string, opts ...store.CreateOption) (store.Run, error) {
	o := store.NewCreateOptions(opts...)
	tenant, err := scope(ctx)
	if err != nil {
		return store.Run{}, err
	}
	if err := backend.CheckID("run", id); err != nil {
		return store.Run{}, err
	}
	if err := backend.CheckID("conversation", conversationID); err != nil {
		return store.Run{}, err
	}
	now := s.now()
	at, err := storeTime(now)
	if err != nil {
		return store.Run{}, fmt.Errorf("sqlite: create run: the clock reads %w", err)
	}
	c, err := backend.CheckCreate(o, now)
	if err != nil {
		return store.Run{}, err
	}
	expires, _ := storeTime(c.Lease.Expires) // checked by CheckCreate
	var run store.Run
	err = s.write(ctx, func(tx *txn) error {
		_, insertErr := tx.ExecContext(ctx, `INSERT INTO runs (tenant, id, conversation, status, created_at, interactive,
			lane, priority, start_at, pool, agent_version, model, user_id, lease_owner, lease_expires)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, tenant, id, conversationID, string(store.StatusRunning), at, o.Interactive,
			o.Lane, o.Priority, c.Start.UnixNano(), o.Pool, o.AgentVersion, o.Model, o.User, c.Lease.Owner, expires)
		if insertErr == nil {
			run = store.Run{
				ID: id, ConversationID: conversationID, Status: store.StatusRunning, CreatedAt: now,
				Interactive: o.Interactive, Lane: o.Lane, Priority: o.Priority, StartAt: c.Start, Pool: o.Pool, AgentVersion: o.AgentVersion, Model: o.Model, User: o.User, Lease: c.Lease,
			}
			return nil
		}
		r, err := getRun(ctx, tx, tenant, id)
		switch {
		case err == nil && r.ConversationID != conversationID:
			return fmt.Errorf("%w: run %q belongs to conversation %q", store.ErrConflict, id, r.ConversationID)
		case err == nil:
			run = r
			return nil
		case !errors.Is(err, store.ErrNotFound):
			return errors.Join(insertErr, err)
		}
		u, err := unfinished(ctx, tx, tenant, conversationID)
		switch {
		case err == nil:
			return fmt.Errorf("%w: conversation %q has run %q", store.ErrUnfinished, conversationID, u.ID)
		case !errors.Is(err, store.ErrNotFound):
			return errors.Join(insertErr, err)
		}
		return fmt.Errorf("sqlite: create run: %w", insertErr)
	})
	return run, err
}

// Get implements [store.RunStore].
func (s *Store) Get(ctx context.Context, id string) (store.Run, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Run{}, err
	}
	return getRun(ctx, s.r, tenant, id)
}

// querier is what *sql.DB and *sql.Tx share.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getRun(ctx context.Context, q querier, tenant, id string) (store.Run, error) {
	r, err := scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE tenant = ? AND id = ?`, tenant, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Run{}, fmt.Errorf("%w: run %q", store.ErrNotFound, id)
	}
	if err != nil {
		return store.Run{}, fmt.Errorf("sqlite: run %q: %w", id, err)
	}
	return r, nil
}

func unfinished(ctx context.Context, q querier, tenant, conversationID string) (store.Run, error) {
	r, err := scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs
		WHERE tenant = ? AND conversation = ? AND status NOT IN `+final, tenant, conversationID))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Run{}, fmt.Errorf("%w: conversation %q has no unfinished run", store.ErrNotFound, conversationID)
	}
	if err != nil {
		return store.Run{}, fmt.Errorf("sqlite: unfinished run of %q: %w", conversationID, err)
	}
	return r, nil
}

// Unfinished implements [store.RunStore].
func (s *Store) Unfinished(ctx context.Context, conversationID string) (store.Run, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Run{}, err
	}
	if err := backend.CheckID("conversation", conversationID); err != nil {
		return store.Run{}, err
	}
	return unfinished(ctx, s.r, tenant, conversationID)
}

// List implements [store.RunStore].
func (s *Store) List(ctx context.Context, conversationID string, p store.Page) (store.RunPage, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.RunPage{}, err
	}
	if err := backend.CheckID("conversation", conversationID); err != nil {
		return store.RunPage{}, err
	}
	return s.page(ctx, p, "tenant = ? AND conversation = ?", tenant, conversationID)
}

// ListSuspended implements [store.RunStore].
func (s *Store) ListSuspended(ctx context.Context, p store.Page) (store.RunPage, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.RunPage{}, err
	}
	return s.page(ctx, p, "tenant = ? AND status = 'suspended'", tenant)
}

// page returns one page of the runs that match where, a keyset query on
// (created_at, id).
func (s *Store) page(ctx context.Context, p store.Page, where string, args ...any) (store.RunPage, error) {
	limit, start, err := backend.ParseRunPage(p)
	if err != nil {
		return store.RunPage{}, err
	}
	conds := []string{where}
	asc := p.Order == store.Ascending
	switch {
	case start.HasAfter && asc:
		conds, args = append(conds, "(created_at, id) > (?, ?)"), append(args, argTime(start.After.Created), start.After.ID)
	case start.HasAfter:
		conds, args = append(conds, "(created_at, id) < (?, ?)"), append(args, argTime(start.After.Created), start.After.ID)
	case !start.FromTime.IsZero() && asc:
		conds, args = append(conds, "created_at >= ?"), append(args, argTime(start.FromTime))
	case !start.FromTime.IsZero():
		conds, args = append(conds, "created_at <= ?"), append(args, argTime(start.FromTime))
	}
	order := "created_at ASC, id ASC"
	if !asc {
		order = "created_at DESC, id DESC"
	}
	rows, err := s.r.QueryContext(ctx, `SELECT `+runColumns+` FROM runs WHERE `+
		strings.Join(conds, " AND ")+` ORDER BY `+order+` LIMIT CAST(? AS INTEGER)`, append(args, limit+1)...)
	if err != nil {
		return store.RunPage{}, fmt.Errorf("sqlite: list runs: %w", err)
	}
	runs, err := scanRuns(rows, nil)
	if err != nil {
		return store.RunPage{}, fmt.Errorf("sqlite: list runs: %w", err)
	}
	var page store.RunPage
	if len(runs) > limit {
		runs = runs[:limit]
		last := runs[limit-1]
		page.Next = backend.RunCursor(p.Order, backend.RunKey{Created: last.CreatedAt, ID: last.ID})
	}
	page.Runs = runs
	return page, nil
}

// ListResumable implements [store.RunStore].
func (s *Store) ListResumable(ctx context.Context, now time.Time, limit int) ([]store.Run, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	n, err := backend.Limit(store.Page{Limit: limit})
	if err != nil {
		return nil, err
	}
	rows, err := s.r.QueryContext(ctx, `SELECT `+runColumns+` FROM runs
		WHERE tenant = ?2 AND status IN ('running', 'failed') AND `+resumable+`
		ORDER BY created_at, id LIMIT CAST(?3 AS INTEGER)`, argTime(now), tenant, n)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list resumable runs: %w", err)
	}
	runs, err := scanRuns(rows, nil)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list resumable runs: %w", err)
	}
	return runs, nil
}

// Transition implements [store.RunStore].
//
// One conditional UPDATE (WHERE status = from) moves the run; only when it
// changes no row is the run read, to tell why.
func (s *Store) Transition(ctx context.Context, id string, from, to store.Status, d store.Details) (store.Run, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Run{}, err
	}
	pending, invalid := backend.CheckTransition(from, to, d)
	var next any
	if invalid == nil {
		next, invalid = storeTime(d.NextAttemptAt)
	}
	var finished any
	if to.Final() {
		if finished, err = storeTime(s.now()); err != nil {
			return store.Run{}, fmt.Errorf("sqlite: transition: the clock reads %w", err)
		}
	}
	var encPending string
	if len(pending) > 0 {
		b, err := json.Marshal(pending)
		if err != nil {
			return store.Run{}, fmt.Errorf("sqlite: transition: %w", err)
		}
		encPending = string(b)
	}
	attempt := 0
	if to == store.StatusFailed {
		attempt = 1
	}

	var run store.Run
	err = s.write(ctx, func(tx *txn) error {
		if invalid == nil {
			res, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?,
				input_tokens = input_tokens + ?, output_tokens = output_tokens + ?,
				cache_read_tokens = cache_read_tokens + ?, cache_write_tokens = cache_write_tokens + ?,
				error = ?, retryable = ?, next_attempt_at = ?, pending = ?,
				attempts = attempts + ?, finished_at = ?
				WHERE tenant = ? AND id = ? AND status = ?`,
				string(to),
				d.Usage.InputTokens, d.Usage.OutputTokens, d.Usage.CacheReadTokens, d.Usage.CacheWriteTokens,
				d.Error, d.Retryable, next, encPending,
				attempt, finished,
				tenant, id, string(from))
			if err != nil {
				return fmt.Errorf("sqlite: transition: %w", err)
			}
			if n, err := res.RowsAffected(); err != nil {
				return fmt.Errorf("sqlite: transition: %w", err)
			} else if n == 1 {
				run, err = getRun(ctx, tx, tenant, id)
				return err
			}
		}
		r, err := getRun(ctx, tx, tenant, id)
		switch {
		case err != nil:
			return err
		case r.Status.Final():
			return fmt.Errorf("%w: run %q is %s", store.ErrFinal, id, r.Status)
		case invalid != nil:
			return invalid
		}
		return fmt.Errorf("%w: run %q is %s, not %s", store.ErrConflict, id, r.Status, from)
	})
	return run, err
}

// Claim implements [store.Leaser]: one UPDATE that takes the lease only
// where nobody else holds it.
func (s *Store) Claim(ctx context.Context, runID, owner string, ttl time.Duration) (store.Lease, error) {
	return s.lease(ctx, runID, owner, ttl, true)
}

// Renew implements [store.Leaser]: one UPDATE that extends the lease only
// where owner holds it, unexpired.
func (s *Store) Renew(ctx context.Context, runID, owner string, ttl time.Duration) (store.Lease, error) {
	return s.lease(ctx, runID, owner, ttl, false)
}

// lease gives owner the lease on the run for ttl from now: for a claim where
// nobody else holds it, for a renewal where owner holds it, unexpired.
func (s *Store) lease(ctx context.Context, runID, owner string, ttl time.Duration, claim bool) (store.Lease, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Lease{}, err
	}
	if err := backend.CheckLease(owner, ttl); err != nil {
		return store.Lease{}, err
	}
	now := s.now()
	l := store.Lease{Owner: owner, Expires: now.Add(ttl)}
	expires, err := storeTime(l.Expires)
	if err != nil {
		return store.Lease{}, err
	}
	l.Expires = l.Expires.UTC()
	// The owner is ?4 and now ?5.
	cond := `(lease_owner = ?4 AND lease_expires > ?5)`
	if claim {
		cond = `(lease_owner = ?4 OR lease_owner = '' OR lease_expires <= ?5)`
	}
	err = s.write(ctx, func(tx *txn) error {
		res, err := tx.ExecContext(ctx, `UPDATE runs SET lease_owner = ?4, lease_expires = ?3
			WHERE tenant = ?1 AND id = ?2 AND status NOT IN `+final+` AND `+cond,
			tenant, runID, expires, owner, argTime(now))
		if err != nil {
			return fmt.Errorf("sqlite: lease: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n == 1 {
			return err
		}
		r, err := getRun(ctx, tx, tenant, runID)
		switch {
		case err != nil:
			return err
		case r.Status.Final():
			return fmt.Errorf("%w: run %q", store.ErrFinal, runID)
		case backend.Held(r.Lease, now) && r.Lease.Owner != owner:
			return fmt.Errorf("%w: run %q", store.ErrLeased, runID)
		case !claim:
			return fmt.Errorf("%w: run %q", store.ErrLeaseLost, runID)
		}
		return fmt.Errorf("sqlite: lease: run %q: the claim changed no row", runID)
	})
	if err != nil {
		return store.Lease{}, err
	}
	return l, nil
}

// Release implements [store.Leaser].
func (s *Store) Release(ctx context.Context, runID, owner string) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	if err := backend.CheckOwner(owner); err != nil {
		return err
	}
	now := s.now()
	return s.write(ctx, func(tx *txn) error {
		res, err := tx.ExecContext(ctx, `UPDATE runs SET lease_owner = '', lease_expires = NULL
			WHERE tenant = ? AND id = ? AND lease_owner = ?`, tenant, runID, owner)
		if err != nil {
			return fmt.Errorf("sqlite: release: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n == 1 {
			return err
		}
		r, err := getRun(ctx, tx, tenant, runID)
		switch {
		case err != nil:
			return err
		case backend.Held(r.Lease, now):
			return fmt.Errorf("%w: run %q", store.ErrLeased, runID)
		}
		return nil // nobody holds it
	})
}

// Admin returns the cross-tenant view of s, for the runner's recovery loop
// only. It reads the same file as s.
//
// It is a function, not a method, on purpose: *Store has no method that leads
// to other tenants' runs, so code holding s as a [store.RunStore] cannot reach
// them by a type assertion. It would have to assert to *Store, which means
// importing this package; code that serves requests has no reason to.
func Admin(s *Store) store.Admin { return admin{s} }

// admin is the [store.Admin] view of a Store.
type admin struct{ s *Store }

// ListResumableAll implements [store.Admin]. It is ListResumable's query with
// no tenant filter, walking the runs_resumable_all index in output order.
func (a admin) ListResumableAll(ctx context.Context, now time.Time, limit int) ([]store.TenantRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n, err := backend.Limit(store.Page{Limit: limit})
	if err != nil {
		return nil, err
	}
	rows, err := a.s.r.QueryContext(ctx, `SELECT tenant, `+runColumns+` FROM runs
		WHERE status IN ('running', 'failed') AND `+resumable+`
		ORDER BY created_at, tenant, id LIMIT CAST(?2 AS INTEGER)`, argTime(now), n)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list resumable runs of every tenant: %w", err)
	}
	var tenants []string
	runs, err := scanRuns(rows, &tenants)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list resumable runs of every tenant: %w", err)
	}
	out := make([]store.TenantRun, len(runs))
	for i, r := range runs {
		t, err := tenancy.NewTenant(tenants[i])
		if err != nil {
			return nil, fmt.Errorf("sqlite: run %q has a bad tenant: %w", r.ID, err)
		}
		out[i] = store.TenantRun{Tenant: t, Run: r}
	}
	return out, nil
}

// ClaimNext implements [store.Admin]. In one write transaction (which holds
// the database's write lock, so claimers take turns) it reads the first
// queued, due run of the lanes from the runs_queue index and leases it.
func (a admin) ClaimNext(ctx context.Context, lanes []string, owner string, ttl time.Duration) (store.TenantRun, error) {
	if err := ctx.Err(); err != nil {
		return store.TenantRun{}, err
	}
	if err := backend.CheckClaimNext(lanes, owner, ttl); err != nil {
		return store.TenantRun{}, err
	}
	now := a.s.now()
	expires, err := storeTime(now.Add(ttl))
	if err != nil {
		return store.TenantRun{}, err
	}
	// now is ?1; the lanes follow.
	args := []any{argTime(now)}
	marks := make([]string, len(lanes))
	for i, l := range lanes {
		args = append(args, l)
		marks[i] = "?" + strconv.Itoa(i+2)
	}
	var out store.TenantRun
	err = a.s.write(ctx, func(tx *txn) error {
		var tenant, id string
		err := tx.QueryRowContext(ctx, `SELECT tenant, id FROM runs
			WHERE lane IN (`+strings.Join(marks, ", ")+`) AND `+queued+` AND `+due+` <= ?1
			ORDER BY priority DESC, `+due+`, tenant, id LIMIT 1`, args...).Scan(&tenant, &id)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: no run of lanes %q is due", store.ErrNotFound, lanes)
		}
		if err != nil {
			return fmt.Errorf("sqlite: claim next run: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET lease_owner = ?, lease_expires = ? WHERE tenant = ? AND id = ?`,
			owner, expires, tenant, id); err != nil {
			return fmt.Errorf("sqlite: claim next run: %w", err)
		}
		r, err := getRun(ctx, tx, tenant, id)
		if err != nil {
			return err
		}
		t, err := tenancy.NewTenant(tenant)
		if err != nil {
			return fmt.Errorf("sqlite: run %q has a bad tenant: %w", id, err)
		}
		out = store.TenantRun{Tenant: t, Run: r}
		return nil
	})
	return out, err
}

// scanRun reads one row of runColumns, after the columns in pre.
func scanRun(row scanner, pre ...any) (store.Run, error) {
	var (
		r                       store.Run
		status, pending         string
		next, created, finished sql.NullInt64
		expires, start          sql.NullInt64
		in, out, cacheR, cacheW int
		retryable               bool
	)
	err := row.Scan(append(pre, &r.ID, &r.ConversationID, &status,
		&in, &out, &cacheR, &cacheW,
		&r.Error, &retryable, &next, &r.Attempts, &pending,
		&created, &finished, &r.Lease.Owner, &expires, &r.Interactive,
		&r.Lane, &r.Priority, &start, &r.Pool, &r.AgentVersion, &r.Model, &r.User)...)
	if err != nil {
		return store.Run{}, err
	}
	r.Status = store.Status(status)
	r.Usage.InputTokens, r.Usage.OutputTokens = in, out
	r.Usage.CacheReadTokens, r.Usage.CacheWriteTokens = cacheR, cacheW
	r.Retryable = retryable
	r.NextAttemptAt, r.CreatedAt, r.FinishedAt = loadTime(next), loadTime(created), loadTime(finished)
	r.Lease.Expires, r.StartAt = loadTime(expires), loadTime(start)
	if pending != "" {
		if err := json.Unmarshal([]byte(pending), &r.Pending); err != nil {
			return store.Run{}, fmt.Errorf("pending of run %q: %w", r.ID, err)
		}
	}
	return r, nil
}

// scanRuns reads rows of runColumns; with tenants, each row starts with its
// tenant, appended there.
func scanRuns(rows *sql.Rows, tenants *[]string) ([]store.Run, error) {
	defer rows.Close()
	var runs []store.Run
	for rows.Next() {
		var pre []any
		var tenant string
		if tenants != nil {
			pre = append(pre, &tenant)
		}
		r, err := scanRun(rows, pre...)
		if err != nil {
			return nil, err
		}
		if tenants != nil {
			*tenants = append(*tenants, tenant)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}
