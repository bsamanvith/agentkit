// Package sqlite keeps a plot's work events in a SQLite file (the farm
// app's own, beside its farms).
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite" // the driver: pure Go

	"github.com/bsamanvith/agentkit/apps/farm/work"
)

var _ work.Store = (*Store)(nil)

// Store is a work.Store on a SQLite file.
type Store struct{ db *sql.DB }

const schema = `CREATE TABLE IF NOT EXISTS work_events (
	farm TEXT NOT NULL,
	id   TEXT NOT NULL,
	at   INTEGER NOT NULL,
	data TEXT NOT NULL,
	PRIMARY KEY (farm, id)
) STRICT;
CREATE INDEX IF NOT EXISTS work_events_by_farm ON work_events (farm, at, id)`

// Open opens the file at path, creating it and its table if needed.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("work sqlite: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String())
	if err != nil {
		return nil, fmt.Errorf("work sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, errors.Join(fmt.Errorf("work sqlite: open %s: %w", path, err), db.Close())
	}
	return &Store{db: db}, nil
}

// Close closes the file.
func (s *Store) Close() error { return s.db.Close() }

// Append implements work.Store, in one transaction.
func (s *Store) Append(ctx context.Context, evs ...work.Event) (n int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("work sqlite: append: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	for _, e := range evs {
		data, err := json.Marshal(e)
		if err != nil {
			return 0, fmt.Errorf("work sqlite: append: %w", err)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO work_events (id, farm, at, data) VALUES (?, ?, ?, ?) ON CONFLICT (farm, id) DO NOTHING`,
			e.ID, e.Farm, e.At.UnixNano(), string(data))
		if err != nil {
			return 0, fmt.Errorf("work sqlite: append: %w", err)
		}
		k, _ := res.RowsAffected()
		n += int(k)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("work sqlite: append: %w", err)
	}
	return n, nil
}

// Events implements work.Store.
func (s *Store) Events(ctx context.Context, farmID string) ([]work.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM work_events WHERE farm = ? ORDER BY at, id`, farmID)
	if err != nil {
		return nil, fmt.Errorf("work sqlite: events: %w", err)
	}
	defer rows.Close()
	var out []work.Event
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("work sqlite: events: %w", err)
		}
		var e work.Event
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			return nil, fmt.Errorf("work sqlite: events: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("work sqlite: events: %w", err)
	}
	return out, nil
}
