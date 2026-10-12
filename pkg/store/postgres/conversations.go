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

const entryColumns = `seq, kind, messages, record, run_id, covers, created_at`

// Append implements [store.ConversationStore].
//
// One statement inserts the entry only if the entry before it exists, at a
// time no earlier than that entry's. The primary key, not a read, decides
// whether the position is free. When nothing was inserted, the log says why:
// a gap, or a taken position whose row tells a retry (the same entry, nil)
// from a conflict.
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
	return retry(ctx, func() error {
		created, err := storeTime(s.now())
		if err != nil {
			return fmt.Errorf("postgres: append: the clock reads %w", err)
		}
		tag, insertErr := s.pool.Exec(ctx, `INSERT INTO entries
			(tenant, conversation, seq, kind, messages, record, run_id, covers, created_at)
			SELECT $1::text, $2::text, $3::bigint, $4::text, $5::text, $6::text, $7::text, $8::bigint,
				GREATEST($9::bigint, COALESCE(prev.created_at, $9::bigint))
			FROM (SELECT 1) AS one
			LEFT JOIN entries AS prev ON prev.tenant = $1 AND prev.conversation = $2 AND prev.seq = $3::bigint - 1
			WHERE $3::bigint = 1 OR prev.seq IS NOT NULL`,
			tenant, id, e.Seq, string(st.Kind), string(st.Messages), string(st.Record), st.RunID, st.Covers, *created)
		switch code, _ := sqlState(insertErr); {
		case insertErr == nil && tag.RowsAffected() == 1:
			return nil
		case insertErr != nil && code != uniqueViolation:
			return fail("append", insertErr)
		}
		// Taken (the insert hit the primary key) or a gap (it inserted
		// nothing). Which, and what is there, is read now.
		taken, _, err := scanEntry(s.pool.QueryRow(ctx, `SELECT `+entryColumns+` FROM entries
			WHERE tenant = $1 AND conversation = $2 AND seq = $3`, tenant, id, e.Seq))
		switch {
		case err == nil && taken.Same(st):
			return nil
		case err == nil:
			return fmt.Errorf("%w: conversation %q already has a different entry %d", store.ErrConflict, id, e.Seq)
		case !errors.Is(err, pgx.ErrNoRows):
			return fail("append", err)
		}
		var last int64
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM entries
			WHERE tenant = $1 AND conversation = $2`, tenant, id).Scan(&last); err != nil {
			return fail("append", err)
		}
		if e.Seq > last+1 {
			return fmt.Errorf("%w: conversation %q has %d entries, got entry %d", store.ErrGap, id, last, e.Seq)
		}
		return errRetry // the entry before it was appended in between
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

	where := []string{"tenant = $1", "conversation = $2"}
	args := []any{tenant, id}
	arg := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	asc := p.Order == store.Ascending
	switch {
	case start.HasAfter && asc:
		arg("seq > ?", start.After)
	case start.HasAfter:
		arg("seq < ?", start.After)
	case start.FromSeq > 0 && asc:
		arg("seq >= ?", start.FromSeq)
	case start.FromSeq > 0:
		arg("seq <= ?", start.FromSeq)
	case !start.FromTime.IsZero() && asc:
		arg("created_at >= ?", argTime(start.FromTime))
	case !start.FromTime.IsZero():
		arg("created_at <= ?", argTime(start.FromTime))
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
	args = append(args, limit+1)
	rows, err := s.pool.Query(ctx, `SELECT `+entryColumns+` FROM entries WHERE `+
		strings.Join(where, " AND ")+` ORDER BY `+order+` LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return store.EntryPage{}, fail("history", err)
	}
	entries, err := scanEntries(rows)
	if err != nil {
		return store.EntryPage{}, fail("history", err)
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
	rows, err := s.pool.Query(ctx, `SELECT `+entryColumns+` FROM entries
		WHERE tenant = $1 AND conversation = $2 AND seq >= COALESCE(
			(SELECT MAX(seq) FROM entries WHERE tenant = $1 AND conversation = $2 AND kind = 'compaction'), 1)
		ORDER BY seq`, tenant, id)
	if err != nil {
		return store.ContextView{}, fail("context", err)
	}
	entries, err := scanEntries(rows)
	if err != nil {
		return store.ContextView{}, fail("context", err)
	}
	view := store.ContextView{Entries: entries}
	if n := len(entries); n > 0 {
		view.Seq = entries[n-1].Seq
	}
	return view, nil
}

// scanEntry reads one row of entryColumns.
func scanEntry(row pgx.Row) (backend.Stored, store.Entry, error) {
	var (
		st            backend.Stored
		seq           int64
		kind          string
		messages, rec string
		created       *int64
	)
	if err := row.Scan(&seq, &kind, &messages, &rec, &st.RunID, &st.Covers, &created); err != nil {
		return backend.Stored{}, store.Entry{}, err
	}
	st.Kind = store.Kind(kind)
	st.Messages = []byte(messages)
	if rec != "" {
		st.Record = []byte(rec) // stored as '' when empty
	}
	e, err := st.Entry(seq, loadTime(created))
	return st, e, err
}

func scanEntries(rows pgx.Rows) ([]store.Entry, error) {
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
