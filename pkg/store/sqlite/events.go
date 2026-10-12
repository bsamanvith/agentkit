package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

// AppendEvents implements [store.EventLog].
//
// One transaction, which holds the write lock from its start: read the last
// event and the stored events the batch overlaps, decide, then INSERT the
// new ones.
func (s *Store) AppendEvents(ctx context.Context, runID string, events []store.Event) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	if err := backend.CheckID("run", runID); err != nil {
		return err
	}
	batch, err := backend.CheckEvents(events)
	if err != nil || len(batch) == 0 {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var last int64
		var lastCreated sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT seq, created_at FROM events
			WHERE tenant = ? AND run_id = ? ORDER BY seq DESC LIMIT 1`,
			tenant, runID).Scan(&last, &lastCreated)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("sqlite: append events: %w", err)
		}
		var taken []store.Event
		if first := batch[0].Seq; first <= last {
			taken, err = queryEvents(ctx, tx, `WHERE tenant = ? AND run_id = ? AND seq BETWEEN ? AND ? ORDER BY seq`,
				tenant, runID, first, batch[len(batch)-1].Seq)
			if err != nil {
				return fmt.Errorf("sqlite: append events: %w", err)
			}
		}
		add, err := backend.NewEvents(runID, batch, last, taken)
		if err != nil || len(add) == 0 {
			return err
		}
		created := s.now()
		if prev := loadTime(lastCreated); created.Before(prev) {
			created = prev // times never go backwards along the log
		}
		at, err := storeTime(created)
		if err != nil {
			return fmt.Errorf("sqlite: append events: the clock reads %w", err)
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO events (tenant, run_id, seq, type, data, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("sqlite: append events: %w", err)
		}
		defer stmt.Close()
		for _, e := range add {
			if _, err := stmt.ExecContext(ctx, tenant, runID, e.Seq, e.Type, string(e.Data), at); err != nil {
				return fmt.Errorf("sqlite: append event %d: %w", e.Seq, err)
			}
		}
		return nil
	})
}

// EventsAfter implements [store.EventLog].
func (s *Store) EventsAfter(ctx context.Context, runID string, after int64, limit int) ([]store.Event, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	if err := backend.CheckID("run", runID); err != nil {
		return nil, err
	}
	n, err := backend.EventLimit(after, limit)
	if err != nil {
		return nil, err
	}
	out, err := queryEvents(ctx, s.r, `WHERE tenant = ? AND run_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
		tenant, runID, after, n)
	if err != nil {
		return nil, fmt.Errorf("sqlite: events after: %w", err)
	}
	return out, nil
}

// LastEventSeq implements [store.EventLog].
func (s *Store) LastEventSeq(ctx context.Context, runID string) (int64, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return 0, err
	}
	if err := backend.CheckID("run", runID); err != nil {
		return 0, err
	}
	var last int64
	if err := s.r.QueryRowContext(ctx, `SELECT coalesce(max(seq), 0) FROM events WHERE tenant = ? AND run_id = ?`,
		tenant, runID).Scan(&last); err != nil {
		return 0, fmt.Errorf("sqlite: last event seq: %w", err)
	}
	return last, nil
}

// queryEvents reads the events a WHERE clause (and what follows it) selects.
func queryEvents(ctx context.Context, q querier, where string, args ...any) ([]store.Event, error) {
	rows, err := q.QueryContext(ctx, `SELECT seq, type, data, created_at FROM events `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Event
	for rows.Next() {
		var e store.Event
		var data string
		var created sql.NullInt64
		if err := rows.Scan(&e.Seq, &e.Type, &data, &created); err != nil {
			return nil, err
		}
		if data != "" {
			e.Data = []byte(data)
		}
		e.CreatedAt = loadTime(created)
		out = append(out, e)
	}
	return out, rows.Err()
}
