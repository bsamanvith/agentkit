package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

const conversationColumns = `id, agent, title, messages, created_at, updated_at`

// CreateConversation implements [store.ConversationIndex].
//
// One transaction, which holds the write lock from its start: read the
// record, and if there is none, insert it with the messages already in the
// log counted. No append can come in between.
func (s *Store) CreateConversation(ctx context.Context, c store.Conversation) (store.Conversation, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Conversation{}, err
	}
	if err := backend.CheckConversation(c); err != nil {
		return store.Conversation{}, err
	}
	at, err := storeTime(s.now())
	if err != nil {
		return store.Conversation{}, fmt.Errorf("sqlite: create conversation: the clock reads %w", err)
	}
	var rec store.Conversation
	err = s.write(ctx, func(tx *txn) error {
		had, err := getConversation(ctx, tx, tenant, c.ID)
		switch {
		case err == nil && had.Agent != c.Agent:
			return fmt.Errorf("%w: conversation %q belongs to agent %q", store.ErrConflict, c.ID, had.Agent)
		case err == nil:
			rec = had
			return nil
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
		rec, err = scanConversation(tx.QueryRowContext(ctx, `INSERT INTO conversations
			(tenant, id, agent, title, messages, created_at, updated_at)
			VALUES (?1, ?2, ?3, ?4, (SELECT COALESCE(SUM(json_array_length(messages)), 0) FROM entries
				WHERE tenant = ?1 AND conversation = ?2 AND kind NOT IN ('compaction', 'interrupt')), ?5, ?5)
			RETURNING `+conversationColumns, tenant, c.ID, c.Agent, c.Title, at))
		if err != nil {
			return fmt.Errorf("sqlite: create conversation: %w", err)
		}
		return nil
	})
	return rec, err
}

// counted adds n messages, appended at at, to the conversation's record, if
// it has one: in the transaction of the append.
func counted(ctx context.Context, tx *txn, tenant, id string, n int64, at any) error {
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET messages = messages + ?, updated_at = MAX(updated_at, ?)
		WHERE tenant = ? AND id = ?`, n, at, tenant, id); err != nil {
		return fmt.Errorf("sqlite: count appended messages: %w", err)
	}
	return nil
}

// GetConversation implements [store.ConversationIndex].
func (s *Store) GetConversation(ctx context.Context, id string) (store.Conversation, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Conversation{}, err
	}
	return getConversation(ctx, s.r, tenant, id)
}

func getConversation(ctx context.Context, q querier, tenant, id string) (store.Conversation, error) {
	c, err := scanConversation(q.QueryRowContext(ctx, `SELECT `+conversationColumns+` FROM conversations
		WHERE tenant = ? AND id = ?`, tenant, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Conversation{}, fmt.Errorf("%w: conversation %q", store.ErrNotFound, id)
	}
	if err != nil {
		return store.Conversation{}, fmt.Errorf("sqlite: conversation %q: %w", id, err)
	}
	return c, nil
}

// ListConversations implements [store.ConversationIndex]. Pages are keyset
// queries on (created_at, id), read from the conversations_created index.
func (s *Store) ListConversations(ctx context.Context, agent string, p store.Page) (store.ConversationPage, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.ConversationPage{}, err
	}
	limit, start, err := backend.ParseConversationPage(p)
	if err != nil {
		return store.ConversationPage{}, err
	}
	conds, args := []string{"tenant = ?"}, []any{tenant}
	if agent != "" {
		conds, args = append(conds, "agent = ?"), append(args, agent)
	}
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
	// One row more than the page says whether there is a next page.
	rows, err := s.r.QueryContext(ctx, `SELECT `+conversationColumns+` FROM conversations WHERE `+
		strings.Join(conds, " AND ")+` ORDER BY `+order+` LIMIT CAST(? AS INTEGER)`, append(args, limit+1)...)
	if err != nil {
		return store.ConversationPage{}, fmt.Errorf("sqlite: list conversations: %w", err)
	}
	defer rows.Close()
	var cs []store.Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return store.ConversationPage{}, fmt.Errorf("sqlite: list conversations: %w", err)
		}
		cs = append(cs, c)
	}
	if err := rows.Err(); err != nil {
		return store.ConversationPage{}, fmt.Errorf("sqlite: list conversations: %w", err)
	}
	var page store.ConversationPage
	if len(cs) > limit {
		cs = cs[:limit]
		last := cs[limit-1]
		page.Next = backend.ConversationCursor(p.Order, backend.RunKey{Created: last.CreatedAt, ID: last.ID})
	}
	page.Conversations = cs
	return page, nil
}

// SetTitle implements [store.ConversationIndex]: one UPDATE of a record
// with no title, then the record is read.
func (s *Store) SetTitle(ctx context.Context, id, title string) (store.Conversation, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Conversation{}, err
	}
	if err := backend.CheckText("conversation title", title); err != nil {
		return store.Conversation{}, err
	}
	at, err := storeTime(s.now())
	if err != nil {
		return store.Conversation{}, fmt.Errorf("sqlite: set title: the clock reads %w", err)
	}
	var c store.Conversation
	err = s.write(ctx, func(tx *txn) error {
		if title != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE conversations SET title = ?, updated_at = MAX(updated_at, ?)
				WHERE tenant = ? AND id = ? AND title = ''`, title, at, tenant, id); err != nil {
				return fmt.Errorf("sqlite: set title: %w", err)
			}
		}
		c, err = getConversation(ctx, tx, tenant, id)
		return err
	})
	return c, err
}

