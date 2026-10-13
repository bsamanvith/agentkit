package sqlite

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
