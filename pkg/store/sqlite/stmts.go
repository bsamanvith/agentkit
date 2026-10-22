package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

// maxStmts bounds the statements a [stmtDB] keeps prepared. The store's
// queries are a fixed set of texts (a page query's text varies with the
// page's order and seek, never with its values), well under this; a query
// past it runs unprepared.
const maxStmts = 256

// stmtDB is a connection pool that prepares each query text once and keeps
// the statement, instead of parsing and planning the SQL again on every
// call: parsing was about half of what a read cost, and a third of the
// package's tests' time under the race detector (T056). database/sql
// prepares a kept statement on each connection the first time it runs
// there, and SQLite prepares it again by itself if the schema changed under
// it (another process migrating the file).
//
// A query's LIMIT is written CAST(? AS INTEGER), not a bare ?: SQLite plans
// with the value bound to a bare LIMIT parameter, so binding one makes the
// statement prepare itself again on every run, kept or not.
type stmtDB struct {
	*sql.DB

	mu    sync.Mutex
	stmts map[string]*sql.Stmt // nil once closed
}

func newStmtDB(db *sql.DB) *stmtDB { return &stmtDB{DB: db, stmts: map[string]*sql.Stmt{}} }

// kept returns the kept statement for query, or nil.
func (d *stmtDB) kept(query string) *sql.Stmt {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stmts[query]
}

// stmt returns the kept statement for query, preparing it if need be. It
// returns nil when the statement cannot be kept: the cache is full or
// closed, or the prepare failed (the caller then runs the query unprepared,
// which reports the same error). Preparing takes a connection of the pool,
// so stmt must not be called while the caller holds the last one.
func (d *stmtDB) stmt(ctx context.Context, query string) *sql.Stmt {
	if st := d.kept(query); st != nil {
		return st
	}
	st, err := d.PrepareContext(ctx, query)
	if err != nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch kept, ok := d.stmts[query]; {
	case ok: // prepared meanwhile by another call
		_ = st.Close()
		return kept
	case d.stmts == nil || len(d.stmts) >= maxStmts:
		_ = st.Close()
		return nil
	}
	d.stmts[query] = st
	return st
}

// QueryContext runs query as [sql.DB.QueryContext] does, through its kept
// statement.
func (d *stmtDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if st := d.stmt(ctx, query); st != nil {
		return st.QueryContext(ctx, args...)
	}
	return d.DB.QueryContext(ctx, query, args...)
}

// QueryRowContext runs query as [sql.DB.QueryRowContext] does, through its
// kept statement.
func (d *stmtDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if st := d.stmt(ctx, query); st != nil {
		return st.QueryRowContext(ctx, args...)
	}
	return d.DB.QueryRowContext(ctx, query, args...)
}

// Close closes the kept statements, then the pool. Queries after Close fail
// as the pool's do.
func (d *stmtDB) Close() error {
	d.mu.Lock()
	stmts := d.stmts
	d.stmts = nil
	d.mu.Unlock()
	var errs []error
	for _, st := range stmts {
		errs = append(errs, st.Close())
	}
	return errors.Join(append(errs, d.DB.Close())...)
}

// txn is a write transaction on the writer pool that runs each query through
// the pool's kept statement. The writer has one connection, which the
// transaction holds, so a query with no kept statement yet cannot be
// prepared for keeping while the transaction runs: it runs unprepared, and
// is prepared for keeping once the transaction is over ([txn.keep]).
type txn struct {
	*sql.Tx
	db     *stmtDB
	missed []string
}

// stmt returns the transaction's use of query's kept statement, or nil if
// there is none yet.
func (t *txn) stmt(ctx context.Context, query string) *sql.Stmt {
	if st := t.db.kept(query); st != nil {
		return t.StmtContext(ctx, st)
	}
	t.missed = append(t.missed, query)
	return nil
}

// keep prepares the queries the transaction ran with no kept statement, for
// the next transactions. It is called after the transaction ended, when the
// writer's connection is free again.
func (t *txn) keep(ctx context.Context) {
	for _, q := range t.missed {
		if ctx.Err() != nil {
			return
		}
		t.db.stmt(ctx, q)
	}
}

// ExecContext runs query as [sql.Tx.ExecContext] does.
func (t *txn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if st := t.stmt(ctx, query); st != nil {
		return st.ExecContext(ctx, args...)
	}
	return t.Tx.ExecContext(ctx, query, args...)
}

// QueryContext runs query as [sql.Tx.QueryContext] does.
func (t *txn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if st := t.stmt(ctx, query); st != nil {
		return st.QueryContext(ctx, args...)
	}
	return t.Tx.QueryContext(ctx, query, args...)
}

// QueryRowContext runs query as [sql.Tx.QueryRowContext] does.
func (t *txn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if st := t.stmt(ctx, query); st != nil {
		return st.QueryRowContext(ctx, args...)
	}
	return t.Tx.QueryRowContext(ctx, query, args...)
}

// PrepareContext returns a statement for query in the transaction, as
// [sql.Tx.PrepareContext] does; closing it leaves the kept one open.
func (t *txn) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	if st := t.stmt(ctx, query); st != nil {
		return st, nil
	}
	return t.Tx.PrepareContext(ctx, query)
}
