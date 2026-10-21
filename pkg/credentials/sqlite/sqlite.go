// Package sqlite is a [credentials.BlobStore] in the SQLite store's file
// (package store/sqlite): a credential_blobs table that the store's
// migration 0009 creates, on the store's connections. It is for one machine
// and local images, as the SQLite store is; several processes may share the
// file. Replicas on several machines use package credentials/postgres.
//
// The table holds per credential its tenant, user, slot, version, kind,
// sealed blob and times, never plaintext: the vault seals before it stores.
// Compare-and-swap is the UPDATE's WHERE version = ?, so it holds across
// connections and processes, not only within one Store. A deleted credential
// keeps its row with blob NULL, so its version never repeats.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/internal/sqltime"
	storesqlite "github.com/bsamanvith/agentkit/pkg/store/sqlite"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Store keeps credential blobs in a SQLite store's file. It is safe for
// concurrent use.
type Store struct {
	w, r  *sql.DB
	close func() error
}

var _ credentials.BlobStore = (*Store)(nil)

// Open opens the SQLite store's file at path, creating it and migrating its
// schema as [storesqlite.Open] does, and returns the credential store in it.
// Close closes the file.
func Open(ctx context.Context, path string) (*Store, error) {
	st, err := storesqlite.Open(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("credentials: %w", err)
	}
	s := New(st)
	s.close = st.Close
	return s, nil
}

// New returns the credential store in st's file, on st's connections; st
// stays the caller's to close.
func New(st *storesqlite.Store) *Store {
	w, r := st.DB()
	return &Store{w: w, r: r, close: func() error { return nil }}
}

// Close closes the file if [Open] opened it.
func (s *Store) Close() error { return s.close() }

func tenant(ctx context.Context) (string, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return "", err
	}
	return t.ID(), nil
}

// Get implements [credentials.BlobStore].
func (s *Store) Get(ctx context.Context, user, slot string) (credentials.Blob, error) {
	tn, err := tenant(ctx)
	if err != nil {
		return credentials.Blob{}, err
	}
	var (
		b                credentials.Blob
		created, updated int64
		expiry           sql.NullInt64
	)
	err = s.r.QueryRowContext(ctx, `SELECT kind, version, blob, created_at, updated_at, expiry FROM credential_blobs
		WHERE tenant = ? AND user_id = ? AND slot = ? AND blob IS NOT NULL`, tn, user, slot).
		Scan(&b.Kind, &b.Version, &b.Data, &created, &updated, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return credentials.Blob{}, fmt.Errorf("credential %q: %w", slot, credentials.ErrNotFound)
	}
	if err != nil {
		return credentials.Blob{}, fmt.Errorf("credentials: sqlite: get: %w", err)
	}
	if b.Data == nil {
		b.Data = []byte{}
	}
	b.Slot, b.User = slot, user
	b.CreatedAt, b.UpdatedAt, b.Expiry = sqltime.Load(created), sqltime.Load(updated), sqltime.LoadNull(expiry.Int64, expiry.Valid)
	return b, nil
}

// Put implements [credentials.BlobStore]. A create is one INSERT that, on a
// tombstone, takes its row with the next version; a replace is one UPDATE
// whose WHERE holds the version it replaces.
func (s *Store) Put(ctx context.Context, user, slot string, b credentials.Blob, ifVersion int64) (credentials.Meta, error) {
	tn, err := tenant(ctx)
	if err != nil {
		return credentials.Meta{}, err
	}
	at, expiry, err := sqltime.Blob(b)
	if err != nil {
		return credentials.Meta{}, err
	}
	data := b.Data
	if data == nil {
		data = []byte{} // NULL marks a tombstone
	}
	m := credentials.Meta{Slot: slot, User: user, Kind: b.Kind, Expiry: b.Expiry.UTC()}
	var created, updated int64
	if ifVersion == 0 {
		err = s.w.QueryRowContext(ctx, `INSERT INTO credential_blobs
			(tenant, user_id, slot, version, kind, blob, created_at, updated_at, expiry)
			VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?)
			ON CONFLICT (tenant, user_id, slot) DO UPDATE SET
				version = credential_blobs.version + 1, kind = excluded.kind, blob = excluded.blob,
				created_at = excluded.created_at, updated_at = excluded.updated_at, expiry = excluded.expiry
			WHERE credential_blobs.blob IS NULL
			RETURNING version, created_at, updated_at`,
			tn, user, slot, b.Kind, data, at, at, expiry).Scan(&m.Version, &created, &updated)
		if errors.Is(err, sql.ErrNoRows) {
			return credentials.Meta{}, fmt.Errorf("credential %q: %w", slot, credentials.ErrExists)
		}
	} else {
		err = s.w.QueryRowContext(ctx, `UPDATE credential_blobs SET
				version = version + 1, kind = ?, blob = ?, updated_at = MAX(?, created_at), expiry = ?
			WHERE tenant = ? AND user_id = ? AND slot = ? AND version = ? AND blob IS NOT NULL
			RETURNING version, created_at, updated_at`,
			b.Kind, data, at, expiry, tn, user, slot, ifVersion).Scan(&m.Version, &created, &updated)
		if errors.Is(err, sql.ErrNoRows) {
			return credentials.Meta{}, fmt.Errorf("credential %q: version %d: %w", slot, ifVersion, credentials.ErrConflict)
		}
	}
	if err != nil {
		return credentials.Meta{}, fmt.Errorf("credentials: sqlite: put: %w", err)
	}
	m.CreatedAt, m.UpdatedAt = sqltime.Load(created), sqltime.Load(updated)
	return m, nil
}

// Delete implements [credentials.BlobStore]: the row stays, with blob NULL.
func (s *Store) Delete(ctx context.Context, user, slot string, ifVersion int64) error {
	tn, err := tenant(ctx)
	if err != nil {
		return err
	}
	res, err := s.w.ExecContext(ctx, `UPDATE credential_blobs SET blob = NULL
		WHERE tenant = ? AND user_id = ? AND slot = ? AND blob IS NOT NULL AND (? = 0 OR version = ?)`,
		tn, user, slot, ifVersion, ifVersion)
	if err != nil {
		return fmt.Errorf("credentials: sqlite: delete: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	// Nothing deleted: say whether there was nothing, or another version.
	if _, err := s.Get(ctx, user, slot); err != nil {
		return err
	}
	return fmt.Errorf("credential %q: version %d: %w", slot, ifVersion, credentials.ErrConflict)
}

// List implements [credentials.BlobStore].
func (s *Store) List(ctx context.Context, user string) ([]credentials.Meta, error) {
	tn, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.r.QueryContext(ctx, `SELECT slot, kind, version, created_at, updated_at, expiry FROM credential_blobs
		WHERE tenant = ? AND user_id = ? AND blob IS NOT NULL ORDER BY slot`, tn, user)
	if err != nil {
		return nil, fmt.Errorf("credentials: sqlite: list: %w", err)
	}
	defer rows.Close()
	var out []credentials.Meta
	for rows.Next() {
		m := credentials.Meta{User: user}
		var created, updated int64
		var expiry sql.NullInt64
		if err := rows.Scan(&m.Slot, &m.Kind, &m.Version, &created, &updated, &expiry); err != nil {
			return nil, fmt.Errorf("credentials: sqlite: list: %w", err)
		}
		m.CreatedAt, m.UpdatedAt, m.Expiry = sqltime.Load(created), sqltime.Load(updated), sqltime.LoadNull(expiry.Int64, expiry.Valid)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("credentials: sqlite: list: %w", err)
	}
	return out, nil
}
