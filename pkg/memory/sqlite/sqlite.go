// Package sqlite is a [memory.Store] in a SQLite file, for agents whose
// memories must survive a restart on one node. Several processes may open
// the same file.
//
// The file holds one table, memory_files, keyed by tenant, user and path. Each
// command is one IMMEDIATE transaction on a single writer connection, so a
// command's checks (limits, what exists) and its writes see the same state
// even with other processes writing. Views read through a pool of read-only
// connections (WAL mode). The schema version is PRAGMA user_version; a file
// whose schema is newer than this package knows is refused (ErrSchemaTooNew).
//
// The driver is modernc.org/sqlite (pure Go), the one the store's SQLite
// backend uses, so it adds nothing to a binary that has that backend.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"time"

	_ "modernc.org/sqlite" // the database/sql driver "sqlite"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/fileops"
)

var _ memory.Store = (*Store)(nil)

// ErrSchemaTooNew is a file written by a newer version of this package.
var ErrSchemaTooNew = errors.New("memory/sqlite: database schema is newer than this binary")

// schemaVersion is the schema this package writes.
const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS memory_files (
	tenant   TEXT    NOT NULL,
	user_id  TEXT    NOT NULL,
	path     TEXT    NOT NULL,
	content  TEXT    NOT NULL,
	size     INTEGER NOT NULL,
	updated  INTEGER NOT NULL,
	PRIMARY KEY (tenant, user_id, path)
) WITHOUT ROWID;
PRAGMA user_version = 1;
`

const busyTimeout = 30 * time.Second

// Store is a memory.Store over one SQLite file. Close it when done.
type Store struct {
	cfg memory.Config
	w   *sql.DB // one connection; every command's transaction
	r   *sql.DB // read-only connections, for View
}

// Open opens (creating it if needed) the file at path, or fails with
// memory.ErrInvalid for a bad cfg.
func Open(ctx context.Context, path string, cfg memory.Config) (*Store, error) {
	cfg, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	w, err := sql.Open("sqlite", dsn(abs, false))
	if err != nil {
		return nil, fmt.Errorf("memory/sqlite: open %s: %w", path, err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)
	if err := migrate(ctx, w); err != nil {
		return nil, errors.Join(fmt.Errorf("memory/sqlite: open %s: %w", path, err), w.Close())
	}
	r, err := sql.Open("sqlite", dsn(abs, true))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("memory/sqlite: open %s: %w", path, err), w.Close())
	}
	n := max(2, runtime.GOMAXPROCS(0))
	r.SetMaxOpenConns(n)
	r.SetMaxIdleConns(n)
	return &Store{cfg: cfg, w: w, r: r}, nil
}

func dsn(abs string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_txlock", "immediate")
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}
	return u.String()
}

// migrate creates the schema, or checks the one there.
func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var v int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	switch {
	case v > schemaVersion:
		return fmt.Errorf("%w: version %d, this binary knows %d", ErrSchemaTooNew, v, schemaVersion)
	case v == schemaVersion:
		return nil
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes the file.
func (s *Store) Close() error { return errors.Join(s.r.Close(), s.w.Close()) }

// q is what files runs its statements on: a transaction, or the read pool.
type q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// files is one scope as fileops.Files.
type files struct {
	ctx   context.Context
	db    q
	owner memory.Owner
	now   int64
}

func (f files) Get(path string) (string, bool, error) {
	var text string
	err := f.db.QueryRowContext(f.ctx,
		`SELECT content FROM memory_files WHERE tenant = ? AND user_id = ? AND path = ?`,
		f.owner.Tenant, f.owner.User, path).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return text, err == nil, err
}

func (f files) Under(dir string) ([]fileops.File, error) {
	// Every path under dir sorts in [dir/, dir0): '0' follows '/'.
	rows, err := f.db.QueryContext(f.ctx,
		`SELECT path, size FROM memory_files WHERE tenant = ? AND user_id = ? AND path >= ? AND path < ? ORDER BY path`,
		f.owner.Tenant, f.owner.User, dir+"/", dir+"0")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fileops.File
	for rows.Next() {
		var file fileops.File
		if err := rows.Scan(&file.Path, &file.Size); err != nil {
			return nil, err
		}
		out = append(out, file)
	}
	return out, rows.Err()
}

func (f files) Usage() (int64, int, error) {
	var bytes int64
	var n int
	err := f.db.QueryRowContext(f.ctx,
		`SELECT COALESCE(SUM(size), 0), COUNT(*) FROM memory_files WHERE tenant = ? AND user_id = ?`,
		f.owner.Tenant, f.owner.User).Scan(&bytes, &n)
	return bytes, n, err
}

func (f files) Put(path, text string) error {
	_, err := f.db.ExecContext(f.ctx,
		`INSERT INTO memory_files (tenant, user_id, path, content, size, updated) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (tenant, user_id, path) DO UPDATE SET content = excluded.content, size = excluded.size, updated = excluded.updated`,
		f.owner.Tenant, f.owner.User, path, text, len(text), f.now)
	return err
}

func (f files) Remove(path string) error {
	_, err := f.db.ExecContext(f.ctx,
		`DELETE FROM memory_files WHERE tenant = ? AND user_id = ? AND path = ?`,
		f.owner.Tenant, f.owner.User, path)
	return err
}

// prepare resolves the owner of ctx and cleans paths.
func (s *Store) prepare(ctx context.Context, paths []string) (memory.Owner, []string, error) {
	if err := ctx.Err(); err != nil {
		return memory.Owner{}, nil, err
	}
	owner, err := memory.OwnerOf(ctx, s.cfg.Scope)
	if err != nil {
		return memory.Owner{}, nil, err
	}
	clean := make([]string, len(paths))
	for i, p := range paths {
		if clean[i], err = memory.CleanPath(p); err != nil {
			return memory.Owner{}, nil, err
		}
	}
	return owner, clean, nil
}

// write runs fn in one transaction, committed only when fn succeeds.
func (s *Store) write(ctx context.Context, paths []string, fn func(files, []string) error) error {
	owner, clean, err := s.prepare(ctx, paths)
	if err != nil {
		return err
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(files{ctx: ctx, db: tx, owner: owner, now: time.Now().UnixNano()}, clean); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	return nil
}

// View implements memory.Store. It reads in one transaction, so a listing is
// one state of the scope.
func (s *Store) View(ctx context.Context, path string) (memory.View, error) {
	owner, clean, err := s.prepare(ctx, []string{path})
	if err != nil {
		return memory.View{}, err
	}
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return memory.View{}, fmt.Errorf("memory/sqlite: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fileops.View(files{ctx: ctx, db: tx, owner: owner}, clean[0])
}

// Create implements memory.Store.
func (s *Store) Create(ctx context.Context, path, text string) error {
	return s.write(ctx, []string{path}, func(f files, p []string) error {
		return fileops.Create(f, s.cfg.Limits, p[0], text)
	})
}

// StrReplace implements memory.Store.
func (s *Store) StrReplace(ctx context.Context, path, old, new string) error {
	return s.write(ctx, []string{path}, func(f files, p []string) error {
		return fileops.StrReplace(f, s.cfg.Limits, p[0], old, new)
	})
}

// Insert implements memory.Store.
func (s *Store) Insert(ctx context.Context, path string, line int, text string) error {
	return s.write(ctx, []string{path}, func(f files, p []string) error {
		return fileops.Insert(f, s.cfg.Limits, p[0], line, text)
	})
}

// Delete implements memory.Store.
func (s *Store) Delete(ctx context.Context, path string) error {
	return s.write(ctx, []string{path}, func(f files, p []string) error {
		return fileops.Delete(f, p[0])
	})
}

// Rename implements memory.Store.
func (s *Store) Rename(ctx context.Context, from, to string) error {
	return s.write(ctx, []string{from, to}, func(f files, p []string) error {
		return fileops.Rename(f, p[0], p[1])
	})
}
