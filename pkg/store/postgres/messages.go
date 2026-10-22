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

// MessageAdmin returns the [store.MessageAdmin] view of s: the messages due
// across every tenant, for the process that delivers them. Like [Admin], it
// connects as the store's admin role.
func MessageAdmin(s *Store) store.MessageAdmin { return admin{s} }

const (
	messageColumns = `id, thread_id, hop, from_addr, to_addr, from_conversation, to_conversation, kind, text, data,
		in_reply_to, state, run_id, attempts, created_at, delivered_at, processed_at, next_attempt_at, lease_owner, lease_expires`
	threadColumns = `id, status, stop_code, stop_reason, hops, tokens, created_at, updated_at`
)

// SendMessage implements [store.MessageLog]: one transaction that creates
// the thread if it has to and locks its row, so that the senders of a
// thread take hops in turn.
func (s *Store) SendMessage(ctx context.Context, m store.Message) (store.Message, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Message{}, err
	}
	if m, err = backend.CheckMessage(m); err != nil {
		return store.Message{}, err
	}
	var out store.Message
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			stored, err := getMessage(ctx, tx, tenant, m.ID)
			switch {
			case err == nil:
				out, err = backend.SentMessage(m, stored)
				return err
			case !errors.Is(err, store.ErrNotFound):
				return err
			}
			created, err := storeTime(s.now())
			if err != nil {
				return fmt.Errorf("postgres: send message: the clock reads %w", err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO threads (tenant, id, created_at, updated_at) VALUES ($1, $2, $3, $3)
				ON CONFLICT (tenant, id) DO NOTHING`, tenant, m.ThreadID, created); err != nil {
				return fail("send message", err)
			}
			th, err := scanThread(tx.QueryRow(ctx, `SELECT `+threadColumns+` FROM threads WHERE tenant = $1 AND id = $2 FOR UPDATE`,
				tenant, m.ThreadID))
			if err != nil {
				return fail("send message", err)
			}
			if err := backend.SendToThreadError(th, m.Kind); err != nil {
				return err
			}
			if m.InReplyTo != "" {
				r, err := getMessage(ctx, tx, tenant, m.InReplyTo)
				if errors.Is(err, store.ErrNotFound) || (err == nil && r.ThreadID != m.ThreadID) {
					return fmt.Errorf("%w: message %q answers %q, which is not in thread %q", store.ErrInvalid, m.ID, m.InReplyTo, m.ThreadID)
				}
				if err != nil {
					return err
				}
			}
			hop := th.Hops + 1
			if _, err := tx.Exec(ctx, `UPDATE threads SET hops = $1, updated_at = $2 WHERE tenant = $3 AND id = $4`,
				hop, created, tenant, m.ThreadID); err != nil {
				return fail("send message", err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO messages (tenant, id, thread_id, hop, from_addr, to_addr,
				from_conversation, to_conversation, kind, text, data, in_reply_to, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
				tenant, m.ID, m.ThreadID, hop, m.From, m.To, m.FromConversation, m.ToConversation,
				string(m.Kind), m.Text, string(m.Data), m.InReplyTo, created); err != nil {
				if code, _ := sqlState(err); code == uniqueViolation {
					// The same ID sent at once to another thread: run again,
					// and find the stored one.
					return errRetry
				}
				return fail("send message", err)
			}
			out, err = getMessage(ctx, tx, tenant, m.ID)
			return err
		})
	})
	return out, err
}

// GetMessage implements [store.MessageLog].
func (s *Store) GetMessage(ctx context.Context, id string) (store.Message, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Message{}, err
	}
	return getMessage(ctx, s.pool, tenant, id)
}

func getMessage(ctx context.Context, q querier, tenant, id string) (store.Message, error) {
	if !backend.TextOK(id) {
		return store.Message{}, fmt.Errorf("%w: message %q", store.ErrNotFound, id)
	}
	m, err := scanMessage(q.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE tenant = $1 AND id = $2`, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Message{}, fmt.Errorf("%w: message %q", store.ErrNotFound, id)
	}
	if err != nil {
		return store.Message{}, fail("get message", err)
	}
	return m, nil
}

