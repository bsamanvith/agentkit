// Package sqlite keeps accounts, identities, sessions and codes in a
// SQLite file: an accounts.Store for one process (the farm app). Each row
// keeps its record as JSON, with the columns it is looked up by.
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

	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
)

var _ accounts.Store = (*Store)(nil)

// Store is an accounts.Store on a SQLite file.
type Store struct{ db *sql.DB }

const schema = `CREATE TABLE IF NOT EXISTS accounts (
	id   TEXT PRIMARY KEY,
	data TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS account_identities (
	lane       TEXT NOT NULL,
	subject    TEXT NOT NULL,
	account    TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	data       TEXT NOT NULL,
	secret     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (lane, subject)
) STRICT;
CREATE INDEX IF NOT EXISTS account_identities_account ON account_identities (account, created_at);
CREATE TABLE IF NOT EXISTS account_sessions (
	key       TEXT PRIMARY KEY,
	account   TEXT NOT NULL,
	created   INTEGER NOT NULL,
	last_seen INTEGER NOT NULL,
	data      TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS account_sessions_account ON account_sessions (account);
CREATE TABLE IF NOT EXISTS account_challenges (
	key     TEXT PRIMARY KEY,
	expires INTEGER NOT NULL,
	data    TEXT NOT NULL
) STRICT`

// Open opens the file at path, creating it and its tables if needed. It may
// be the app's own database file.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("accounts sqlite: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String())
	if err != nil {
		return nil, fmt.Errorf("accounts sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, errors.Join(fmt.Errorf("accounts sqlite: open %s: %w", path, err), db.Close())
	}
	return &Store{db: db}, nil
}

// Close closes the file.
func (s *Store) Close() error { return s.db.Close() }

func wrap(op string, err error) error { return fmt.Errorf("accounts sqlite: %s: %w", op, err) }

func encode(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func decode[T any](data string) (T, error) {
	var v T
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		return v, wrap("decode", err)
	}
	return v, nil
}

func affected(res sql.Result, err error, op string) error {
	if err != nil {
		return wrap(op, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return wrap(op, err)
	} else if n == 0 {
		return accounts.ErrNotFound
	}
	return nil
}

// CreateAccount implements accounts.Store.
func (s *Store) CreateAccount(ctx context.Context, a accounts.Account) error {
	data, err := encode(a)
	if err != nil {
		return wrap("create account", err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO accounts (id, data) VALUES (?, ?) ON CONFLICT (id) DO NOTHING`, a.ID, data)
	if err := affected(res, err, "create account"); err != nil {
		if errors.Is(err, accounts.ErrNotFound) {
			return accounts.ErrExists
		}
		return err
	}
	return nil
}

// Account implements accounts.Store.
func (s *Store) Account(ctx context.Context, id string) (accounts.Account, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM accounts WHERE id = ?`, id).Scan(&data)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return accounts.Account{}, accounts.ErrNotFound
	case err != nil:
		return accounts.Account{}, wrap("account", err)
	}
	return decode[accounts.Account](data)
}

// UpdateAccount implements accounts.Store.
func (s *Store) UpdateAccount(ctx context.Context, a accounts.Account) error {
	data, err := encode(a)
	if err != nil {
		return wrap("update account", err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET data = ? WHERE id = ?`, data, a.ID)
	return affected(res, err, "update account")
}

// DeleteAccount implements accounts.Store.
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("delete account", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	if err := affected(res, err, "delete account"); err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM account_identities WHERE account = ?`, `DELETE FROM account_sessions WHERE account = ?`} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return wrap("delete account", err)
		}
	}
	return wrap2("delete account", tx.Commit())
}

func wrap2(op string, err error) error {
	if err != nil {
		return wrap(op, err)
	}
	return nil
}