// DeleteConversation implements [store.ConversationIndex].
//
// One transaction, which holds the write lock from its start: delete the
// record, refuse while a run of the conversation is leased (rolling the
// delete back), then delete the runs' event logs and checkpoints, the runs
// and the log.
func (s *Store) DeleteConversation(ctx context.Context, id string) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	return s.write(ctx, func(tx *txn) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE tenant = ? AND id = ?`, tenant, id)
		if err != nil {
			return fmt.Errorf("sqlite: delete conversation: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("sqlite: delete conversation: %w", err)
		} else if n == 0 {
			return fmt.Errorf("%w: conversation %q", store.ErrNotFound, id)
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+runColumns+` FROM runs
			WHERE tenant = ? AND conversation = ? AND status NOT IN `+final, tenant, id)
		if err != nil {
			return fmt.Errorf("sqlite: delete conversation: %w", err)
		}
		live, err := scanRuns(rows, nil)
		if err != nil {
			return fmt.Errorf("sqlite: delete conversation: %w", err)
		}
		for _, r := range live {
			if backend.Held(r.Lease, now) {
				return fmt.Errorf("%w: run %q of conversation %q is running", store.ErrLeased, r.ID, id)
			}
		}
		for _, q := range []string{
			`DELETE FROM events WHERE tenant = ?1 AND run_id IN (SELECT id FROM runs WHERE tenant = ?1 AND conversation = ?2)`,
			`DELETE FROM checkpoints WHERE tenant = ?1 AND run_id IN (SELECT id FROM runs WHERE tenant = ?1 AND conversation = ?2)`,
			`DELETE FROM notifications WHERE tenant = ?1 AND run_id IN (SELECT id FROM runs WHERE tenant = ?1 AND conversation = ?2)`,
			`DELETE FROM runs WHERE tenant = ?1 AND conversation = ?2`,
			`DELETE FROM entries WHERE tenant = ?1 AND conversation = ?2`,
		} {
			if _, err := tx.ExecContext(ctx, q, tenant, id); err != nil {
				return fmt.Errorf("sqlite: delete conversation: %w", err)
			}
		}
		return nil
	})
}

// scanConversation reads one row of conversationColumns.
func scanConversation(row scanner) (store.Conversation, error) {
	var (
		c                store.Conversation
		created, updated sql.NullInt64
	)
	if err := row.Scan(&c.ID, &c.Agent, &c.Title, &c.Messages, &created, &updated); err != nil {
		return store.Conversation{}, err
	}
	c.CreatedAt, c.UpdatedAt = loadTime(created), loadTime(updated)
	return c, nil
}
