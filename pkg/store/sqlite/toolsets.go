package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

const toolsetColumns = `name, data, version, created_at, updated_at`

// PutToolset implements [store.ToolsetStore].
//
// One transaction, which holds the write lock from its start: read the
// stored version, compare it, then INSERT or UPDATE.
func (s *Store) PutToolset(ctx context.Context, t store.ToolsetRecord, ifVersion int64) (store.ToolsetRecord, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.ToolsetRecord{}, err
	}
	t, err = backend.CheckToolset(t, ifVersion)
	if err != nil {
		return store.ToolsetRecord{}, err
	}
	now := s.now()
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.ToolsetRecord{}, err
	}
	err = s.write(ctx, func(tx *txn) error {
		old, found, err := getToolset(ctx, tx, tenant, t.Name)
		if err != nil {
			return fmt.Errorf("sqlite: put toolset: %w", err)
		}
		if err := backend.PutToolsetError(t.Name, ifVersion, old.Version); err != nil {
			return err
		}
		t.Version, t.CreatedAt, t.UpdatedAt = old.Version+1, now, now
		if !found {
			_, err = tx.ExecContext(ctx, `INSERT INTO toolsets (tenant, name, data, version, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)`, tenant, t.Name, string(t.Data), t.Version, now.UnixNano(), now.UnixNano())
		} else {
			t.CreatedAt = old.CreatedAt
			if now.Before(old.UpdatedAt) {
				t.UpdatedAt = old.UpdatedAt // times never go backwards
			}
			_, err = tx.ExecContext(ctx, `UPDATE toolsets SET data = ?, version = ?, updated_at = ?
				WHERE tenant = ? AND name = ?`, string(t.Data), t.Version, t.UpdatedAt.UnixNano(), tenant, t.Name)
		}
		if err != nil {
			return fmt.Errorf("sqlite: put toolset: %w", err)
		}
		return nil
	})
	if err != nil {
		return store.ToolsetRecord{}, err
	}
	t.CreatedAt, t.UpdatedAt = t.CreatedAt.UTC(), t.UpdatedAt.UTC()
	return t, nil
}

// GetToolset implements [store.ToolsetStore].
func (s *Store) GetToolset(ctx context.Context, name string) (store.ToolsetRecord, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.ToolsetRecord{}, err
	}
	t, found, err := getToolset(ctx, s.r, tenant, name)
	if err != nil {
		return store.ToolsetRecord{}, fmt.Errorf("sqlite: get toolset: %w", err)
	}
	if !found {
		return store.ToolsetRecord{}, fmt.Errorf("%w: toolset %q", store.ErrNotFound, name)
	}
	return t, nil
}

// ListToolsets implements [store.ToolsetStore].
func (s *Store) ListToolsets(ctx context.Context) ([]store.ToolsetRecord, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	out, err := queryToolsets(ctx, s.r, `WHERE tenant = ? ORDER BY name`, tenant)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list toolsets: %w", err)
	}
	return out, nil
}

// DeleteToolset implements [store.ToolsetStore].
func (s *Store) DeleteToolset(ctx context.Context, name string, ifVersion int64) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	if err := backend.CheckDeleteToolset(name, ifVersion); err != nil {
		return err
	}
	return s.write(ctx, func(tx *txn) error {
		old, _, err := getToolset(ctx, tx, tenant, name)
		if err != nil {
			return fmt.Errorf("sqlite: delete toolset: %w", err)
		}
		if err := backend.DeleteToolsetError(name, ifVersion, old.Version); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM toolsets WHERE tenant = ? AND name = ?`, tenant, name); err != nil {
			return fmt.Errorf("sqlite: delete toolset: %w", err)
		}
		return nil
	})
}

// getToolset reads one toolset; found is false when there is none.
func getToolset(ctx context.Context, q querier, tenant, name string) (t store.ToolsetRecord, found bool, err error) {
	ts, err := queryToolsets(ctx, q, `WHERE tenant = ? AND name = ?`, tenant, name)
	if err != nil || len(ts) == 0 {
		return store.ToolsetRecord{}, false, err
	}
	return ts[0], true, nil
}

// queryToolsets reads the toolsets a WHERE clause (and what follows it)
// selects. It returns an empty slice, not nil, for none.
func queryToolsets(ctx context.Context, q querier, where string, args ...any) ([]store.ToolsetRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+toolsetColumns+` FROM toolsets `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.ToolsetRecord{}
	for rows.Next() {
		var t store.ToolsetRecord
		var data string
		var created, updated sql.NullInt64
		if err := rows.Scan(&t.Name, &data, &t.Version, &created, &updated); err != nil {
			return nil, err
		}
		t.Data = []byte(data)
		t.CreatedAt, t.UpdatedAt = loadTime(created), loadTime(updated)
		out = append(out, t)
	}
	return out, rows.Err()
}
