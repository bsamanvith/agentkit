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

const entryColumns = `seq, kind, messages, record, run_id, covers, created_at`

// Append implements [store.ConversationStore].
//
// One transaction: read the last entry, refuse a gap, then a plain INSERT.
// The primary key, not the read, decides whether the position is free: when
// the INSERT fails, the row at that position tells a retry (the same entry,
// nil) from a conflict.
func (s *Store) Append(ctx context.Context, id string, e store.Entry) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	if err := backend.CheckID("conversation", id); err != nil {
		return err
	}
	st, err := backend.CheckEntry(e)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var last int64
		var lastCreated sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT seq, created_at FROM entries
			WHERE tenant = ? AND conversation = ? ORDER BY seq DESC LIMIT 1`,
			tenant, id).Scan(&last, &lastCreated)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("sqlite: append: %w", err)
		}
		if e.Seq > last+1 {
			return fmt.Errorf("%w: conversation %q has %d entries, got entry %d", store.ErrGap, id, last, e.Seq)
		}
		created := s.now()
		if prev := loadTime(lastCreated); created.Before(prev) {
			created = prev // times never go backwards along the log
		}
		at, err := storeTime(created)
		if err != nil {
			return fmt.Errorf("sqlite: append: the clock reads %w", err)
		}
		_, insertErr := tx.ExecContext(ctx, `INSERT INTO entries
			(tenant, conversation, seq, kind, messages, record, run_id, covers, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			tenant, id, e.Seq, string(st.Kind), string(st.Messages), string(st.Record), st.RunID, st.Covers, at)
		if insertErr == nil {
			return nil
		}
		taken, _, err := scanEntry(tx.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM entries
			WHERE tenant = ? AND conversation = ? AND seq = ?`, tenant, id, e.Seq))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("sqlite: append: %w", insertErr)
		case err != nil:
			return fmt.Errorf("sqlite: append: %w", errors.Join(insertErr, err))
		case taken.Same(st):
			return nil
		}
		return fmt.Errorf("%w: conversation %q already has a different entry %d", store.ErrConflict, id, e.Seq)
	})
}

// History implements [store.ConversationStore]. Pages are keyset queries on
// seq (or created_at for a seek by time), so a page reads only its rows.
func (s *Store) History(ctx context.Context, id string, p store.Page) (store.EntryPage, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.EntryPage{}, err
	}
	if err := backend.CheckID("conversation", id); err != nil {
		return store.EntryPage{}, err
	}
	limit, start, err := backend.ParseHistoryPage(p)
	if err != nil {
		return store.EntryPage{}, err
	}

	where := []string{"tenant = ?", "conversation = ?"}
	args := []any{tenant, id}
	asc := p.Order == store.Ascending
	switch {
	case start.HasAfter && asc:
		where, args = append(where, "seq > ?"), append(args, start.After)
	case start.HasAfter:
		where, args = append(where, "seq < ?"), append(args, start.After)
	case start.FromSeq > 0 && asc:
		where, args = append(where, "seq >= ?"), append(args, start.FromSeq)
	case start.FromSeq > 0:
		where, args = append(where, "seq <= ?"), append(args, start.FromSeq)
	case !start.FromTime.IsZero() && asc:
		where, args = append(where, "created_at >= ?"), append(args, argTime(start.FromTime))
	case !start.FromTime.IsZero():
		where, args = append(where, "created_at <= ?"), append(args, argTime(start.FromTime))
	}
	order := "seq"
	if !start.FromTime.IsZero() {
		// The same order (times never go backwards along the log), read from
		// the entries_created index without a sort.
		order = "created_at, seq"
	}
	if !asc {
		order = strings.ReplaceAll(order, ",", " DESC,") + " DESC"
	}
	// One row more than the page says whether there is a next page.
	rows, err := s.r.QueryContext(ctx, `SELECT `+entryColumns+` FROM entries WHERE `+
		strings.Join(where, " AND ")+` ORDER BY `+order+` LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return store.EntryPage{}, fmt.Errorf("sqlite: history: %w", err)
	}
	entries, err := scanEntries(rows)
	if err != nil {
		return store.EntryPage{}, fmt.Errorf("sqlite: history: %w", err)
	}
	var page store.EntryPage
	if len(entries) > limit {
		entries = entries[:limit]
		page.Next = backend.HistoryCursor(p.Order, entries[limit-1].Seq)
	}
	page.Entries = entries
	return page, nil
}

// Context implements [store.ConversationStore]. It is one statement, so it
// reads one snapshot: the latest compaction and every entry after it.
func (s *Store) Context(ctx context.Context, id string) (store.ContextView, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.ContextView{}, err
	}
	if err := backend.CheckID("conversation", id); err != nil {
		return store.ContextView{}, err
	}
	rows, err := s.r.QueryContext(ctx, `SELECT `+entryColumns+` FROM entries
		WHERE tenant = ?1 AND conversation = ?2 AND seq >= COALESCE(
			(SELECT MAX(seq) FROM entries WHERE tenant = ?1 AND conversation = ?2 AND kind = 'compaction'), 1)
		ORDER BY seq`, tenant, id)
	if err != nil {
		return store.ContextView{}, fmt.Errorf("sqlite: context: %w", err)
	}
	entries, err := scanEntries(rows)
	if err != nil {
		return store.ContextView{}, fmt.Errorf("sqlite: context: %w", err)
	}
	view := store.ContextView{Entries: entries}
	if n := len(entries); n > 0 {
		view.Seq = entries[n-1].Seq
	}
	return view, nil
}

type scanner interface{ Scan(dest ...any) error }

// scanEntry reads one row of entryColumns.
func scanEntry(row scanner) (backend.Stored, store.Entry, error) {
	var (
		st      backend.Stored
		seq     int64
		kind    string
		created sql.NullInt64
	)
	if err := row.Scan(&seq, &kind, &st.Messages, &st.Record, &st.RunID, &st.Covers, &created); err != nil {
		return backend.Stored{}, store.Entry{}, err
	}
	st.Kind = store.Kind(kind)
	if len(st.Record) == 0 {
		st.Record = nil // stored as ''
	}
	e, err := st.Entry(seq, loadTime(created))
	return st, e, err
}

func scanEntries(rows *sql.Rows) ([]store.Entry, error) {
	defer rows.Close()
	var entries []store.Entry
	for rows.Next() {
		_, e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
