// Package postgres is a [memory.Store] in the Postgres store's database
// (package store/postgres): the memory_files table its migration 0019
// creates, on the store's connection pool. It is the backend for replicas:
// every process sharing the database shares the memories.
//
// Each command is one transaction that first takes a transaction advisory
// lock on its scope (tenant and user), so its checks (limits, what exists)
// and its writes see one state of the scope even with other replicas
// writing. Views read in one REPEATABLE READ transaction, so a listing is one
// state of the scope.
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

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/fileops"
	storepg "github.com/bsamanvith/agentkit/pkg/store/postgres"
)

var _ memory.Store = (*Store)(nil)

// Store is a memory.Store in a Postgres store's database. It is safe for
// concurrent use.
type Store struct {
	cfg   memory.Config
	pool  *pgxpool.Pool
	close func()
}

// Open connects to the Postgres store's database at url as [storepg.Open]
// does with opts (migrating its schema by default, or with
// storepg.WithMigrate(false) only checking it), and returns the memory store
// in it, or fails with memory.ErrInvalid for a bad cfg. Close closes the
// connections.
func Open(ctx context.Context, url string, cfg memory.Config, opts ...storepg.Option) (*Store, error) {
	if _, err := cfg.Resolve(); err != nil {
		return nil, err
	}
	st, err := storepg.Open(ctx, url, opts...)
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	s, err := New(st, cfg)
	if err != nil {
		st.Close()
		return nil, err
	}
	s.close = st.Close
	return s, nil
}

// New returns the memory store in st's database, on st's pool, or fails with
// memory.ErrInvalid for a bad cfg; st stays the caller's to close.
func New(st *storepg.Store, cfg memory.Config) (*Store, error) {
	cfg, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	return &Store{cfg: cfg, pool: st.Pool(), close: func() {}}, nil
}

// Close closes the connections if [Open] opened them.
func (s *Store) Close() error {
	s.close()
	return nil
}

// files is one scope, in one transaction, as fileops.Files.
type files struct {
	ctx   context.Context
	tx    pgx.Tx
	owner memory.Owner
	now   int64
}

func (f files) Get(path string) (string, bool, error) {
	var text string
	err := f.tx.QueryRow(f.ctx,
		`SELECT content FROM memory_files WHERE tenant = $1 AND user_id = $2 AND path = $3`,
		f.owner.Tenant, f.owner.User, path).Scan(&text)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return text, err == nil, err
}

func (f files) Under(dir string) ([]fileops.File, error) {
	// Every path under dir sorts in [dir/, dir0): '0' follows '/', and the
	// column's collation is "C", byte order.
	rows, err := f.tx.Query(f.ctx,
		`SELECT path, size FROM memory_files WHERE tenant = $1 AND user_id = $2 AND path >= $3 AND path < $4 ORDER BY path`,
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
	err := f.tx.QueryRow(f.ctx,
		`SELECT COALESCE(SUM(size), 0)::BIGINT, COUNT(*) FROM memory_files WHERE tenant = $1 AND user_id = $2`,
		f.owner.Tenant, f.owner.User).Scan(&bytes, &n)
	return bytes, n, err
}

func (f files) Put(path, text string) error {
	_, err := f.tx.Exec(f.ctx,
		`INSERT INTO memory_files (tenant, user_id, path, content, size, updated) VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (tenant, user_id, path) DO UPDATE SET content = excluded.content, size = excluded.size, updated = excluded.updated`,
		f.owner.Tenant, f.owner.User, path, text, len(text), f.now)
	return err
}

func (f files) Remove(path string) error {
	_, err := f.tx.Exec(f.ctx,
		`DELETE FROM memory_files WHERE tenant = $1 AND user_id = $2 AND path = $3`,
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

// write runs fn in one transaction holding the scope's lock, committed only
// when fn succeeds.
func (s *Store) write(ctx context.Context, paths []string, fn func(files, []string) error) error {
	owner, clean, err := s.prepare(ctx, paths)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memory/postgres: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('agentkit.memory'), hashtext($1 || '/' || $2))`,
		owner.Tenant, owner.User); err != nil {
		return fmt.Errorf("memory/postgres: lock: %w", err)
	}
	if err := fn(files{ctx: ctx, tx: tx, owner: owner, now: time.Now().UnixNano()}, clean); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("memory/postgres: %w", err)
	}
	return nil
}

// View implements memory.Store.
func (s *Store) View(ctx context.Context, path string) (memory.View, error) {
	owner, clean, err := s.prepare(ctx, []string{path})
	if err != nil {
		return memory.View{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return memory.View{}, fmt.Errorf("memory/postgres: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	return fileops.View(files{ctx: ctx, tx: tx, owner: owner}, clean[0])
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
