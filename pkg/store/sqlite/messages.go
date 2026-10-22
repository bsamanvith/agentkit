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

// MessageAdmin returns the [store.MessageAdmin] view of s: the messages due
// across every tenant, for the process that delivers them.
func MessageAdmin(s *Store) store.MessageAdmin { return admin{s} }

const (
	messageColumns = `id, thread_id, hop, from_addr, to_addr, from_conversation, to_conversation, kind, text, data,
		in_reply_to, state, run_id, attempts, created_at, delivered_at, processed_at, next_attempt_at, lease_owner, lease_expires`
	threadColumns = `id, status, stop_code, stop_reason, hops, tokens, created_at, updated_at`
)

// SendMessage implements [store.MessageLog]: in one write transaction, the
// thread is read (or created) and the message takes its next hop.
func (s *Store) SendMessage(ctx context.Context, m store.Message) (store.Message, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Message{}, err
	}
	if m, err = backend.CheckMessage(m); err != nil {
		return store.Message{}, err
	}
	now := s.now()
	created, err := storeTime(now)
	if err != nil {
		return store.Message{}, err
	}
	var out store.Message
	err = s.write(ctx, func(tx *txn) error {
		stored, err := getMessage(ctx, tx, tenant, m.ID)
		switch {
		case err == nil:
			out, err = backend.SentMessage(m, stored)
			return err
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		th, err := getThread(ctx, tx, tenant, m.ThreadID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			th = store.Thread{ID: m.ThreadID, Status: store.ThreadOpen}
			if _, err := tx.ExecContext(ctx, `INSERT INTO threads (tenant, id, created_at, updated_at) VALUES (?, ?, ?, ?)`,
				tenant, m.ThreadID, created, created); err != nil {
				return fmt.Errorf("sqlite: create thread: %w", err)
			}
		case err != nil:
			return err
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
		if _, err := tx.ExecContext(ctx, `UPDATE threads SET hops = ?, updated_at = ? WHERE tenant = ? AND id = ?`,
			hop, created, tenant, m.ThreadID); err != nil {
			return fmt.Errorf("sqlite: send message: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages (tenant, id, thread_id, hop, from_addr, to_addr,
			from_conversation, to_conversation, kind, text, data, in_reply_to, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			tenant, m.ID, m.ThreadID, hop, m.From, m.To, m.FromConversation, m.ToConversation,
			string(m.Kind), m.Text, string(m.Data), m.InReplyTo, created); err != nil {
			return fmt.Errorf("sqlite: send message: %w", err)
		}
		out, err = getMessage(ctx, tx, tenant, m.ID)
		return err
	})
	return out, err
}

// GetMessage implements [store.MessageLog].
func (s *Store) GetMessage(ctx context.Context, id string) (store.Message, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Message{}, err
	}
	return getMessage(ctx, s.r, tenant, id)
}

func getMessage(ctx context.Context, q querier, tenant, id string) (store.Message, error) {
	m, err := scanMessage(q.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE tenant = ? AND id = ?`, tenant, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Message{}, fmt.Errorf("%w: message %q", store.ErrNotFound, id)
	}
	if err != nil {
		return store.Message{}, fmt.Errorf("sqlite: get message: %w", err)
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
	rows, err := s.r.QueryContext(ctx, `SELECT `+messageColumns+` FROM messages
		WHERE tenant = ? AND thread_id = ? AND hop > ? ORDER BY hop LIMIT CAST(? AS INTEGER)`, tenant, threadID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: thread messages: %w", err)
	}
	defer rows.Close()
	var out []store.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: thread messages: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: thread messages: %w", err)
	}
	return out, nil
}

// GetThread implements [store.MessageLog].
func (s *Store) GetThread(ctx context.Context, id string) (store.Thread, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Thread{}, err
	}
	return getThread(ctx, s.r, tenant, id)
}

func getThread(ctx context.Context, q querier, tenant, id string) (store.Thread, error) {
	th, err := scanThread(q.QueryRowContext(ctx, `SELECT `+threadColumns+` FROM threads WHERE tenant = ? AND id = ?`, tenant, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Thread{}, fmt.Errorf("%w: thread %q", store.ErrNotFound, id)
	}
	if err != nil {
		return store.Thread{}, fmt.Errorf("sqlite: get thread: %w", err)
	}
	return th, nil
}

// ListThreads implements [store.MessageLog]: a keyset query on the
// threads_tenant index (migration 0015).
func (s *Store) ListThreads(ctx context.Context, p store.Page) (store.ThreadPage, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.ThreadPage{}, err
	}
	limit, start, err := backend.ParseThreadPage(p)
	if err != nil {
		return store.ThreadPage{}, err
	}
	conds, args := []string{"tenant = ?"}, []any{tenant}
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
	rows, err := s.r.QueryContext(ctx, `SELECT `+threadColumns+` FROM threads WHERE `+
		strings.Join(conds, " AND ")+` ORDER BY `+order+` LIMIT CAST(? AS INTEGER)`, append(args, limit+1)...)
	if err != nil {
		return store.ThreadPage{}, fmt.Errorf("sqlite: list threads: %w", err)
	}
	defer rows.Close()
	var ts []store.Thread
	for rows.Next() {
		th, err := scanThread(rows)
		if err != nil {
			return store.ThreadPage{}, fmt.Errorf("sqlite: list threads: %w", err)
		}
		ts = append(ts, th)
	}
	if err := rows.Err(); err != nil {
		return store.ThreadPage{}, fmt.Errorf("sqlite: list threads: %w", err)
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

// StopThread implements [store.MessageLog].
func (s *Store) StopThread(ctx context.Context, id, code, reason string) (store.Thread, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Thread{}, err
	}
	if err := backend.CheckStop(code, reason); err != nil {
		return store.Thread{}, err
	}
	now, err := storeTime(s.now())
	if err != nil {
		return store.Thread{}, err
	}
	var out store.Thread
	err = s.write(ctx, func(tx *txn) error {
		if _, err := tx.ExecContext(ctx, `UPDATE threads SET status = 'stopped', stop_code = ?, stop_reason = ?, updated_at = ?
			WHERE tenant = ? AND id = ? AND status <> 'stopped'`, code, reason, now, tenant, id); err != nil {
			return fmt.Errorf("sqlite: stop thread: %w", err)
		}
		out, err = getThread(ctx, tx, tenant, id)
		return err
	})
	return out, err
}

// ClaimMessage implements [store.MessageLog].
func (s *Store) ClaimMessage(ctx context.Context, id, owner string, ttl time.Duration) (store.Message, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Message{}, err
	}
	if err := backend.CheckLease(owner, ttl); err != nil {
		return store.Message{}, err
	}
	now := s.now()
	if _, err := storeTime(now.Add(ttl)); err != nil {
		return store.Message{}, err
	}
	var out store.Message
	err = s.write(ctx, func(tx *txn) error {
		m, err := getMessage(ctx, tx, tenant, id)
		if err != nil {
			return err
		}
		if err := backend.ClaimMessageError(m, owner, now); err != nil {
			return err
		}
		out, err = claimMessage(ctx, tx, tenant, m, owner, now, ttl)
		return err
	})
	return out, err
}

// claimMessage writes m claimed by owner.
func claimMessage(ctx context.Context, tx *txn, tenant string, m store.Message, owner string, now time.Time, ttl time.Duration) (store.Message, error) {
	m = backend.Claimed(m, owner, now, now.Add(ttl))
	delivered, err := storeTime(m.DeliveredAt)
	if err != nil {
		return store.Message{}, err
	}
	expires, err := storeTime(m.Lease.Expires)
	if err != nil {
		return store.Message{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET lease_owner = ?, lease_expires = ?, attempts = ?, delivered_at = ?
		WHERE tenant = ? AND id = ?`, owner, expires, m.Attempts, delivered, tenant, m.ID); err != nil {
		return store.Message{}, fmt.Errorf("sqlite: claim message: %w", err)
	}
	return m, nil
}

// UpdateMessage implements [store.MessageLog].
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
	now := s.now()
	stamp, err := storeTime(now)
	if err != nil {
		return store.Message{}, err
	}
	var out store.Message
	err = s.write(ctx, func(tx *txn) error {
		m, err := getMessage(ctx, tx, tenant, id)
		if err != nil {
			return err
		}
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
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET state = ?, run_id = ?, processed_at = ?, next_attempt_at = ?,
			lease_owner = '', lease_expires = NULL WHERE tenant = ? AND id = ?`,
			string(m.State), m.RunID, processed, next, tenant, id); err != nil {
			return fmt.Errorf("sqlite: update message: %w", err)
		}
		if u.Tokens > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE threads SET tokens = tokens + ?, updated_at = ? WHERE tenant = ? AND id = ?`,
				u.Tokens, stamp, tenant, m.ThreadID); err != nil {
				return fmt.Errorf("sqlite: update message: %w", err)
			}
		}
		out = m
		return nil
	})
	return out, err
}

