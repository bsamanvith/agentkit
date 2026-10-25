package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

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

// resumable is backend.Resumable in SQL, with now as $1.
const resumable = `(NOT interactive AND start_at <= $1 AND ((status = 'running' AND (lease_owner = '' OR lease_expires <= $1))
	OR (status = 'failed' AND retryable AND (next_attempt_at IS NULL OR next_attempt_at <= $1))))`

// queued is backend.Queued in SQL, and due is store.Run.Due: the predicate
// and the expression of the runs_queue index (migration 0010), which a query
// repeats so that the planner uses it.
const (
	queued = `NOT interactive AND (status = 'running' OR (status = 'failed' AND retryable))`
	due    = `GREATEST(start_at, lease_expires, next_attempt_at)`
)

// Create implements [store.RunStore].
//
// The INSERT comes first; the primary key (runs_pkey) and the partial unique
// index on unfinished runs (runs_unfinished) decide, and when it fails the
// stored rows say why: the same run (a retry), the id in another
// conversation, or another unfinished run.
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
	var run store.Run
	err = retry(ctx, func() error {
		now := s.now()
		at, err := storeTime(now)
		if err != nil {
			return fmt.Errorf("postgres: create run: the clock reads %w", err)
		}
		c, err := backend.CheckCreate(o, now)
		if err != nil {
			return err
		}
		expires, _ := storeTime(c.Lease.Expires) // checked by CheckCreate
		_, insertErr := s.pool.Exec(ctx, `INSERT INTO runs (tenant, id, conversation, status, created_at, interactive,
			lane, priority, start_at, pool, agent_version, model, user_id, lease_owner, lease_expires)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`, tenant, id, conversationID, string(store.StatusRunning), at, o.Interactive,
			o.Lane, o.Priority, c.Start.UnixNano(), o.Pool, o.AgentVersion, o.Model, o.User, c.Lease.Owner, expires)
		if insertErr == nil {
			run = store.Run{
				ID: id, ConversationID: conversationID, Status: store.StatusRunning, CreatedAt: now,
				Interactive: o.Interactive, Lane: o.Lane, Priority: o.Priority, StartAt: c.Start, Pool: o.Pool, AgentVersion: o.AgentVersion, Model: o.Model, User: o.User, Lease: c.Lease,
			}
			return nil
		}
		switch code, constraint := sqlState(insertErr); {
		case code != uniqueViolation:
			return fail("create run", insertErr)
		case constraint != "runs_pkey" && constraint != "runs_unfinished":
			return fail("create run", insertErr)
		}
		// Either index may be the one reported when both are hit, so the
		// run itself is looked at first.
		r, err := getRun(ctx, s.pool, tenant, id)
		switch {
		case err == nil && r.ConversationID != conversationID:
			return fmt.Errorf("%w: run %q belongs to conversation %q", store.ErrConflict, id, r.ConversationID)
		case err == nil:
			run = r
			return nil
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		u, err := unfinished(ctx, s.pool, tenant, conversationID)
		switch {
		case err == nil:
			return fmt.Errorf("%w: conversation %q has run %q", store.ErrUnfinished, conversationID, u.ID)
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		return errRetry // the run in the way finished in between
	})
	return run, err
}

// Get implements [store.RunStore].
func (s *Store) Get(ctx context.Context, id string) (store.Run, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Run{}, err
	}
	return getRun(ctx, s.pool, tenant, id)
}

