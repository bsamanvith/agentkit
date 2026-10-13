// Package sqlite is the local and single-node store: a
// [store.ConversationStore], [store.RunStore], [store.EventLog] and
// [flow.Checkpointer] in one value over one SQLite file, and a [store.Admin]
// view of it as a separate value ([Admin]). It passes the same conformance
// suites as package memory.
//
// The driver is modernc.org/sqlite, pure Go, so binaries stay static. The file
// is opened in WAL mode with a busy timeout, through two connection pools: one
// writer connection, whose transactions begin IMMEDIATE (they take the write
// lock up front, so a read-then-write never fails half way), and a pool of
// read-only connections. Several processes may open the same file; their
// writes are serialized by SQLite's lock, and the store's rules (one entry per
// position, one unfinished run per conversation, one lease holder) are kept by
// primary keys, a partial unique index and conditional updates, never by a
// check in Go that another process could race.
//
// The schema is created and upgraded at [Open] by numbered migrations embedded
// in the binary (migrations/NNNN_name.sql), each applied in its own
// transaction and recorded in schema_migrations. A binary refuses a file whose
// schema is newer than it knows ([ErrSchemaTooNew]).
//
// Every row has a tenant column, part of every primary key, and every query
// filters by it except the one in [Admin]. Tenant and other ids compare as
// bytes. Times are stored as Unix nanoseconds (NULL for the zero time), so
// they round-trip exactly; a time this encoding cannot hold (before 1678 or
// after 2262) is refused as [store.ErrInvalid] where a caller passes one.
// Messages, records and checkpoints are stored as JSON text, with entries
// kept byte for byte as Append accepted them. The column types are the ones
// Postgres has (TEXT, INTEGER as BIGINT), so the schema ports column for
// column.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	sqlite3 "modernc.org/sqlite" // and the database/sql driver "sqlite"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

var (
	_ store.ConversationStore = (*Store)(nil)
	_ store.RunStore          = (*Store)(nil)
	_ store.EventLog          = (*Store)(nil)
	_ store.CommandLog        = (*Store)(nil)
	_ flow.Checkpointer       = (*Store)(nil)
	_ store.Admin             = admin{}
)

// busyTimeout is how long a statement waits for another connection's or
// process's lock before failing with SQLITE_BUSY.
const busyTimeout = 30 * time.Second

// Option configures a [Store].
type Option func(*options)

type options struct {
	clk     clock.Clock
	readers int
	// migrations limits the migrations applied, for tests of upgrades; 0
	// means all of them.
	migrations int
}

// WithClock sets the clock that entry, event and run times and lease expiry
// are read from. The default is [clock.Real].
func WithClock(c clock.Clock) Option {
	return func(o *options) { o.clk = c }
}

// WithReaders sets the most read connections open at once. The default is
// GOMAXPROCS, and at least 2.
func WithReaders(n int) Option {
	return func(o *options) { o.readers = n }
}

// Store keeps conversations, runs, event logs and checkpoints in one SQLite
// file, per tenant. It is safe for concurrent use, and other Stores, in this process or
// others, may use the same file at the same time. Make one with [Open] and
// [Store.Close] it when done.
type Store struct {
	clk clock.Clock
	w   *sql.DB // one connection; every write goes through it
	r   *sql.DB // read-only connections
}

