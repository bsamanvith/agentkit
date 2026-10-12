package sqlite

// WithMigrations makes Open apply only the first n migrations, as a binary
// from before the later ones would: for tests of upgrades and of a newer file.
func WithMigrations(n int) Option {
	return func(o *options) { o.migrations = n }
}
