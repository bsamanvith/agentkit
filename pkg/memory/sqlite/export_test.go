package sqlite

import (
	"context"
	"fmt"
)

// SetSchemaVersion sets the file's schema version, for tests of upgrades.
func SetSchemaVersion(ctx context.Context, s *Store, v int) error {
	_, err := s.w.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v))
	return err
}

// SetIndexSchemaVersion sets the index's schema version, for tests of
// upgrades.
func SetIndexSchemaVersion(ctx context.Context, x *Index, v int) error {
	_, err := x.w.ExecContext(ctx, `UPDATE memory_index_meta SET value = ? WHERE key = 'schema'`, v)
	return err
}

// MakeIndexVersion1 turns the index into what T059 wrote: schema version 1,
// with no vectors table.
func MakeIndexVersion1(ctx context.Context, x *Index) error {
	_, err := x.w.ExecContext(ctx, `DROP TABLE memory_vectors; UPDATE memory_index_meta SET value = 1 WHERE key = 'schema'`)
	return err
}