// ThreadMessages implements [store.MessageLog].
func (s *Store) ThreadMessages(ctx context.Context, threadID string, after, limit int) ([]store.Message, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	if limit, err = backend.CheckThreadMessages(after, limit); err != nil {
		return nil, err
	}
	if !backend.TextOK(threadID) {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+messageColumns+` FROM messages
		WHERE tenant = $1 AND thread_id = $2 AND hop > $3 ORDER BY hop LIMIT $4`, tenant, threadID, after, limit)
	if err != nil {
		return nil, fail("thread messages", err)
	}
	defer rows.Close()
	var out []store.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fail("thread messages", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fail("thread messages", err)
	}
	return out, nil
}

// GetThread implements [store.MessageLog].
func (s *Store) GetThread(ctx context.Context, id string) (store.Thread, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Thread{}, err
	}
	return getThread(ctx, s.pool, tenant, id)
}

func getThread(ctx context.Context, q querier, tenant, id string) (store.Thread, error) {
	if !backend.TextOK(id) {
		return store.Thread{}, fmt.Errorf("%w: thread %q", store.ErrNotFound, id)
	}
	th, err := scanThread(q.QueryRow(ctx, `SELECT `+threadColumns+` FROM threads WHERE tenant = $1 AND id = $2`, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Thread{}, fmt.Errorf("%w: thread %q", store.ErrNotFound, id)
	}
	if err != nil {
		return store.Thread{}, fail("get thread", err)
	}
	return th, nil
}

// ListThreads implements [store.MessageLog]: a keyset query on the
// threads_tenant index (migration 0016).
func (s *Store) ListThreads(ctx context.Context, p store.Page) (store.ThreadPage, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.ThreadPage{}, err
	}
	limit, start, err := backend.ParseThreadPage(p)
	if err != nil {
		return store.ThreadPage{}, err
	}
	conds, args := []string{"tenant = $1"}, []any{tenant}
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
	rows, err := s.pool.Query(ctx, `SELECT `+threadColumns+` FROM threads WHERE `+
		strings.Join(conds, " AND ")+` ORDER BY `+order+` LIMIT `+next(limit+1), args...)
	if err != nil {
		return store.ThreadPage{}, fail("list threads", err)
	}
	defer rows.Close()
	var ts []store.Thread
	for rows.Next() {
		th, err := scanThread(rows)
		if err != nil {
			return store.ThreadPage{}, fail("list threads", err)
		}
		ts = append(ts, th)
	}
	if err := rows.Err(); err != nil {
		return store.ThreadPage{}, fail("list threads", err)
	}
	var page store.ThreadPage
	if len(ts) > limit {
		ts = ts[:limit]
		last := ts[limit-1]
		page.Next = backend.ThreadCursor(p.Order, backend.RunKey{Created: last.CreatedAt, ID: last.ID})
	}
	page.Threads = ts
	return page, nil
}

// StopThread implements [store.MessageLog]: one UPDATE of a thread that is
// open, then the thread is read.
func (s *Store) StopThread(ctx context.Context, id, code, reason string) (store.Thread, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Thread{}, err
	}
	if err := backend.CheckStop(code, reason); err != nil {
		return store.Thread{}, err
	}
	var out store.Thread
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			now, err := storeTime(s.now())
			if err != nil {
				return err
			}
			if backend.TextOK(id) {
				if _, err := tx.Exec(ctx, `UPDATE threads SET status = 'stopped', stop_code = $1, stop_reason = $2, updated_at = $3
					WHERE tenant = $4 AND id = $5 AND status <> 'stopped'`, code, reason, now, tenant, id); err != nil {
					return fail("stop thread", err)
				}
			}
			out, err = getThread(ctx, tx, tenant, id)
			return err
		})
	})
	return out, err
}

