// Package sqlite keeps automations, their firings, runs and notices in a
// SQLite file (the farm app's own, beside its farms).
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the driver: pure Go

	"github.com/bsamanvith/agentkit/apps/farm/automation"
)

var _ automation.Store = (*Store)(nil)

// Store is an automation.Store on a SQLite file.
type Store struct{ db *sql.DB }

const schema = `CREATE TABLE IF NOT EXISTS automations (
	id         TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL,
	data       TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS automation_firings (
	automation TEXT NOT NULL,
	due        INTEGER NOT NULL,
	PRIMARY KEY (automation, due)
) STRICT;
CREATE TABLE IF NOT EXISTS automation_runs (
	id         TEXT PRIMARY KEY,
	automation TEXT NOT NULL,
	farm       TEXT NOT NULL,
	started    INTEGER NOT NULL,
	data       TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS automation_runs_by_automation ON automation_runs (automation, started);
CREATE INDEX IF NOT EXISTS automation_runs_by_farm ON automation_runs (farm, started);
CREATE TABLE IF NOT EXISTS notices (
	id         TEXT PRIMARY KEY,
	farm       TEXT NOT NULL,
	key        TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	read_at    INTEGER,
	data       TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS notices_by_farm ON notices (farm, created_at);
CREATE INDEX IF NOT EXISTS notices_by_key ON notices (farm, key, created_at)`

// Open opens the file at path, creating it and its tables if needed.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("automation sqlite: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String())
	if err != nil {
		return nil, fmt.Errorf("automation sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, errors.Join(fmt.Errorf("automation sqlite: open %s: %w", path, err), db.Close())
	}
	return &Store{db: db}, nil
}

// Close closes the file.
func (s *Store) Close() error { return s.db.Close() }

func wrap(op string, err error) error { return fmt.Errorf("automation sqlite: %s: %w", op, err) }

