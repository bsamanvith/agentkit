package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

// AppendEvents implements [store.EventLog].
//
// One transaction reads the last event and the stored events the batch
// overlaps (rows up to the last never change: the log is append only),
// decides, and inserts the new events in one statement. The primary key, not
// the read, decides whether a position is free: when another writer inserted
// at one of them in between, the insert fails and the whole append runs again
// from fresh reads, which then see that writer's events.
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
	return retry(ctx, func() error {
		now := s.now()
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var last int64
			var lastCreated *int64
			err := tx.QueryRow(ctx, `SELECT seq, created_at FROM events
				WHERE tenant = $1 AND run_id = $2 ORDER BY seq DESC LIMIT 1`,
				tenant, runID).Scan(&last, &lastCreated)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			var taken []store.Event
			if first := batch[0].Seq; first <= last {
				taken, err = queryEvents(ctx, tx, `WHERE tenant = $1 AND run_id = $2 AND seq BETWEEN $3 AND $4 ORDER BY seq`,
					tenant, runID, first, min(last, batch[len(batch)-1].Seq))
				if err != nil {
					return err
				}
			}
			add, err := backend.NewEvents(runID, batch, last, taken)
			if err != nil || len(add) == 0 {
				return err
			}
			created := now
			if prev := loadTime(lastCreated); created.Before(prev) {
				created = prev // times never go backwards along the log
			}
			at, err := storeTime(created)
			if err != nil {
				return fmt.Errorf("postgres: append events: the clock reads %w", err)
			}
			seqs := make([]int64, len(add))
			types := make([]string, len(add))
			data := make([]string, len(add))
			for i, e := range add {
				seqs[i], types[i], data[i] = e.Seq, e.Type, string(e.Data)
			}
			_, err = tx.Exec(ctx, `INSERT INTO events (tenant, run_id, seq, type, data, created_at)
				SELECT $1, $2, e.seq, e.type, e.data, $6
				FROM unnest($3::bigint[], $4::text[], $5::text[]) AS e (seq, type, data)`,
				tenant, runID, seqs, types, data, at)
			return err
		})
		switch code, constraint := sqlState(err); {
		case err == nil:
			return nil
		case code == uniqueViolation && constraint == "events_pkey":
			return errRetry
		case code != "":
			return fail("append events", err)
		}
		return err
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
	out, err := queryEvents(ctx, s.pool, `WHERE tenant = $1 AND run_id = $2 AND seq > $3 ORDER BY seq LIMIT $4`,
		tenant, runID, after, n)
	if err != nil {
		return nil, fail("events after", err)
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
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events WHERE tenant = $1 AND run_id = $2`,
		tenant, runID).Scan(&last); err != nil {
		return 0, fail("last event seq", err)
	}
	return last, nil
}

// queryEvents reads the events a WHERE clause (and what follows it) selects.
func queryEvents(ctx context.Context, q querier, where string, args ...any) ([]store.Event, error) {
	rows, err := q.Query(ctx, `SELECT seq, type, data, created_at FROM events `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Event
	for rows.Next() {
		var e store.Event
		var data string
		var created *int64
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