// lockMessage reads the message, its row locked.
func lockMessage(ctx context.Context, tx pgx.Tx, tenant, id string) (store.Message, error) {
	if !backend.TextOK(id) {
		return store.Message{}, fmt.Errorf("%w: message %q", store.ErrNotFound, id)
	}
	m, err := scanMessage(tx.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE tenant = $1 AND id = $2 FOR UPDATE`, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Message{}, fmt.Errorf("%w: message %q", store.ErrNotFound, id)
	}
	if err != nil {
		return store.Message{}, fail("lock message", err)
	}
	return m, nil
}

// ClaimMessage implements [store.MessageLog]: one transaction that locks the
// message's row.
func (s *Store) ClaimMessage(ctx context.Context, id, owner string, ttl time.Duration) (store.Message, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Message{}, err
	}
	if err := backend.CheckLease(owner, ttl); err != nil {
		return store.Message{}, err
	}
	var out store.Message
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			m, err := lockMessage(ctx, tx, tenant, id)
			if err != nil {
				return err
			}
			now := s.now()
			if err := backend.ClaimMessageError(m, owner, now); err != nil {
				return err
			}
			out, err = claimMessage(ctx, tx, tenant, m, owner, now, ttl)
			return err
		})
	})
	return out, err
}

// claimMessage writes m claimed by owner.
func claimMessage(ctx context.Context, q querier, tenant string, m store.Message, owner string, now time.Time, ttl time.Duration) (store.Message, error) {
	m = backend.Claimed(m, owner, now, now.Add(ttl))
	delivered, err := storeTime(m.DeliveredAt)
	if err != nil {
		return store.Message{}, err
	}
	expires, err := storeTime(m.Lease.Expires)
	if err != nil {
		return store.Message{}, err
	}
	if _, err := q.Exec(ctx, `UPDATE messages SET lease_owner = $1, lease_expires = $2, attempts = $3, delivered_at = $4
		WHERE tenant = $5 AND id = $6`, owner, expires, m.Attempts, delivered, tenant, m.ID); err != nil {
		return store.Message{}, fail("claim message", err)
	}
	return m, nil
}

// UpdateMessage implements [store.MessageLog]: one transaction that locks the
// message's row.
func (s *Store) UpdateMessage(ctx context.Context, id, owner string, u store.MessageUpdate) (store.Message, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Message{}, err
	}
	if err := backend.CheckOwner(owner); err != nil {
		return store.Message{}, err
	}
	if err := backend.CheckMessageUpdate(u); err != nil {
		return store.Message{}, err
	}
	var out store.Message
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			m, err := lockMessage(ctx, tx, tenant, id)
			if err != nil {
				return err
			}
			now := s.now()
			if err := backend.UpdateMessageError(m, owner, u, now); err != nil {
				return err
			}
			m = backend.Updated(m, u, now)
			processed, err := storeTime(m.ProcessedAt)
			if err != nil {
				return err
			}
			next, err := storeTime(m.NextAttemptAt)
			if err != nil {
				return err
			}
			stamp, err := storeTime(now)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE messages SET state = $1, run_id = $2, processed_at = $3, next_attempt_at = $4,
				lease_owner = '', lease_expires = NULL WHERE tenant = $5 AND id = $6`,
				string(m.State), m.RunID, processed, next, tenant, id); err != nil {
				return fail("update message", err)
			}
			if u.Tokens > 0 {
				if _, err := tx.Exec(ctx, `UPDATE threads SET tokens = tokens + $1, updated_at = $2 WHERE tenant = $3 AND id = $4`,
					u.Tokens, stamp, tenant, m.ThreadID); err != nil {
					return fail("update message", err)
				}
			}
			out = m
			return nil
		})
	})
	return out, err
}

