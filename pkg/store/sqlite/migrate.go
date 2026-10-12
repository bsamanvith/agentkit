package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// ErrSchemaTooNew means the database file has migrations this binary does not
// know: it was written by a newer version. Upgrade the binary; never open the
// file with this one.
var ErrSchemaTooNew = errors.New("database schema is newer than this binary")

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
// numbered densely from 1.
func migrations() ([]migration, error) {
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
	return ms, nil
}

// migrate brings the schema of db up to date, applying up to limit migrations
// (all of them when limit is 0). Each runs in its own IMMEDIATE transaction
// and is recorded in schema_migrations; another process migrating the same
// file at the same time waits for the lock and then finds the work done.
func migrate(ctx context.Context, db *sql.DB, clk clock.Clock, limit int) error {
	ms, err := migrations()
	if err != nil {
		return err
	}
	if limit > 0 {
		ms = ms[:min(limit, len(ms))]
	}
	for _, m := range ms {
		if err := apply(ctx, db, clk, m, len(ms)); err != nil {
			return err
		}
	}
	// Checked once more when nothing was left to apply.
	return checkVersion(ctx, db, len(ms))
}

// apply runs m unless the file already has it.
func apply(ctx context.Context, db *sql.DB, clk clock.Clock, m migration, known int) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, ignoreDone(tx.Rollback()))
		}
	}()
	current, err := version(ctx, tx)
	if err != nil {
		return err
	}
	if current > known {
		return tooNew(current, known)
	}
	if current >= m.version {
		return tx.Rollback()
	}
	if current != m.version-1 {
		return fmt.Errorf("schema is at version %d, cannot apply migration %d", current, m.version)
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %04d_%s: %w", m.version, m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, clk.Now().UnixNano()); err != nil {
		return fmt.Errorf("migration %04d_%s: %w", m.version, m.name, err)
	}
	return tx.Commit()
}

func checkVersion(ctx context.Context, db *sql.DB, known int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	current, err := version(ctx, tx)
	err = errors.Join(err, ignoreDone(tx.Rollback()))
	switch {
	case err != nil:
		return err
	case current > known:
		return tooNew(current, known)
	}
	return nil
}

// version returns the file's schema version, creating the table that records
// it if needed. tx is IMMEDIATE (db is the writer pool): a statement outside
// a transaction that reads and then writes would fail with SQLITE_BUSY at
// once, without waiting, when another process is migrating the same file.
func version(ctx context.Context, tx *sql.Tx) (int, error) {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER NOT NULL PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return 0, fmt.Errorf("schema_migrations: %w", err)
	}
	var v int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("schema version: %w", err)
	}
	return v, nil
}

func tooNew(current, known int) error {
	return fmt.Errorf("%w: the file is at version %d, this binary knows up to %d", ErrSchemaTooNew, current, known)
}
