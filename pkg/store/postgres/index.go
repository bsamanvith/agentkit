package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

const conversationColumns = `id, agent, title, messages, created_at, updated_at`

// lockConversation takes the conversation's lock until tx ends: a
// transaction-scoped advisory lock on a hash of the tenant and id (two
// conversations that share a hash only wait for each other). Appends,
// CreateConversation and DeleteConversation take it first, so that the
// statements after it see every write of the others to that conversation.
// Under READ COMMITTED, without it, an append whose count found no record
// yet could commit after a create counted the log without its entry, and the
// record would miss its messages for good.
func lockConversation(ctx context.Context, tx pgx.Tx, tenant, id string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text || '/' || $2::text, 0))`, tenant, id)
	return err
}

// counted adds n messages, appended at at, to the conversation's record, if
// it has one: in the transaction of the append.
func counted(ctx context.Context, tx pgx.Tx, tenant, id string, n, at int64) error {
	_, err := tx.Exec(ctx, `UPDATE conversations SET messages = messages + $3, updated_at = GREATEST(updated_at, $4)
		WHERE tenant = $1 AND id = $2`, tenant, id, n, at)
	return err
}

// CreateConversation implements [store.ConversationIndex].
//
// One transaction, under the conversation's lock: an INSERT of the record
// with the messages already in the log counted, which the primary key turns
// into nothing when the record exists; then that record is read.
func (s *Store) CreateConversation(ctx context.Context, c store.Conversation) (store.Conversation, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Conversation{}, err
	}
	if err := backend.CheckConversation(c); err != nil {
		return store.Conversation{}, err
	}
	var rec store.Conversation
	err = retry(ctx, func() error {
		at, err := storeTime(s.now())
		if err != nil {
			return fmt.Errorf("postgres: create conversation: the clock reads %w", err)
		}
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if err := lockConversation(ctx, tx, tenant, c.ID); err != nil {
				return fail("create conversation", err)
			}
			rec, err = scanConversation(tx.QueryRow(ctx, `INSERT INTO conversations
				(tenant, id, agent, title, messages, created_at, updated_at)
				SELECT $1::text, $2::text, $3::text, $4::text, COALESCE(SUM(json_array_length(messages::json)), 0), $5::bigint, $5::bigint
				FROM entries WHERE tenant = $1 AND conversation = $2
				ON CONFLICT (tenant, id) DO NOTHING
				RETURNING `+conversationColumns, tenant, c.ID, c.Agent, c.Title, *at))
			switch {
			case err == nil:
				return nil
			case !errors.Is(err, pgx.ErrNoRows):
				return fail("create conversation", err)
			}
			had, err := getConversation(ctx, tx, tenant, c.ID)
			switch {
			case err != nil:
				return err
			case had.Agent != c.Agent:
				return fmt.Errorf("%w: conversation %q belongs to agent %q", store.ErrConflict, c.ID, had.Agent)
			}
			rec = had
			return nil
		})
	})
	return rec, err
}

// GetConversation implements [store.ConversationIndex].
func (s *Store) GetConversation(ctx context.Context, id string) (store.Conversation, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Conversation{}, err
	}
	return getConversation(ctx, s.pool, tenant, id)
}

func getConversation(ctx context.Context, q querier, tenant, id string) (store.Conversation, error) {
	if !backend.TextOK(id) {
		return store.Conversation{}, fmt.Errorf("%w: conversation %q", store.ErrNotFound, id)
	}
	c, err := scanConversation(q.QueryRow(ctx, `SELECT `+conversationColumns+` FROM conversations
		WHERE tenant = $1 AND id = $2`, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Conversation{}, fmt.Errorf("%w: conversation %q", store.ErrNotFound, id)
	}
	if err != nil {
		return store.Conversation{}, fail(fmt.Sprintf("conversation %q", id), err)
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
	if !backend.TextOK(agent) {
		return store.ConversationPage{}, nil // no record has it
	}
	conds, args := []string{"tenant = $1"}, []any{tenant}
	// next adds a parameter and returns its placeholder.
	next := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if agent != "" {
		conds = append(conds, "agent = "+next(agent))
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
	// One row more than the page says whether there is a next page.
	rows, err := s.pool.Query(ctx, `SELECT `+conversationColumns+` FROM conversations WHERE `+
		strings.Join(conds, " AND ")+` ORDER BY `+order+` LIMIT `+next(limit+1), args...)
	if err != nil {
		return store.ConversationPage{}, fail("list conversations", err)
	}
	defer rows.Close()
	var cs []store.Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return store.ConversationPage{}, fail("list conversations", err)
		}
		cs = append(cs, c)
	}
	if err := rows.Err(); err != nil {
		return store.ConversationPage{}, fail("list conversations", err)
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
// with no title that returns the record, which is read when it changes
// nothing.
func (s *Store) SetTitle(ctx context.Context, id, title string) (store.Conversation, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Conversation{}, err
	}
	if err := backend.CheckText("conversation title", title); err != nil {
		return store.Conversation{}, err
	}
	var c store.Conversation
	err = retry(ctx, func() error {
		at, err := storeTime(s.now())
		if err != nil {
			return fmt.Errorf("postgres: set title: the clock reads %w", err)
		}
		if title != "" && backend.TextOK(id) {
			c, err = scanConversation(s.pool.QueryRow(ctx, `UPDATE conversations
				SET title = $3, updated_at = GREATEST(updated_at, $4)
				WHERE tenant = $1 AND id = $2 AND title = ''
				RETURNING `+conversationColumns, tenant, id, title, *at))
			if err == nil {
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return fail("set title", err)
			}
		}
		c, err = getConversation(ctx, s.pool, tenant, id)
		return err
	})
	return c, err
}

// DeleteConversation implements [store.ConversationIndex].
//
// One transaction, under the conversation's lock: delete the record, lock
// the conversation's unfinished runs (a Claim waits for the transaction, and
// then finds no run) and refuse while one is leased, rolling the delete
// back; then delete the runs' event logs and checkpoints, the runs and the
// log.
func (s *Store) DeleteConversation(ctx context.Context, id string) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	if !backend.TextOK(id) {
		return fmt.Errorf("%w: conversation %q", store.ErrNotFound, id)
	}
	return retry(ctx, func() error {
		now := s.now()
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if err := lockConversation(ctx, tx, tenant, id); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `DELETE FROM conversations WHERE tenant = $1 AND id = $2`, tenant, id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("%w: conversation %q", store.ErrNotFound, id)
			}
			rows, err := tx.Query(ctx, `SELECT `+runColumns+` FROM runs
				WHERE tenant = $1 AND conversation = $2 AND status NOT IN `+final+` FOR UPDATE`, tenant, id)
			if err != nil {
				return err
			}
			live, err := scanRuns(rows, nil)
			if err != nil {
				return err
			}
			for _, r := range live {
				if backend.Held(r.Lease, now) {
					return fmt.Errorf("%w: run %q of conversation %q is running", store.ErrLeased, r.ID, id)
				}
			}
			for _, q := range []string{
				`DELETE FROM events WHERE tenant = $1 AND run_id IN (SELECT id FROM runs WHERE tenant = $1 AND conversation = $2)`,
				`DELETE FROM checkpoints WHERE tenant = $1 AND run_id IN (SELECT id FROM runs WHERE tenant = $1 AND conversation = $2)`,
				`DELETE FROM runs WHERE tenant = $1 AND conversation = $2`,
				`DELETE FROM entries WHERE tenant = $1 AND conversation = $2`,
			} {
				if _, err := tx.Exec(ctx, q, tenant, id); err != nil {
					return err
				}
			}
			return nil
		})
		if code, _ := sqlState(err); code != "" {
			return fail("delete conversation", err)
		}
		return err
	})
}

// scanConversation reads one row of conversationColumns.
func scanConversation(row pgx.Row) (store.Conversation, error) {
	var (
		c                store.Conversation
		created, updated *int64
	)
	if err := row.Scan(&c.ID, &c.Agent, &c.Title, &c.Messages, &created, &updated); err != nil {
		return store.Conversation{}, err
	}
	c.CreatedAt, c.UpdatedAt = loadTime(created), loadTime(updated)
	return c, nil
}