// nextMessage is ClaimDueMessage's query: the first due message in claim
// order whose row no other claimer has locked (FOR UPDATE SKIP LOCKED). $1
// is now.
const nextMessage = `SELECT m.tenant, m.id FROM messages m
	WHERE m.state <> 'done' AND (m.lease_owner = '' OR m.lease_expires <= $1)
		AND (m.next_attempt_at IS NULL OR m.next_attempt_at <= $1)
		AND (m.state <> 'queued' OR NOT EXISTS (SELECT 1 FROM messages e WHERE e.tenant = m.tenant
			AND e.thread_id = m.thread_id AND e.to_conversation = m.to_conversation AND e.state = 'queued' AND e.hop < m.hop))
	ORDER BY m.next_attempt_at NULLS FIRST, m.created_at, m.tenant, m.id
	LIMIT 1 FOR UPDATE OF m SKIP LOCKED`

// ClaimDueMessage implements [store.MessageAdmin]: one transaction that
// locks the first due message and leases it.
func (a admin) ClaimDueMessage(ctx context.Context, owner string, ttl time.Duration) (store.TenantMessage, error) {
	if err := ctx.Err(); err != nil {
		return store.TenantMessage{}, err
	}
	if err := backend.CheckLease(owner, ttl); err != nil {
		return store.TenantMessage{}, err
	}
	ctx = unscoped(ctx)
	if err := a.checkRole(ctx); err != nil {
		return store.TenantMessage{}, err
	}
	var out store.TenantMessage
	err := retry(ctx, func() error {
		return pgx.BeginFunc(ctx, a.s.admin, func(tx pgx.Tx) error {
			now := a.s.now()
			if _, err := storeTime(now.Add(ttl)); err != nil {
				return err
			}
			var tenant, id string
			err := tx.QueryRow(ctx, nextMessage, argTime(now)).Scan(&tenant, &id)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: no message is due", store.ErrNotFound)
			}
			if err != nil {
				return fail("claim message", err)
			}
			m, err := getMessage(ctx, tx, tenant, id)
			if err != nil {
				return err
			}
			if m, err = claimMessage(ctx, tx, tenant, m, owner, now, ttl); err != nil {
				return err
			}
			t, err := tenancy.NewTenant(tenant)
			if err != nil {
				return fmt.Errorf("postgres: message %q has a bad tenant: %w", id, err)
			}
			out = store.TenantMessage{Tenant: t, Message: m}
			return nil
		})
	})
	return out, err
}

// scanMessage reads one row of messageColumns.
func scanMessage(row pgx.Row) (store.Message, error) {
	var (
		m                                         store.Message
		kind, state, data                         string
		created, delivered, processed, next, exps *int64
	)
	if err := row.Scan(&m.ID, &m.ThreadID, &m.Hop, &m.From, &m.To, &m.FromConversation, &m.ToConversation, &kind, &m.Text, &data,
		&m.InReplyTo, &state, &m.RunID, &m.Attempts, &created, &delivered, &processed, &next, &m.Lease.Owner, &exps); err != nil {
		return store.Message{}, err
	}
	m.Kind, m.State = store.MessageKind(kind), store.MessageState(state)
	if data != "" {
		m.Data = []byte(data)
	}
	m.CreatedAt, m.DeliveredAt, m.ProcessedAt = loadTime(created), loadTime(delivered), loadTime(processed)
	m.NextAttemptAt, m.Lease.Expires = loadTime(next), loadTime(exps)
	return m, nil
}

// scanThread reads one row of threadColumns.
func scanThread(row pgx.Row) (store.Thread, error) {
	var (
		th               store.Thread
		status           string
		created, updated *int64
	)
	if err := row.Scan(&th.ID, &status, &th.StopCode, &th.StopReason, &th.Hops, &th.Tokens, &created, &updated); err != nil {
		return store.Thread{}, err
	}
	th.Status = store.ThreadStatus(status)
	th.CreatedAt, th.UpdatedAt = loadTime(created), loadTime(updated)
	return th, nil
}