// CreateAutomation implements automation.Store.
func (s *Store) CreateAutomation(ctx context.Context, a automation.Automation) error {
	data, err := json.Marshal(a)
	if err != nil {
		return wrap("create", err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO automations (id, created_at, data) VALUES (?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		a.ID, a.CreatedAt.UnixNano(), string(data))
	if err != nil {
		return wrap("create", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: automation %s exists", automation.ErrInvalid, a.ID)
	}
	return nil
}

// GetAutomation implements automation.Store.
func (s *Store) GetAutomation(ctx context.Context, id string) (automation.Automation, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM automations WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return automation.Automation{}, fmt.Errorf("%w: %s", automation.ErrNotFound, id)
	}
	if err != nil {
		return automation.Automation{}, wrap("get", err)
	}
	return decode[automation.Automation](data)
}

// ListAutomations implements automation.Store.
func (s *Store) ListAutomations(ctx context.Context) ([]automation.Automation, error) {
	return list[automation.Automation](ctx, s.db, `SELECT data FROM automations ORDER BY created_at, id`)
}

// UpdateAutomation implements automation.Store.
func (s *Store) UpdateAutomation(ctx context.Context, a automation.Automation) error {
	data, err := json.Marshal(a)
	if err != nil {
		return wrap("update", err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE automations SET data = ? WHERE id = ?`, string(data), a.ID)
	if err != nil {
		return wrap("update", err)
	}
	return affected(res, a.ID)
}

// SetLastDue implements automation.Store.
func (s *Store) SetLastDue(ctx context.Context, id string, due time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("set last due", err)
	}
	defer func() { _ = tx.Rollback() }()
	var data string
	err = tx.QueryRowContext(ctx, `SELECT data FROM automations WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", automation.ErrNotFound, id)
	}
	if err != nil {
		return wrap("set last due", err)
	}
	a, err := decode[automation.Automation](data)
	if err != nil {
		return err
	}
	a.LastDue = due.UTC()
	b, err := json.Marshal(a)
	if err != nil {
		return wrap("set last due", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE automations SET data = ? WHERE id = ?`, string(b), id); err != nil {
		return wrap("set last due", err)
	}
	return tx.Commit()
}

// DeleteAutomation implements automation.Store.
func (s *Store) DeleteAutomation(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM automations WHERE id = ?`, id)
	if err != nil {
		return wrap("delete", err)
	}
	return affected(res, id)
}

// ClaimFiring implements automation.Store.
func (s *Store) ClaimFiring(ctx context.Context, id string, due time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO automation_firings (automation, due) VALUES (?, ?) ON CONFLICT DO NOTHING`, id, due.UnixNano())
	if err != nil {
		return false, wrap("claim", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// AddRun implements automation.Store.
func (s *Store) AddRun(ctx context.Context, r automation.Run) error {
	data, err := json.Marshal(r)
	if err != nil {
		return wrap("add run", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO automation_runs (id, automation, farm, started, data) VALUES (?, ?, ?, ?, ?)`,
		r.ID, r.Automation, r.Farm, r.Started.UnixNano(), string(data))
	if err != nil {
		return wrap("add run", err)
	}
	return nil
}

// ListRuns implements automation.Store.
func (s *Store) ListRuns(ctx context.Context, f automation.RunFilter) ([]automation.Run, error) {
	var where []string
	var args []any
	if f.Automation != "" {
		where, args = append(where, "automation = ?"), append(args, f.Automation)
	}
	if f.Farm != "" {
		where, args = append(where, "farm = ?"), append(args, f.Farm)
	}
	q := `SELECT data FROM automation_runs`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY started DESC, id DESC LIMIT ?`
	return list[automation.Run](ctx, s.db, q, append(args, limit(f.Limit))...)
}

func limit(n int) int {
	if n <= 0 {
		return 50
	}
	return min(n, 1000)
}

// AddNotice implements automation.Store.
func (s *Store) AddNotice(ctx context.Context, n automation.Notice, since time.Time) (bool, error) {
	data, err := json.Marshal(n)
	if err != nil {
		return false, wrap("add notice", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, wrap("add notice", err)
	}
	defer func() { _ = tx.Rollback() }()
	var seen int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM notices WHERE farm = ? AND key = ? AND created_at >= ?`,
		n.Farm, n.Key, since.UnixNano()).Scan(&seen); err != nil {
		return false, wrap("add notice", err)
	}
	if seen > 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notices (id, farm, key, created_at, read_at, data) VALUES (?, ?, ?, ?, NULL, ?)`,
		n.ID, n.Farm, n.Key, n.CreatedAt.UnixNano(), string(data)); err != nil {
		return false, wrap("add notice", err)
	}
	return true, wrap0(tx.Commit())
}

func wrap0(err error) error {
	if err != nil {
		return wrap("commit", err)
	}
	return nil
}

// ListNotices implements automation.Store.
func (s *Store) ListNotices(ctx context.Context, f automation.NoticeFilter) ([]automation.Notice, error) {
	var where []string
	var args []any
	if f.Farm != "" {
		where, args = append(where, "farm = ?"), append(args, f.Farm)
	}
	if f.Unread {
		where = append(where, "read_at IS NULL")
	}
	q := `SELECT data, read_at FROM notices`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, append(args, limit(f.Limit))...)
	if err != nil {
		return nil, wrap("list notices", err)
	}
	defer rows.Close()
	out := []automation.Notice{}
	for rows.Next() {
		var data string
		var read sql.NullInt64
		if err := rows.Scan(&data, &read); err != nil {
			return nil, wrap("list notices", err)
		}
		n, err := decode[automation.Notice](data)
		if err != nil {
			return nil, err
		}
		n.CreatedAt = n.CreatedAt.UTC()
		n.ReadAt = nil
		if read.Valid {
			t := time.Unix(0, read.Int64).UTC()
			n.ReadAt = &t
		}
		out = append(out, n)
	}
	return out, wrapErr("list notices", rows.Err())
}

func wrapErr(op string, err error) error {
	if err != nil {
		return wrap(op, err)
	}
	return nil
}

// MarkRead implements automation.Store.
func (s *Store) MarkRead(ctx context.Context, farm string, ids []string, at time.Time) (int, error) {
	q := `UPDATE notices SET read_at = ? WHERE read_at IS NULL`
	args := []any{at.UnixNano()}
	if farm != "" {
		q, args = q+` AND farm = ?`, append(args, farm)
	}
	if len(ids) > 0 {
		q += ` AND id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, wrap("mark read", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// Prune implements automation.Store.
func (s *Store) Prune(ctx context.Context, before time.Time) error {
	b := before.UnixNano()
	for _, q := range []string{
		`DELETE FROM automation_runs WHERE started < ?`,
		`DELETE FROM notices WHERE created_at < ?`,
		`DELETE FROM automation_firings WHERE due < ?`,
	} {
		if _, err := s.db.ExecContext(ctx, q, b); err != nil {
			return wrap("prune", err)
		}
	}
	return nil
}

func affected(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return wrap("update", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", automation.ErrNotFound, id)
	}
	return nil
}

func decode[T any](data string) (T, error) {
	var v T
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		return v, wrap("decode", err)
	}
	return v, nil
}

func list[T any](ctx context.Context, db *sql.DB, q string, args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrap("list", err)
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, wrap("list", err)
		}
		v, err := decode[T](data)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, wrapErr("list", rows.Err())
}
