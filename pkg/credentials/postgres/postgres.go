// Package postgres is a [credentials.BlobStore] in the Postgres store's
// database (package store/postgres): a credential_blobs table that the
// store's migration 0011 creates, on the store's connection pool. It is the
// backend for replicas: every process sharing the database shares the
// credentials.
//
// The table holds per credential its tenant, user, slot, version, kind,
// sealed blob and times, never plaintext: the vault seals before it stores.
// Compare-and-swap is the UPDATE's WHERE version = $n, so it holds across
// replicas. A deleted credential keeps its row with blob NULL, so its
// version never repeats.
//
// The pool's connections set app.tenant from the context, and the table has
// the store's row-level security policy, so a query sees and writes only its
// tenant's rows even if it forgot its tenant filter. Run it, as the store,
// as a role without BYPASSRLS.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/internal/sqltime"
	storepg "github.com/bsamanvith/agentkit/pkg/store/postgres"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Store keeps credential blobs in a Postgres store's database. It is safe
// for concurrent use.
type Store struct {
	pool  *pgxpool.Pool
	close func()
}

var _ credentials.BlobStore = (*Store)(nil)

// Open connects to the Postgres store's database at url as [storepg.Open]
// does with opts (migrating its schema by default, or with
// storepg.WithMigrate(false) only checking it), and returns the credential
// store in it. Close closes the connections.
func Open(ctx context.Context, url string, opts ...storepg.Option) (*Store, error) {
	st, err := storepg.Open(ctx, url, opts...)
	if err != nil {
		return nil, fmt.Errorf("credentials: %w", err)
	}
	s := New(st)
	s.close = st.Close
	return s, nil
}

// New returns the credential store in st's database, on st's pool; st stays
// the caller's to close.
func New(st *storepg.Store) *Store {
	return &Store{pool: st.Pool(), close: func() {}}
}

// Close closes the connections if [Open] opened them.
func (s *Store) Close() error {
	s.close()
	return nil
}

func tenant(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
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
		expiry           *int64
	)
	err = s.pool.QueryRow(ctx, `SELECT kind, version, blob, created_at, updated_at, expiry FROM credential_blobs
		WHERE tenant = $1 AND user_id = $2 AND slot = $3 AND blob IS NOT NULL`, tn, user, slot).
		Scan(&b.Kind, &b.Version, &b.Data, &created, &updated, &expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return credentials.Blob{}, fmt.Errorf("credential %q: %w", slot, credentials.ErrNotFound)
	}
	if err != nil {
		return credentials.Blob{}, fmt.Errorf("credentials: postgres: get: %w", err)
	}
	if b.Data == nil {
		b.Data = []byte{}
	}
	b.Slot, b.User = slot, user
	b.CreatedAt, b.UpdatedAt, b.Expiry = sqltime.Load(created), sqltime.Load(updated), loadNull(expiry)
	return b, nil
}

// loadNull decodes a time column that may be NULL (the zero time).
func loadNull(n *int64) time.Time {
	if n == nil {
		return time.Time{}
	}
	return sqltime.Load(*n)
}

// Put implements [credentials.BlobStore]. A create is one INSERT that, on a
// tombstone, takes its row with the next version (ON CONFLICT, so racing
// creators never see a unique violation); a replace is one UPDATE whose
// WHERE holds the version it replaces.
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
		err = s.pool.QueryRow(ctx, `INSERT INTO credential_blobs
			(tenant, user_id, slot, version, kind, blob, created_at, updated_at, expiry)
			VALUES ($1, $2, $3, 1, $4, $5, $6, $6, $7)
			ON CONFLICT (tenant, user_id, slot) DO UPDATE SET
				version = credential_blobs.version + 1, kind = excluded.kind, blob = excluded.blob,
				created_at = excluded.created_at, updated_at = excluded.updated_at, expiry = excluded.expiry
			WHERE credential_blobs.blob IS NULL
			RETURNING version, created_at, updated_at`,
			tn, user, slot, string(b.Kind), data, at, expiry).Scan(&m.Version, &created, &updated)
		if errors.Is(err, pgx.ErrNoRows) {
			return credentials.Meta{}, fmt.Errorf("credential %q: %w", slot, credentials.ErrExists)
		}
	} else {
		err = s.pool.QueryRow(ctx, `UPDATE credential_blobs SET
				version = version + 1, kind = $1, blob = $2, updated_at = GREATEST($3::BIGINT, created_at), expiry = $4
			WHERE tenant = $5 AND user_id = $6 AND slot = $7 AND version = $8 AND blob IS NOT NULL
			RETURNING version, created_at, updated_at`,
			string(b.Kind), data, at, expiry, tn, user, slot, ifVersion).Scan(&m.Version, &created, &updated)
		if errors.Is(err, pgx.ErrNoRows) {
			return credentials.Meta{}, fmt.Errorf("credential %q: version %d: %w", slot, ifVersion, credentials.ErrConflict)
		}
	}
	if err != nil {
		return credentials.Meta{}, fmt.Errorf("credentials: postgres: put: %w", err)
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
	tag, err := s.pool.Exec(ctx, `UPDATE credential_blobs SET blob = NULL
		WHERE tenant = $1 AND user_id = $2 AND slot = $3 AND blob IS NOT NULL AND ($4::BIGINT = 0 OR version = $4)`,
		tn, user, slot, ifVersion)
	if err != nil {
		return fmt.Errorf("credentials: postgres: delete: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
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
	rows, err := s.pool.Query(ctx, `SELECT slot, kind, version, created_at, updated_at, expiry FROM credential_blobs
		WHERE tenant = $1 AND user_id = $2 AND blob IS NOT NULL ORDER BY slot`, tn, user)
	if err != nil {
		return nil, fmt.Errorf("credentials: postgres: list: %w", err)
	}
	defer rows.Close()
	var out []credentials.Meta
	for rows.Next() {
		m := credentials.Meta{User: user}
		var created, updated int64
		var expiry *int64
		if err := rows.Scan(&m.Slot, &m.Kind, &m.Version, &created, &updated, &expiry); err != nil {
			return nil, fmt.Errorf("credentials: postgres: list: %w", err)
		}
		m.CreatedAt, m.UpdatedAt, m.Expiry = sqltime.Load(created), sqltime.Load(updated), loadNull(expiry)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("credentials: postgres: list: %w", err)
	}
	return out, nil
}
