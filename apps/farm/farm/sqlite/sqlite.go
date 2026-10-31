// Package sqlite keeps farms in a SQLite file, one row per farm with the
// farm as JSON: the app reads and writes whole farms, and their boundaries
// are only ever searched in Go.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // the driver: pure Go, so the binary stays static

	"github.com/bsamanvith/agentkit/apps/farm/farm"
)

var _ farm.Store = (*Store)(nil)

// Store is a farm.Store on a SQLite file.
type Store struct{ db *sql.DB }

const schema = `CREATE TABLE IF NOT EXISTS farms (
	id         TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL,
	data       TEXT NOT NULL
) STRICT`

// Open opens the file at path, creating it and its table if needed.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("farm sqlite: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String())
	if err != nil {
		return nil, fmt.Errorf("farm sqlite: open %s: %w", path, err)
	}
	// One connection: writes are serialised anyway, and a farmer's
	// requests are few.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, errors.Join(fmt.Errorf("farm sqlite: open %s: %w", path, err), db.Close())
	}
	return &Store{db: db}, nil
}

// Close closes the file.
func (s *Store) Close() error { return s.db.Close() }

// Create implements farm.Store.
func (s *Store) Create(ctx context.Context, f farm.Farm) error {
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("farm sqlite: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO farms (id, created_at, data) VALUES (?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		f.ID, f.CreatedAt.UnixNano(), string(data))
	if err != nil {
		return fmt.Errorf("farm sqlite: create: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: farm %s exists", farm.ErrInvalid, f.ID)
	}
	return nil
}

// Get implements farm.Store.
func (s *Store) Get(ctx context.Context, id string) (farm.Farm, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM farms WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return farm.Farm{}, fmt.Errorf("%w: %s", farm.ErrNotFound, id)
	}
	if err != nil {
		return farm.Farm{}, fmt.Errorf("farm sqlite: get: %w", err)
	}
	return decode(data)
}

// List implements farm.Store.
func (s *Store) List(ctx context.Context) ([]farm.Farm, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM farms ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("farm sqlite: list: %w", err)
	}
	defer rows.Close()
	out := []farm.Farm{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("farm sqlite: list: %w", err)
		}
		f, err := decode(data)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("farm sqlite: list: %w", err)
	}
	return out, nil
}

// Update implements farm.Store.
func (s *Store) Update(ctx context.Context, f farm.Farm) error {
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("farm sqlite: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE farms SET data = ? WHERE id = ?`, string(data), f.ID)
	if err != nil {
		return fmt.Errorf("farm sqlite: update: %w", err)
	}
	return affected(res, f.ID)
}

// Delete implements farm.Store.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM farms WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("farm sqlite: delete: %w", err)
	}
	return affected(res, id)
}

func affected(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("farm sqlite: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", farm.ErrNotFound, id)
	}
	return nil
}

func decode(data string) (farm.Farm, error) {
	var f farm.Farm
	if err := json.Unmarshal([]byte(data), &f); err != nil {
		return farm.Farm{}, fmt.Errorf("farm sqlite: decode: %w", err)
	}
	// JSON keeps the instant, not the location; farms are stamped in UTC.
	f.CreatedAt, f.UpdatedAt = f.CreatedAt.In(time.UTC), f.UpdatedAt.In(time.UTC)
	return f, nil
}
