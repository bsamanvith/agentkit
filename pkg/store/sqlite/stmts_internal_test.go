package sqlite

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

// A write transaction that runs a query again while rows of the same text
// are still open reads every row once, whether the text has a kept statement
// yet (the first transaction) or not: the second run must not reset the
// kept statement under the open rows, which would start the outer loop over.
func TestTxnSameQueryWhileItsRowsAreOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "agentkit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, q := range []string{`CREATE TABLE nested (x INTEGER)`, `INSERT INTO nested VALUES (1), (2), (3)`} {
		if _, err := s.w.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	const q = `SELECT x FROM nested ORDER BY x`
	for round := range 3 { // unkept, then kept
		var outer, inner []int
		err := s.write(ctx, func(tx *txn) error {
			rows, err := tx.QueryContext(ctx, q)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() && len(outer) < 10 {
				var x, y int
				if err := rows.Scan(&x); err != nil {
					return err
				}
				outer = append(outer, x)
				if err := tx.QueryRowContext(ctx, q).Scan(&y); err != nil {
					return err
				}
				inner = append(inner, y)
				if _, err := tx.ExecContext(ctx, q); err != nil {
					return err
				}
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if want := []int{1, 2, 3}; !reflect.DeepEqual(outer, want) || !reflect.DeepEqual(inner, []int{1, 1, 1}) {
			t.Fatalf("round %d: outer rows %v, inner %v; want %v, [1 1 1]", round, outer, inner, want)
		}
		if round == 0 && s.w.kept(q) == nil {
			t.Fatal("the query was not kept after the first transaction")
		}
	}
}
