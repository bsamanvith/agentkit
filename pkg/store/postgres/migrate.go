package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// ErrSchemaTooNew means the database has migrations this binary does not
// know: a newer version wrote it. Upgrade the binary; never use the database
// with this one.
var ErrSchemaTooNew = errors.New("database schema is newer than this binary")

// ErrSchemaTooOld means the database lacks migrations this binary needs, and
// the store was opened with WithMigrate(false). Run the migrations ([Migrate],
// or agentkit migrate) first.
var ErrSchemaTooOld = errors.New("database schema is older than this binary")

// The schema, one file per version: migrations/NNNN_name.sql, numbered densely
// from 0001. Applied migrations are never edited; a change is a new file.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

// migrations returns the embedded migrations in order, checking that they are
// numbered densely from 1, and keeps the first limit of them (all when limit
// is 0).
func migrations(limit int) ([]migration, error) {
	files, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for i, f := range files { // fs.Glob sorts, and the numbers are zero-padded
		base := path.Base(f)
		num, name, ok := strings.Cut(strings.TrimSuffix(base, ".sql"), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || len(num) != 4 || v != i+1 {
			return nil, fmt.Errorf("migration %s: want %04d_<name>.sql", base, i+1)
		}
		body, err := migrationFiles.ReadFile(f)
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: name, sql: string(body)})
	}
	if limit > 0 {
		ms = ms[:min(limit, len(ms))]
	}
	return ms, nil
}

// lockKey is the advisory lock migrations hold, one per schema, so that stores
// in different schemas of one database migrate independently.
const lockKey = `hashtext('agentkit.store.migrate'), hashtext(current_schema())`

// migrate brings the schema up to date, applying the first limit migrations
// (all when limit is 0). It holds a session advisory lock on one connection
// throughout, so processes opening the same database at once apply each
// migration once: the others wait, then find the work done. Each migration
// runs in its own transaction (Postgres DDL is transactional) with its row in
// schema_migrations.
func migrate(ctx context.Context, pool *pgxpool.Pool, clk clock.Clock, limit int) (err error) {
	ms, err := migrations(limit)
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(`+lockKey+`)`); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer func() {
		// A connection whose context ended may not run this; closing it
		// ends the session, which drops the lock.
		if _, unlockErr := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(`+lockKey+`)`); unlockErr != nil {
			_ = conn.Conn().Close(context.WithoutCancel(ctx)) // already failing; the lock goes with the session
			err = errors.Join(err, fmt.Errorf("migration unlock: %w", unlockErr))
		}
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER NOT NULL PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at BIGINT  NOT NULL
	)`); err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}
	current, err := version(ctx, conn)
	if err != nil {
		return err
	}
	if current > len(ms) {
		return tooNew(current, len(ms))
	}
	for _, m := range ms[current:] {
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)`,
				m.version, m.name, clk.Now().UnixNano())
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %04d_%s: %w", m.version, m.name, err)
		}
	}
	return nil
}

// checkSchema checks, without changing anything, that the schema is at the
// version of the first limit migrations (all when limit is 0).
func checkSchema(ctx context.Context, pool *pgxpool.Pool, limit int) error {
	ms, err := migrations(limit)
	if err != nil {
		return err
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}
	current := 0
	if exists {
		if current, err = version(ctx, pool); err != nil {
			return err
		}
	}
	switch {
	case current > len(ms):
		return tooNew(current, len(ms))
	case current < len(ms):
		return fmt.Errorf("%w: the database is at version %d, this binary needs %d", ErrSchemaTooOld, current, len(ms))
	}
	return nil
}

func version(ctx context.Context, q querier) (int, error) {
	var v int
	if err := q.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("schema version: %w", err)
	}
	return v, nil
}

func tooNew(current, known int) error {
	return fmt.Errorf("%w: the database is at version %d, this binary knows up to %d", ErrSchemaTooNew, current, known)
}
