package postgres

import "github.com/jackc/pgx/v5/pgxpool"

// WithMigrations makes Open apply (or expect) only the first n migrations, as
// a binary from before the later ones would: for tests of upgrades and of a
// newer schema.
func WithMigrations(n int) Option {
	return func(o *options) { o.migrations = n }
}

// Latest is the version of the newest embedded migration: the one a schema
// this binary migrates ends at.
func Latest() int {
	ms, err := migrations(0)
	if err != nil {
		panic(err)
	}
	return len(ms)
}

// Pool is the store's connection pool, for tests that plant queries the
// store would never send.
func Pool(s *Store) *pgxpool.Pool { return s.pool }
