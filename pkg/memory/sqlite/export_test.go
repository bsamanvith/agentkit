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

// SetVectorBlob overwrites the stored vector of the item id of any scope,
// for tests of corrupt files.
func SetVectorBlob(ctx context.Context, x *Index, id string, dim int, blob []byte) error {
	_, err := x.w.ExecContext(ctx, `UPDATE memory_vectors SET dim = ?, vec = ?
		WHERE seq = (SELECT seq FROM memory_items WHERE id = ?)`, dim, blob, id)
	return err
}

// ExecIndex runs statements on the index's file as another writer would
// (one that knows nothing of vectors, such as a version 1 binary).
func ExecIndex(ctx context.Context, x *Index, stmt string, args ...any) error {
	_, err := x.w.ExecContext(ctx, stmt, args...)
	return err
}

// CountVectors is how many vectors the file holds, in every scope.
func CountVectors(ctx context.Context, x *Index) (int, error) {
	var n int
	err := x.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_vectors`).Scan(&n)
	return n, err
}
