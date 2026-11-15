// Package sqlite keeps plots' crop cycle records in a SQLite file (the
// farm app's own, beside its farms).
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

	"github.com/bsamanvith/agentkit/apps/farm/cycle"
)

var _ cycle.Store = (*Store)(nil)

// Store is a cycle.Store on a SQLite file.
type Store struct{ db *sql.DB }

const schema = `CREATE TABLE IF NOT EXISTS crop_cycles (
	farm    TEXT PRIMARY KEY,
	version INTEGER NOT NULL,
	data    TEXT NOT NULL
) STRICT`

// Open opens the file at path, creating it and its table if needed.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("cycle sqlite: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String())
	if err != nil {
		return nil, fmt.Errorf("cycle sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, errors.Join(fmt.Errorf("cycle sqlite: open %s: %w", path, err), db.Close())
	}
	return &Store{db: db}, nil
}

// Close closes the file.
func (s *Store) Close() error { return s.db.Close() }

// Get implements cycle.Store.
func (s *Store) Get(ctx context.Context, farmID string) (cycle.Record, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM crop_cycles WHERE farm = ?`, farmID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return cycle.Record{Farm: farmID}, nil
	}
	if err != nil {
		return cycle.Record{}, fmt.Errorf("cycle sqlite: get: %w", err)
	}
	var r cycle.Record
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		return cycle.Record{}, fmt.Errorf("cycle sqlite: get: %w", err)
	}
	return r, nil
}

// Put implements cycle.Store: the version check and the write are one
// statement.
func (s *Store) Put(ctx context.Context, r cycle.Record) (cycle.Record, error) {
	r.Version++
	data, err := json.Marshal(r)
	if err != nil {
		return cycle.Record{}, fmt.Errorf("cycle sqlite: put: %w", err)
	}
	var res sql.Result
	if r.Version == 1 {
		res, err = s.db.ExecContext(ctx, `INSERT INTO crop_cycles (farm, version, data) VALUES (?, 1, ?) ON CONFLICT (farm) DO NOTHING`, r.Farm, string(data))
	} else {
		res, err = s.db.ExecContext(ctx, `UPDATE crop_cycles SET version = ?, data = ? WHERE farm = ? AND version = ?`, r.Version, string(data), r.Farm, r.Version-1)
	}
	if err != nil {
		return cycle.Record{}, fmt.Errorf("cycle sqlite: put: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return cycle.Record{}, errors.Join(cycle.ErrConflict, err)
	}
	var out cycle.Record
	if err := json.Unmarshal(data, &out); err != nil {
		return cycle.Record{}, fmt.Errorf("cycle sqlite: put: %w", err)
	}
	return out, nil
}