// Open opens the database file at path, creating it if it does not exist, and
// brings its schema up to date. It fails with [ErrSchemaTooNew] if the file was
// written by a newer binary.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	o := options{clk: clock.Real(), readers: max(2, runtime.GOMAXPROCS(0))}
	for _, opt := range opts {
		opt(&o)
	}
	if o.readers < 1 {
		return nil, fmt.Errorf("%w: %d read connections", store.ErrInvalid, o.readers)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}

	w, err := sql.Open("sqlite", dsn(abs, false))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)
	if err := enableWAL(ctx, w); err != nil {
		return nil, errors.Join(fmt.Errorf("sqlite: open %s: %w", path, err), w.Close())
	}
	if err := migrate(ctx, w, o.clk, o.migrations); err != nil {
		return nil, errors.Join(fmt.Errorf("sqlite: open %s: %w", path, err), w.Close())
	}

	r, err := sql.Open("sqlite", dsn(abs, true))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("sqlite: open %s: %w", path, err), w.Close())
	}
	r.SetMaxOpenConns(o.readers)
	r.SetMaxIdleConns(o.readers)
	r.SetConnMaxLifetime(0)
	r.SetConnMaxIdleTime(0)
	return &Store{clk: o.clk, w: w, r: r}, nil
}

// dsn is the data source name of the file at abs. Every connection of a pool
// is set up the same way, since the settings are in the name.
func dsn(abs string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	q.Add("_pragma", "foreign_keys(1)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_txlock", "immediate")
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}
	return u.String()
}

// enableWAL switches the file to WAL mode, which is persistent: readers and
// later opens find it so. Switching takes an exclusive lock, and SQLite
// reports a busy file at once there instead of waiting (processes opening a
// new file together), so this waits itself, up to the busy timeout.
func enableWAL(ctx context.Context, db *sql.DB) error {
	deadline := time.Now().Add(busyTimeout)
	for wait := time.Millisecond; ; wait = min(2*wait, 100*time.Millisecond) {
		var mode string
		err := db.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&mode)
		switch {
		case err == nil && strings.EqualFold(mode, "wal"):
			return nil
		case err == nil:
			return fmt.Errorf("journal mode is %q, want wal", mode)
		case !busy(err) || time.Now().After(deadline):
			return fmt.Errorf("journal mode: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// busy reports whether err is SQLITE_BUSY (any extended code).
func busy(err error) bool {
	var e *sqlite3.Error
	return errors.As(err, &e) && e.Code()&0xff == sqliteBusy
}

// sqliteBusy is SQLITE_BUSY's primary result code.
const sqliteBusy = 5

// Close closes the store's connections. Calls after Close fail.
func (s *Store) Close() error {
	return errors.Join(s.r.Close(), s.w.Close())
}

// scope checks ctx and returns the id of its tenant.
func scope(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	t, err := tenancy.Require(ctx)
	if err != nil {
		return "", err
	}
	return t.ID(), nil
}

// write runs fn in a transaction on the writer connection. The transaction
// begins IMMEDIATE (see dsn), so it holds the database's write lock from the
// start. fn must not use s.w itself: its one connection is taken.
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	// If fn panics, roll back on the way out: an open transaction would
	// keep the writer's one connection, and every later write would wait
	// for it. After a commit or a rollback this is a no-op (ErrTxDone).
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return errors.Join(err, ignoreDone(tx.Rollback()))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit: %w", err)
	}
	return nil
}

// ignoreDone drops the error of rolling back a transaction that database/sql
// already rolled back because its context ended.
func ignoreDone(err error) error {
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

// now reads the store's clock without its monotonic reading.
func (s *Store) now() time.Time { return s.clk.Now().Round(0) }

// storeTime encodes t for a column: nil for the zero time, else Unix
// nanoseconds. A time outside [backend.MinTime, backend.MaxTime] is
// [store.ErrInvalid].
func storeTime(t time.Time) (any, error) {
	if t.IsZero() {
		return nil, nil
	}
	if err := backend.CheckTime("time", t); err != nil {
		return nil, err
	}
	return t.UnixNano(), nil
}

// argTime encodes a time a query compares stored times with. One outside the
// stored range is clamped just past it, which compares the same.
func argTime(t time.Time) int64 {
	switch {
	case t.Before(backend.MinTime):
		return math.MinInt64
	case t.After(backend.MaxTime):
		return math.MaxInt64
	}
	return t.UnixNano()
}

// loadTime decodes a time column.
func loadTime(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return time.Unix(0, n.Int64).UTC()
}