func getRun(ctx context.Context, q querier, tenant, id string) (store.Run, error) {
	if !backend.TextOK(id) {
		return store.Run{}, fmt.Errorf("%w: run %q", store.ErrNotFound, id)
	}
	r, err := scanRun(q.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE tenant = $1 AND id = $2`, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Run{}, fmt.Errorf("%w: run %q", store.ErrNotFound, id)
	}
	if err != nil {
		return store.Run{}, fail(fmt.Sprintf("run %q", id), err)
	}
	return r, nil
}

func unfinished(ctx context.Context, q querier, tenant, conversationID string) (store.Run, error) {
	r, err := scanRun(q.QueryRow(ctx, `SELECT `+runColumns+` FROM runs
		WHERE tenant = $1 AND conversation = $2 AND status NOT IN `+final, tenant, conversationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Run{}, fmt.Errorf("%w: conversation %q has no unfinished run", store.ErrNotFound, conversationID)
	}
	if err != nil {
		return store.Run{}, fail(fmt.Sprintf("unfinished run of %q", conversationID), err)
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
	return unfinished(ctx, s.pool, tenant, conversationID)
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
	return s.page(ctx, p, "tenant = $1 AND conversation = $2", tenant, conversationID)
}

// ListSuspended implements [store.RunStore].
func (s *Store) ListSuspended(ctx context.Context, p store.Page) (store.RunPage, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.RunPage{}, err
	}
	return s.page(ctx, p, "tenant = $1 AND status = 'suspended'", tenant)
}

// page returns one page of the runs that match where (whose parameters are
// args), a keyset query on (created_at, id).
func (s *Store) page(ctx context.Context, p store.Page, where string, args ...any) (store.RunPage, error) {
	limit, start, err := backend.ParseRunPage(p)
	if err != nil {
		return store.RunPage{}, err
	}
	conds := []string{where}
	// next adds a parameter and returns its placeholder.
	next := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	asc := p.Order == store.Ascending
	switch {
	case start.HasAfter && asc:
		conds = append(conds, "(created_at, id) > ("+next(argTime(start.After.Created))+", "+next(start.After.ID)+")")
	case start.HasAfter:
		conds = append(conds, "(created_at, id) < ("+next(argTime(start.After.Created))+", "+next(start.After.ID)+")")
	case !start.FromTime.IsZero() && asc:
		conds = append(conds, "created_at >= "+next(argTime(start.FromTime)))
	case !start.FromTime.IsZero():
		conds = append(conds, "created_at <= "+next(argTime(start.FromTime)))
	}
	order := "created_at ASC, id ASC"
	if !asc {
		order = "created_at DESC, id DESC"
	}
	rows, err := s.pool.Query(ctx, `SELECT `+runColumns+` FROM runs WHERE `+
		strings.Join(conds, " AND ")+` ORDER BY `+order+` LIMIT `+next(limit+1), args...)
	if err != nil {
		return store.RunPage{}, fail("list runs", err)
	}
	runs, err := scanRuns(rows, nil)
	if err != nil {
		return store.RunPage{}, fail("list runs", err)
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
	rows, err := s.pool.Query(ctx, `SELECT `+runColumns+` FROM runs
		WHERE tenant = $2 AND status IN ('running', 'failed') AND `+resumable+`
		ORDER BY created_at, id LIMIT $3`, argTime(now), tenant, n)
	if err != nil {
		return nil, fail("list resumable runs", err)
	}
	runs, err := scanRuns(rows, nil)
	if err != nil {
		return nil, fail("list resumable runs", err)
	}
	return runs, nil
}

// Transition implements [store.RunStore].
//
// One conditional UPDATE (WHERE status = from) moves the run and returns it;
// only when it changes no row is the run read, to tell why.
func (s *Store) Transition(ctx context.Context, id string, from, to store.Status, d store.Details) (store.Run, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Run{}, err
	}
	pending, invalid := backend.CheckTransition(from, to, d)
	var next *int64
	if invalid == nil {
		next, invalid = storeTime(d.NextAttemptAt)
	}
	var encPending string
	if len(pending) > 0 {
		b, err := json.Marshal(pending)
		if err != nil {
			return store.Run{}, fmt.Errorf("postgres: transition: %w", err)
		}
		encPending = string(b)
	}
	attempt := 0
	if to == store.StatusFailed {
		attempt = 1
	}

	var run store.Run
	err = retry(ctx, func() error {
		var finished *int64
		if to.Final() {
			var err error
			if finished, err = storeTime(s.now()); err != nil {
				return fmt.Errorf("postgres: transition: the clock reads %w", err)
			}
		}
		if invalid == nil && backend.TextOK(id) {
			r, err := scanRun(s.pool.QueryRow(ctx, `UPDATE runs SET status = $1,
				input_tokens = input_tokens + $2, output_tokens = output_tokens + $3,
				cache_read_tokens = cache_read_tokens + $4, cache_write_tokens = cache_write_tokens + $5,
				error = $6, retryable = $7, next_attempt_at = $8, pending = $9,
				attempts = attempts + $10, finished_at = $11
				WHERE tenant = $12 AND id = $13 AND status = $14
				RETURNING `+runColumns,
				string(to),
				d.Usage.InputTokens, d.Usage.OutputTokens, d.Usage.CacheReadTokens, d.Usage.CacheWriteTokens,
				d.Error, d.Retryable, next, encPending,
				attempt, finished,
				tenant, id, string(from)))
			if err == nil {
				run = r
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return fail("transition", err)
			}
		}
		r, err := getRun(ctx, s.pool, tenant, id)
		switch {
		case err != nil:
			return err
		case r.Status.Final():
			return fmt.Errorf("%w: run %q is %s", store.ErrFinal, id, r.Status)
		case invalid != nil:
			return invalid
		case r.Status == from:
			return errRetry // it moved away and back in between
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
	// The owner is $4 and now $5.
	cond := `(lease_owner = $4 AND lease_expires > $5)`
	if claim {
		cond = `(lease_owner = $4 OR lease_owner = '' OR lease_expires <= $5)`
	}
	var l store.Lease
	err = retry(ctx, func() error {
		now := s.now()
		l = store.Lease{Owner: owner, Expires: now.Add(ttl)}
		expires, err := storeTime(l.Expires)
		if err != nil {
			return err
		}
		l.Expires = l.Expires.UTC()
		if backend.TextOK(runID) {
			tag, err := s.pool.Exec(ctx, `UPDATE runs SET lease_owner = $4, lease_expires = $3
				WHERE tenant = $1 AND id = $2 AND status NOT IN `+final+` AND `+cond,
				tenant, runID, expires, owner, argTime(now))
			if err != nil {
				return fail("lease", err)
			}
			if tag.RowsAffected() == 1 {
				return nil
			}
		}
		r, err := getRun(ctx, s.pool, tenant, runID)
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
		return errRetry // the lease changed hands in between
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
	return retry(ctx, func() error {
		now := s.now()
		if backend.TextOK(runID) {
			tag, err := s.pool.Exec(ctx, `UPDATE runs SET lease_owner = '', lease_expires = NULL
				WHERE tenant = $1 AND id = $2 AND lease_owner = $3`, tenant, runID, owner)
			if err != nil {
				return fail("release", err)
			}
			if tag.RowsAffected() == 1 {
				return nil
			}
		}
		r, err := getRun(ctx, s.pool, tenant, runID)
		switch {
		case err != nil:
			return err
		case backend.Held(r.Lease, now) && r.Lease.Owner != owner:
			return fmt.Errorf("%w: run %q", store.ErrLeased, runID)
		case r.Lease.Owner == owner:
			return errRetry // owner claimed it again in between
		}
		return nil // nobody holds it
	})
}

// Admin returns the cross-tenant view of s, for the runner's recovery loop
// only. It reads the same database as s, on the admin URL's connections
// ([WithAdminURL]) when there is one. Its role must bypass row-level
// security; when it does not, ListResumableAll fails with [ErrAdminRole]
// rather than silently finding nothing.
//
// It is a function, not a method, on purpose: *Store has no method that leads
// to other tenants' runs, so code holding s as a [store.RunStore] cannot reach
// them by a type assertion. It would have to assert to *Store, which means
// importing this package; code that serves requests has no reason to.
func Admin(s *Store) store.Admin { return admin{s} }

// ErrAdminRole is returned by [Admin] when its role cannot read across
// tenants under row-level security.
var ErrAdminRole = errors.New("postgres: store.Admin's role must bypass row-level security (BYPASSRLS or superuser); give it its own URL (AGENTKIT_STORE_ADMIN)")

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
	ctx = unscoped(ctx)
	if err := a.checkRole(ctx); err != nil {
		return nil, err
	}
	rows, err := a.s.admin.Query(ctx, `SELECT tenant, `+runColumns+` FROM runs
		WHERE status IN ('running', 'failed') AND `+resumable+`
		ORDER BY created_at, tenant, id LIMIT $2`, argTime(now), n)
	if err != nil {
		return nil, fail("list resumable runs of every tenant", err)
	}
	var tenants []string
	runs, err := scanRuns(rows, &tenants)
	if err != nil {
		return nil, fail("list resumable runs of every tenant", err)
	}
	out := make([]store.TenantRun, len(runs))
	for i, r := range runs {
		t, err := tenancy.NewTenant(tenants[i])
		if err != nil {
			return nil, fmt.Errorf("postgres: run %q has a bad tenant: %w", r.ID, err)
		}
		out[i] = store.TenantRun{Tenant: t, Run: r}
	}
	return out, nil
}

// checkRole fails with [ErrAdminRole] unless Admin's role bypasses row-level
// security. A role found to bypass it is not asked again by this Store.
func (a admin) checkRole(ctx context.Context) error {
	if a.s.bypass.Load() {
		return nil
	}
	var bypass bool
	if err := a.s.admin.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&bypass); err != nil {
		return fail("check the admin role", err)
	}
	if !bypass {
		return ErrAdminRole
	}
	a.s.bypass.Store(true)
	return nil
}

// claimNext is ClaimNext's statement. For each lane ($1), the LATERAL
// subquery walks the runs_queue index in claim order to the first due run
// whose row no other claimer has locked (FOR UPDATE SKIP LOCKED, so claimers
// never wait on each other's choice); of those the best is leased to $3
// until $4. $2 is now. The subquery's columns are renamed so that RETURNING
// names the runs table's alone.
const claimNext = `WITH next AS (
	SELECT c.t, c.i FROM unnest($1::text[]) AS l(lane)
	CROSS JOIN LATERAL (
		SELECT tenant AS t, id AS i, priority AS p, ` + due + ` AS d FROM runs
		WHERE runs.lane = l.lane AND ` + queued + ` AND ` + due + ` <= $2
		ORDER BY priority DESC, ` + due + `, tenant, id
		LIMIT 1 FOR UPDATE SKIP LOCKED
	) c
	ORDER BY c.p DESC, c.d, c.t, c.i
	LIMIT 1
)
UPDATE runs SET lease_owner = $3, lease_expires = $4
FROM next WHERE runs.tenant = next.t AND runs.id = next.i
RETURNING tenant, ` + runColumns

// ClaimNext implements [store.Admin], in one statement (claimNext).
func (a admin) ClaimNext(ctx context.Context, lanes []string, owner string, ttl time.Duration) (store.TenantRun, error) {
	if err := ctx.Err(); err != nil {
		return store.TenantRun{}, err
	}
	if err := backend.CheckClaimNext(lanes, owner, ttl); err != nil {
		return store.TenantRun{}, err
	}
	ctx = unscoped(ctx)
	if err := a.checkRole(ctx); err != nil {
		return store.TenantRun{}, err
	}
	lanes = slices.Compact(slices.Sorted(slices.Values(lanes)))
	var out store.TenantRun
	err := retry(ctx, func() error {
		now := a.s.now()
		expires, err := storeTime(now.Add(ttl))
		if err != nil {
			return err
		}
		var tenant string
		r, err := scanRun(a.s.admin.QueryRow(ctx, claimNext, lanes, argTime(now), owner, expires), &tenant)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: no run of lanes %q is due", store.ErrNotFound, lanes)
		}
		if err != nil {
			return fail("claim next run", err)
		}
		t, err := tenancy.NewTenant(tenant)
		if err != nil {
			return fmt.Errorf("postgres: run %q has a bad tenant: %w", r.ID, err)
		}
		out = store.TenantRun{Tenant: t, Run: r}
		return nil
	})
	return out, err
}

// scanRun reads one row of runColumns, after the columns in pre.
func scanRun(row pgx.Row, pre ...any) (store.Run, error) {
	var (
		r                       store.Run
		status, pending         string
		next, created, finished *int64
		expires, start          *int64
		priority                int32
	)
	err := row.Scan(append(pre, &r.ID, &r.ConversationID, &status,
		&r.Usage.InputTokens, &r.Usage.OutputTokens, &r.Usage.CacheReadTokens, &r.Usage.CacheWriteTokens,
		&r.Error, &r.Retryable, &next, &r.Attempts, &pending,
		&created, &finished, &r.Lease.Owner, &expires, &r.Interactive,
		&r.Lane, &priority, &start, &r.Pool, &r.AgentVersion, &r.Model, &r.User)...)
	if err != nil {
		return store.Run{}, err
	}
	r.Status = store.Status(status)
	r.Priority = int(priority)
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
func scanRuns(rows pgx.Rows, tenants *[]string) ([]store.Run, error) {
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