// messageDue is backend.MessageDue in SQL, over messages m, with now as ?1.
const messageDue = `m.state <> 'done' AND (m.lease_owner = '' OR m.lease_expires <= ?1)
	AND (m.next_attempt_at IS NULL OR m.next_attempt_at <= ?1)
	AND (m.state <> 'queued' OR NOT EXISTS (SELECT 1 FROM messages e WHERE e.tenant = m.tenant
		AND e.thread_id = m.thread_id AND e.to_conversation = m.to_conversation AND e.state = 'queued' AND e.hop < m.hop))`

// ClaimDueMessage implements [store.MessageAdmin]. In one write transaction
// (which holds the database's write lock, so claimers take turns) it reads
// the first due message in claim order and leases it.
func (a admin) ClaimDueMessage(ctx context.Context, owner string, ttl time.Duration) (store.TenantMessage, error) {
	if err := ctx.Err(); err != nil {
		return store.TenantMessage{}, err
	}
	if err := backend.CheckLease(owner, ttl); err != nil {
		return store.TenantMessage{}, err
	}
	now := a.s.now()
	if _, err := storeTime(now.Add(ttl)); err != nil {
		return store.TenantMessage{}, err
	}
	var out store.TenantMessage
	err := a.s.write(ctx, func(tx *txn) error {
		var tenant, id string
		// NULLs (due at once) sort first.
		err := tx.QueryRowContext(ctx, `SELECT m.tenant, m.id FROM messages m WHERE `+messageDue+`
			ORDER BY m.next_attempt_at, m.created_at, m.tenant, m.id LIMIT 1`, argTime(now)).Scan(&tenant, &id)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: no message is due", store.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("sqlite: claim message: %w", err)
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
			return fmt.Errorf("sqlite: message %q has a bad tenant: %w", id, err)
		}
		out = store.TenantMessage{Tenant: t, Message: m}
		return nil
	})
	return out, err
}

// scanMessage reads one row of messageColumns.
func scanMessage(row scanner) (store.Message, error) {
	var (
		m                                         store.Message
		kind, state, data                         string
		created, delivered, processed, next, exps sql.NullInt64
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
func scanThread(row scanner) (store.Thread, error) {
	var (
		th               store.Thread
		status           string
		created, updated sql.NullInt64
	)
	if err := row.Scan(&th.ID, &status, &th.StopCode, &th.StopReason, &th.Hops, &th.Tokens, &created, &updated); err != nil {
		return store.Thread{}, err
	}
	th.Status = store.ThreadStatus(status)
	th.CreatedAt, th.UpdatedAt = loadTime(created), loadTime(updated)
	return th, nil
}
