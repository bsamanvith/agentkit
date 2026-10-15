// Package postgres is the multi-node store: a [store.ConversationStore],
// [store.ConversationIndex], [store.RunStore], [store.EventLog] and
// [flow.Checkpointer] in one value over a Postgres database, and a
// [store.Admin] view of it as a separate value ([Admin]). It passes the same conformance suites as packages memory
// and sqlite, and its schema is SQLite's, ported column for column.
//
// The driver is pgx, through a pgxpool connection pool. Many processes may use
// the same database at once; the store's rules (one entry or event per
// position, one unfinished run per conversation, one lease holder) are kept by
// primary keys, a partial unique index and conditional updates, never by a
// check in Go that another process could race. A unique violation (SQLSTATE
// 23505) becomes the store's conflict errors. A serialization failure or
// deadlock (40001, 40P01) is retried a few times inside the store, the whole
// statement or transaction again, so callers never see one.
//
// The schema is created and upgraded by numbered migrations embedded in the
// binary (migrations/NNNN_name.sql), each applied in its own transaction and
// recorded in schema_migrations, one process at a time under an advisory
// lock. [Open] migrates by default; with WithMigrate(false) it only checks the
// schema and refuses one that is older ([ErrSchemaTooOld]), for deployments
// that migrate as a separate job ([Migrate]). Every binary refuses a schema
// newer than it knows ([ErrSchemaTooNew]). The tables live in the connection's
// current schema (the first one on its search_path), so a database can hold
// several stores side by side.
//
// Every row has a tenant column, part of every primary key, and every query
// filters by it except the one in [Admin]. Tenants and other ids are TEXT with
// the "C" collation, so they compare as bytes, as in SQLite and in Go, and
// cursors mean the same in every backend. Times are BIGINT Unix nanoseconds
// (NULL for the zero time), read from the store's injected clock and never
// from the database's: a time outside 1678 to 2262 is refused as
// [store.ErrInvalid] where a caller passes one. Messages, records, event data
// and checkpoints are TEXT, not JSONB, and are kept byte for byte.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

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
	_ store.ConversationIndex = (*Store)(nil)
	_ flow.Checkpointer       = (*Store)(nil)
	_ store.Admin             = admin{}
)

// Option configures a [Store].
type Option func(*options)

type options struct {
	clk     clock.Clock
	migrate bool
	// migrations limits the migrations applied or expected, for tests of
	// upgrades; 0 means all of them.
	migrations int
}

// WithClock sets the clock that entry, event and run times and lease expiry
// are read from. The default is [clock.Real].
func WithClock(c clock.Clock) Option {
	return func(o *options) { o.clk = c }
}

// WithMigrate sets whether [Open] applies the migrations the database lacks
// (the default) or only checks that it has them all, failing with
// [ErrSchemaTooOld] otherwise.
func WithMigrate(on bool) Option {
	return func(o *options) { o.migrate = on }
}

// Store keeps conversations, runs, event logs and checkpoints in a Postgres
// database, per tenant. It is safe for concurrent use, and other Stores, in
// this process or others, may use the same database at the same time. Make
// one with [Open] and [Store.Close] it when done.
type Store struct {
	clk  clock.Clock
	pool *pgxpool.Pool
}

// Open connects to the database at url (a postgres:// URL or a key=value
// connection string, as pgx reads them) and brings its schema up to date, or
// with WithMigrate(false) checks it. It fails with [ErrSchemaTooNew] if the
// schema was written by a newer binary.
func Open(ctx context.Context, url string, opts ...Option) (*Store, error) {
	o := options{clk: clock.Real(), migrate: true}
	for _, opt := range opts {
		opt(&o)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	if o.migrate {
		err = migrate(ctx, pool, o.clk, o.migrations)
	} else {
		err = checkSchema(ctx, pool, o.migrations)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return &Store{clk: o.clk, pool: pool}, nil
}

// Migrate brings the schema of the database at url up to date and returns:
// the one-off job for deployments that open their stores with
// WithMigrate(false).
func Migrate(ctx context.Context, url string, opts ...Option) error {
	s, err := Open(ctx, url, append(opts, WithMigrate(true))...)
	if err != nil {
		return err
	}
	s.Close()
	return nil
}

// Close closes the store's connections, waiting for those in use to be
// returned. Calls after Close fail.
func (s *Store) Close() { s.pool.Close() }

// scope checks ctx and returns its tenant's id.
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

// now reads the store's clock without its monotonic reading.
func (s *Store) now() time.Time { return s.clk.Now().Round(0) }

// maxAttempts bounds how often a write runs when the database asks for it to
// be tried again (a serialization failure or deadlock) or another writer took
// a position between its read and its insert.
const maxAttempts = 8

// errRetry is returned by a write that found its reads overtaken by another
// writer and must run again from the start.
var errRetry = errors.New("overtaken by another writer")

// retry runs fn until it returns an error that is not worth trying again, at
// most maxAttempts times, with a short growing pause in between. fn runs a
// whole statement or transaction, so trying again is always safe.
func retry(ctx context.Context, fn func() error) error {
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !retryable(err) {
			return err
		}
		if attempt == maxAttempts {
			return fmt.Errorf("gave up after %d attempts: %w", attempt, err)
		}
		pause := time.Duration(attempt) * time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
	}
}

// retryable reports whether err asks for the whole write to run again.
func retryable(err error) bool {
	switch code, _ := sqlState(err); code {
	case serializationFailure, deadlockDetected:
		return true
	}
	return errors.Is(err, errRetry)
}

// The SQLSTATEs the store acts on.
const (
	uniqueViolation       = "23505"
	serializationFailure  = "40001"
	deadlockDetected      = "40P01"
	characterNotInCharset = "22021" // invalid byte sequence (a NUL, bad UTF-8)
	untranslatableChar    = "22P05"
)

// sqlState returns err's SQLSTATE and the constraint it names, if err came
// from the server.
func sqlState(err error) (code, constraint string) {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code, pe.ConstraintName
	}
	return "", ""
}

// fail wraps an error of op. Text the server cannot hold is [store.ErrInvalid]
// (the backends' own check refuses it first; this covers values that check
// does not see, such as a checkpoint's body).
func fail(op string, err error) error {
	switch code, _ := sqlState(err); code {
	case characterNotInCharset, untranslatableChar:
		return fmt.Errorf("%w: postgres: %s: %w", store.ErrInvalid, op, err)
	}
	return fmt.Errorf("postgres: %s: %w", op, err)
}

// storeTime encodes t for a column: nil for the zero time, else Unix
// nanoseconds. A time outside [backend.MinTime, backend.MaxTime] is
// [store.ErrInvalid].
func storeTime(t time.Time) (*int64, error) {
	if t.IsZero() {
		return nil, nil
	}
	if err := backend.CheckTime("time", t); err != nil {
		return nil, err
	}
	n := t.UnixNano()
	return &n, nil
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
func loadTime(n *int64) time.Time {
	if n == nil {
		return time.Time{}
	}
	return time.Unix(0, *n).UTC()
}

// querier is what *pgxpool.Pool and pgx.Tx share.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