// AddIdentity implements accounts.Store.
func (s *Store) AddIdentity(ctx context.Context, id accounts.Identity) error {
	data, err := encode(id)
	if err != nil {
		return wrap("add identity", err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO account_identities (lane, subject, account, created_at, data, secret) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (lane, subject) DO NOTHING`, id.Lane, id.Subject, id.Account, id.CreatedAt.UnixNano(), data, id.Secret)
	if err := affected(res, err, "add identity"); err != nil {
		if errors.Is(err, accounts.ErrNotFound) {
			return accounts.ErrExists
		}
		return err
	}
	return nil
}

func scanIdentity(data, account, secret string) (accounts.Identity, error) {
	id, err := decode[accounts.Identity](data)
	id.Account, id.Secret = account, secret
	return id, err
}

// Identity implements accounts.Store.
func (s *Store) Identity(ctx context.Context, lane, subject string) (accounts.Identity, error) {
	var data, account, secret string
	err := s.db.QueryRowContext(ctx, `SELECT data, account, secret FROM account_identities WHERE lane = ? AND subject = ?`, lane, subject).Scan(&data, &account, &secret)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return accounts.Identity{}, accounts.ErrNotFound
	case err != nil:
		return accounts.Identity{}, wrap("identity", err)
	}
	return scanIdentity(data, account, secret)
}

// Identities implements accounts.Store.
func (s *Store) Identities(ctx context.Context, account string) ([]accounts.Identity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data, account, secret FROM account_identities WHERE account = ? ORDER BY created_at, lane, subject`, account)
	if err != nil {
		return nil, wrap("identities", err)
	}
	defer rows.Close()
	out := []accounts.Identity{}
	for rows.Next() {
		var data, acc, secret string
		if err := rows.Scan(&data, &acc, &secret); err != nil {
			return nil, wrap("identities", err)
		}
		id, err := scanIdentity(data, acc, secret)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, wrap2("identities", rows.Err())
}

// UpdateIdentity implements accounts.Store.
func (s *Store) UpdateIdentity(ctx context.Context, id accounts.Identity) error {
	data, err := encode(id)
	if err != nil {
		return wrap("update identity", err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE account_identities SET data = ?, secret = ? WHERE lane = ? AND subject = ?`, data, id.Secret, id.Lane, id.Subject)
	return affected(res, err, "update identity")
}

// RemoveIdentity implements accounts.Store.
func (s *Store) RemoveIdentity(ctx context.Context, lane, subject string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM account_identities WHERE lane = ? AND subject = ?`, lane, subject)
	return affected(res, err, "remove identity")
}

// CreateSession implements accounts.Store.
func (s *Store) CreateSession(ctx context.Context, key string, sess accounts.Session) error {
	data, err := encode(sess)
	if err != nil {
		return wrap("create session", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO account_sessions (key, account, created, last_seen, data) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET account = excluded.account, created = excluded.created, last_seen = excluded.last_seen, data = excluded.data`,
		key, sess.Account, sess.Created.UnixNano(), sess.LastSeen.UnixNano(), data)
	return wrap2("create session", err)
}

// Session implements accounts.Store.
func (s *Store) Session(ctx context.Context, key string) (accounts.Session, error) {
	var data string
	var last int64
	err := s.db.QueryRowContext(ctx, `SELECT data, last_seen FROM account_sessions WHERE key = ?`, key).Scan(&data, &last)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return accounts.Session{}, accounts.ErrNotFound
	case err != nil:
		return accounts.Session{}, wrap("session", err)
	}
	sess, err := decode[accounts.Session](data)
	sess.LastSeen = time.Unix(0, last).UTC()
	sess.Created = sess.Created.UTC()
	return sess, err
}

// TouchSession implements accounts.Store.
func (s *Store) TouchSession(ctx context.Context, key string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE account_sessions SET last_seen = ? WHERE key = ?`, at.UnixNano(), key)
	return affected(res, err, "touch session")
}

// DeleteSession implements accounts.Store.
func (s *Store) DeleteSession(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM account_sessions WHERE key = ?`, key)
	return wrap2("delete session", err)
}

// DeleteSessions implements accounts.Store.
func (s *Store) DeleteSessions(ctx context.Context, account string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM account_sessions WHERE account = ?`, account)
	return wrap2("delete sessions", err)
}

// ExpireSessions implements accounts.Store.
func (s *Store) ExpireSessions(ctx context.Context, idle, created time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM account_sessions WHERE last_seen < ? OR created < ?`, idle.UnixNano(), created.UnixNano())
	return wrap2("expire sessions", err)
}

// PutChallenge implements accounts.Store.
func (s *Store) PutChallenge(ctx context.Context, key string, c accounts.Challenge) error {
	data, err := encode(c)
	if err != nil {
		return wrap("put challenge", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO account_challenges (key, expires, data) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET expires = excluded.expires, data = excluded.data`, key, c.Expires.UnixNano(), data)
	return wrap2("put challenge", err)
}

// Challenge implements accounts.Store.
func (s *Store) Challenge(ctx context.Context, key string) (accounts.Challenge, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM account_challenges WHERE key = ?`, key).Scan(&data)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return accounts.Challenge{}, accounts.ErrNotFound
	case err != nil:
		return accounts.Challenge{}, wrap("challenge", err)
	}
	return decode[accounts.Challenge](data)
}

// AttemptChallenge implements accounts.Store.
func (s *Store) AttemptChallenge(ctx context.Context, key string) (accounts.Challenge, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `UPDATE account_challenges SET data = json_set(data, '$.attempts', json_extract(data, '$.attempts') + 1)
		WHERE key = ? RETURNING data`, key).Scan(&data)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return accounts.Challenge{}, accounts.ErrNotFound
	case err != nil:
		return accounts.Challenge{}, wrap("attempt challenge", err)
	}
	return decode[accounts.Challenge](data)
}

// DeleteChallenge implements accounts.Store.
func (s *Store) DeleteChallenge(ctx context.Context, key string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM account_challenges WHERE key = ?`, key)
	return affected(res, err, "delete challenge")
}

// ExpireChallenges implements accounts.Store.
func (s *Store) ExpireChallenges(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM account_challenges WHERE expires < ?`, now.UnixNano())
	return wrap2("expire challenges", err)
}
