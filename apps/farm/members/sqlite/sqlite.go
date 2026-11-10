// Package sqlite keeps the farm app's holdings and members in its SQLite
// file.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite" // the driver: pure Go, so the binary stays static

	"github.com/bsamanvith/agentkit/apps/farm/members"
)

var _ members.Store = (*Store)(nil)

// Store is a members.Store on a SQLite file.
type Store struct{ db *sql.DB }

const schema = `CREATE TABLE IF NOT EXISTS holdings (
	id   TEXT PRIMARY KEY,
	data TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS holding_members (
	holding  TEXT NOT NULL,
	account  TEXT NOT NULL,
	added_at INTEGER NOT NULL,
	data     TEXT NOT NULL,
	PRIMARY KEY (holding, account)
) STRICT;
CREATE INDEX IF NOT EXISTS holding_members_account ON holding_members (account, added_at)`

// Open opens the file at path (the app's database), creating the tables if
// needed.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("members sqlite: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String())
	if err != nil {
		return nil, fmt.Errorf("members sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, errors.Join(fmt.Errorf("members sqlite: open %s: %w", path, err), db.Close())
	}
	return &Store{db: db}, nil
}

// Close closes the file.
func (s *Store) Close() error { return s.db.Close() }

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("members sqlite: %s: %w", op, err)
}

func decode[T any](data string) (T, error) {
	var v T
	return v, wrap("decode", json.Unmarshal([]byte(data), &v))
}

// CreateHolding implements members.Store.
func (s *Store) CreateHolding(ctx context.Context, h members.Holding) error {
	data, err := json.Marshal(h)
	if err != nil {
		return wrap("create holding", err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO holdings (id, data) VALUES (?, ?) ON CONFLICT (id) DO NOTHING`, h.ID, string(data))
	if err != nil {
		return wrap("create holding", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return members.ErrExists
	}
	return nil
}

// Holding implements members.Store.
func (s *Store) Holding(ctx context.Context, id string) (members.Holding, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM holdings WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return members.Holding{}, members.ErrNotFound
	}
	if err != nil {
		return members.Holding{}, wrap("holding", err)
	}
	return decode[members.Holding](data)
}

// UpdateHolding implements members.Store.
func (s *Store) UpdateHolding(ctx context.Context, h members.Holding) error {
	data, err := json.Marshal(h)
	if err != nil {
		return wrap("update holding", err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE holdings SET data = ? WHERE id = ?`, string(data), h.ID)
	if err != nil {
		return wrap("update holding", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return members.ErrNotFound
	}
	return nil
}

// CountHoldings implements members.Store.
func (s *Store) CountHoldings(ctx context.Context) (int, error) {
	var n int
	return n, wrap("count holdings", s.db.QueryRowContext(ctx, `SELECT count(*) FROM holdings`).Scan(&n))
}

// PutMember implements members.Store.
func (s *Store) PutMember(ctx context.Context, m members.Member) error {
	data, err := json.Marshal(m)
	if err != nil {
		return wrap("put member", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO holding_members (holding, account, added_at, data) VALUES (?, ?, ?, ?)
		ON CONFLICT (holding, account) DO UPDATE SET added_at = excluded.added_at, data = excluded.data`,
		m.Holding, m.Account, m.AddedAt.UnixNano(), string(data))
	return wrap("put member", err)
}

// Member implements members.Store.
func (s *Store) Member(ctx context.Context, holding, account string) (members.Member, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM holding_members WHERE holding = ? AND account = ?`, holding, account).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return members.Member{}, members.ErrNotFound
	}
	if err != nil {
		return members.Member{}, wrap("member", err)
	}
	return decode[members.Member](data)
}

func (s *Store) list(ctx context.Context, where, arg string) ([]members.Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM holding_members WHERE `+where+` = ? ORDER BY added_at, holding, account`, arg)
	if err != nil {
		return nil, wrap("members", err)
	}
	defer rows.Close()
	out := []members.Member{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, wrap("members", err)
		}
		m, err := decode[members.Member](data)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, wrap("members", rows.Err())
}

// Members implements members.Store.
func (s *Store) Members(ctx context.Context, holding string) ([]members.Member, error) {
	return s.list(ctx, "holding", holding)
}

// Memberships implements members.Store.
func (s *Store) Memberships(ctx context.Context, account string) ([]members.Member, error) {
	return s.list(ctx, "account", account)
}

// RemoveMember implements members.Store.
func (s *Store) RemoveMember(ctx context.Context, holding, account string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM holding_members WHERE holding = ? AND account = ?`, holding, account)
	if err != nil {
		return wrap("remove member", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return members.ErrNotFound
	}
	return nil
}
