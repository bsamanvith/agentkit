package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

const toolsetColumns = `name, data, version, created_at, updated_at`

// PutToolset implements [store.ToolsetStore].
//
// One transaction locks the toolset's row, compares its version, then
// updates it where the version is still the one read; a create inserts with ON CONFLICT DO NOTHING, so of two
// creates of one name (no row to lock yet) only one inserts.
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
	var out store.ToolsetRecord
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			old, found, err := getToolset(ctx, tx, tenant, t.Name, true)
			if err != nil {
				return fail("put toolset", err)
			}
			if err := backend.PutToolsetError(t.Name, ifVersion, old.Version); err != nil {
				return err
			}
			r := t
			r.Version, r.CreatedAt, r.UpdatedAt = old.Version+1, now.UTC(), now.UTC()
			if !found {
				tag, err := tx.Exec(ctx, `INSERT INTO toolsets (tenant, name, data, version, created_at, updated_at)
					VALUES ($1, $2, $3, $4, $5, $5) ON CONFLICT (tenant, name) DO NOTHING`,
					tenant, r.Name, string(r.Data), r.Version, now.UnixNano())
				if err != nil {
					return fail("put toolset", err)
				}
				if tag.RowsAffected() == 0 { // created since the read
					return fmt.Errorf("%w: toolset %q exists", store.ErrConflict, t.Name)
				}
				out = r
				return nil
			}
			r.CreatedAt = old.CreatedAt
			if now.Before(old.UpdatedAt) {
				r.UpdatedAt = old.UpdatedAt // times never go backwards
			}
			tag, err := tx.Exec(ctx, `UPDATE toolsets SET data = $1, version = $2, updated_at = $3
				WHERE tenant = $4 AND name = $5 AND version = $6`,
				string(r.Data), r.Version, r.UpdatedAt.UnixNano(), tenant, r.Name, old.Version)
			if err != nil {
				return fail("put toolset", err)
			}
			if tag.RowsAffected() == 0 { // changed since the read
				return errRetry
			}
			out = r
			return nil
		})
	})
	if err != nil {
		return store.ToolsetRecord{}, err
	}
	return out, nil
}

// GetToolset implements [store.ToolsetStore].
func (s *Store) GetToolset(ctx context.Context, name string) (store.ToolsetRecord, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.ToolsetRecord{}, err
	}
	t, found, err := getToolset(ctx, s.pool, tenant, name, false)
	if err != nil {
		return store.ToolsetRecord{}, fail("get toolset", err)
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
	out, err := queryToolsets(ctx, s.pool, `WHERE tenant = $1 ORDER BY name`, tenant)
	if err != nil {
		return nil, fail("list toolsets", err)
	}
	return out, nil
}

// DeleteToolset implements [store.ToolsetStore].
//
// One transaction locks the toolset's row, compares its version, then
// deletes it.
func (s *Store) DeleteToolset(ctx context.Context, name string, ifVersion int64) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	if err := backend.CheckDeleteToolset(name, ifVersion); err != nil {
		return err
	}
	return retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			old, _, err := getToolset(ctx, tx, tenant, name, true)
			if err != nil {
				return fail("delete toolset", err)
			}
			if err := backend.DeleteToolsetError(name, ifVersion, old.Version); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `DELETE FROM toolsets WHERE tenant = $1 AND name = $2 AND version = $3`,
				tenant, name, old.Version)
			if err != nil {
				return fail("delete toolset", err)
			}
			if tag.RowsAffected() == 0 { // changed since the read
				return errRetry
			}
			return nil
		})
	})
}

// getToolset reads one toolset, locking its row until the transaction ends
// when lock is set; found is false when there is none. A name the server
// cannot hold names no toolset.
func getToolset(ctx context.Context, q querier, tenant, name string, lock bool) (t store.ToolsetRecord, found bool, err error) {
	if !backend.TextOK(name) {
		return store.ToolsetRecord{}, false, nil
	}
	where := `WHERE tenant = $1 AND name = $2`
	if lock {
		where += ` FOR UPDATE`
	}
	ts, err := queryToolsets(ctx, q, where, tenant, name)
	if err != nil || len(ts) == 0 {
		return store.ToolsetRecord{}, false, err
	}
	return ts[0], true, nil
}

// queryToolsets reads the toolsets a WHERE clause (and what follows it)
// selects. It returns an empty slice, not nil, for none.
func queryToolsets(ctx context.Context, q querier, where string, args ...any) ([]store.ToolsetRecord, error) {
	rows, err := q.Query(ctx, `SELECT `+toolsetColumns+` FROM toolsets `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.ToolsetRecord{}
	for rows.Next() {
		var t store.ToolsetRecord
		var data string
		var created, updated *int64
		if err := rows.Scan(&t.Name, &data, &t.Version, &created, &updated); err != nil {
			return nil, err
		}
		t.Data = []byte(data)
		t.CreatedAt, t.UpdatedAt = loadTime(created), loadTime(updated)
		out = append(out, t)
	}
	return out, rows.Err()
}
