package sqlite

import (
	"context"
	"database/sql"
)

// WithMigrations makes Open apply only the first n migrations, as a binary
// from before the later ones would: for tests of upgrades and of a newer file.
func WithMigrations(n int) Option {
	return func(o *options) { o.migrations = n }
}

// Exec runs a statement on s's file, as an older binary would have written
// it: for tests of what a migration does to rows it finds.
func Exec(s *Store, query string, args ...any) error {
	_, err := s.w.Exec(query, args...)
	return err
}

// Write runs fn in a write transaction of s, as the store's own methods do:
// for tests of how a failing fn leaves the writer.
func Write(ctx context.Context, s *Store, fn func(*sql.Tx) error) error {
	return s.write(ctx, func(t *txn) error { return fn(t.Tx) })
}
