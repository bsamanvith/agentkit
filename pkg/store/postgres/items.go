package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

const (
	itemColumns    = `kind, name, latest, published, created_at, updated_at`
	versionColumns = `kind, name, version, data, author, message, created_at, published_at`
)

// SaveItem implements [store.ItemStore].
//
// One transaction locks the item's row, compares its latest version, then
// inserts the version and moves the item on where its latest is still the
// one read; a new item is inserted with ON CONFLICT DO NOTHING, so of two
// creates of one name (no row to lock yet) only one inserts.
func (s *Store) SaveItem(ctx context.Context, v store.ItemVersion, ifLatest int64) (store.ItemVersion, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.ItemVersion{}, err
	}
	v, err = backend.CheckItemVersion(v, ifLatest)
	if err != nil {
		return store.ItemVersion{}, err
	}
	now := s.now()
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.ItemVersion{}, err
	}
	var out store.ItemVersion
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			old, found, err := getItem(ctx, tx, tenant, v.Kind, v.Name, true)
			if err != nil {
				return fail("save item", err)
			}
			if err := backend.SaveItemError(v.Kind, v.Name, ifLatest, old.Latest); err != nil {
				return err
			}
			r := v
			r.Version, r.CreatedAt = old.Latest+1, now.UTC()
			if !found {
				tag, err := tx.Exec(ctx, `INSERT INTO items (tenant, kind, name, latest, published, created_at, updated_at)
					VALUES ($1, $2, $3, $4, 0, $5, $5) ON CONFLICT (tenant, kind, name) DO NOTHING`,
					tenant, r.Kind, r.Name, r.Version, now.UnixNano())
				if err != nil {
					return fail("save item", err)
				}
				if tag.RowsAffected() == 0 { // created since the read
					return fmt.Errorf("%w: %s %q exists", store.ErrConflict, v.Kind, v.Name)
				}
			} else {
				tag, err := tx.Exec(ctx, `UPDATE items SET latest = $1, updated_at = GREATEST(updated_at, $2)
					WHERE tenant = $3 AND kind = $4 AND name = $5 AND latest = $6`,
					r.Version, now.UnixNano(), tenant, r.Kind, r.Name, old.Latest)
				if err != nil {
					return fail("save item", err)
				}
				if tag.RowsAffected() == 0 { // changed since the read
					return errRetry
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO item_versions (tenant, kind, name, version, data, author, message, created_at, published_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0)`,
				tenant, r.Kind, r.Name, r.Version, string(r.Data), r.Author, r.Message, now.UnixNano()); err != nil {
				return fail("save item", err)
			}
			out = r
			return nil
		})
	})
	if err != nil {
		return store.ItemVersion{}, err
	}
	return out, nil
}

// PublishItem implements [store.ItemStore].
//
// One transaction locks the item's row, checks the version, then sets the
// version's first publication and the item's published version.
func (s *Store) PublishItem(ctx context.Context, kind, name string, version int64) (store.Item, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Item{}, err
	}
	if err := backend.CheckItemRef(kind, name, version); err != nil {
		return store.Item{}, err
	}
	now := s.now()
	if err := backend.CheckTime("the clock's time", now); err != nil {
		return store.Item{}, err
	}
	var out store.Item
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			it, found, err := getItem(ctx, tx, tenant, kind, name, true)
			if err != nil {
				return fail("publish item", err)
			}
			if !found {
				return backend.ItemVersionNotFound(kind, name, version)
			}
			vs, err := queryVersions(ctx, tx, `WHERE tenant = $1 AND kind = $2 AND name = $3 AND version = $4`, tenant, kind, name, version)
			if err != nil {
				return fail("publish item", err)
			}
			if len(vs) == 0 {
				return backend.ItemVersionNotFound(kind, name, version)
			}
			if it.Published == version {
				out = it
				return nil
			}
			if _, err := tx.Exec(ctx, `UPDATE item_versions SET published_at = $1
				WHERE tenant = $2 AND kind = $3 AND name = $4 AND version = $5 AND published_at = 0`,
				now.UnixNano(), tenant, kind, name, version); err != nil {
				return fail("publish item", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE items SET published = $1, updated_at = GREATEST(updated_at, $2)
				WHERE tenant = $3 AND kind = $4 AND name = $5`, version, now.UnixNano(), tenant, kind, name); err != nil {
				return fail("publish item", err)
			}
			it.Published = version
			if now.After(it.UpdatedAt) {
				it.UpdatedAt = now.UTC()
			}
			out = it
			return nil
		})
	})
	return out, err
}

// GetItem implements [store.ItemStore].
func (s *Store) GetItem(ctx context.Context, kind, name string) (store.Item, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Item{}, err
	}
	it, found, err := getItem(ctx, s.pool, tenant, kind, name, false)
	if err != nil {
		return store.Item{}, fail("get item", err)
	}
	if !found {
		return store.Item{}, backend.ItemNotFound(kind, name)
	}
	return it, nil
}

// ListItems implements [store.ItemStore].
func (s *Store) ListItems(ctx context.Context, kind string) ([]store.Item, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	if !backend.TextOK(kind) {
		return []store.Item{}, nil
	}
	out, err := queryItems(ctx, s.pool, `WHERE tenant = $1 AND kind = $2 ORDER BY name`, tenant, kind)
	if err != nil {
		return nil, fail("list items", err)
	}
	return out, nil
}

// GetItemVersion implements [store.ItemStore].
func (s *Store) GetItemVersion(ctx context.Context, kind, name string, version int64) (store.ItemVersion, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.ItemVersion{}, err
	}
	if !backend.TextOK(kind) || !backend.TextOK(name) {
		return store.ItemVersion{}, backend.ItemVersionNotFound(kind, name, version)
	}
	vs, err := queryVersions(ctx, s.pool, `WHERE tenant = $1 AND kind = $2 AND name = $3 AND version = $4`, tenant, kind, name, version)
	if err != nil {
		return store.ItemVersion{}, fail("get item version", err)
	}
	if len(vs) == 0 {
		return store.ItemVersion{}, backend.ItemVersionNotFound(kind, name, version)
	}
	return vs[0], nil
}

// ListItemVersions implements [store.ItemStore].
//
// One transaction, so the item and its versions agree.
func (s *Store) ListItemVersions(ctx context.Context, kind, name string) ([]store.ItemVersion, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.ItemVersion
	found := false
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		if _, found, err = getItem(ctx, tx, tenant, kind, name, false); err != nil || !found {
			return err
		}
		out, err = queryVersions(ctx, tx, `WHERE tenant = $1 AND kind = $2 AND name = $3 ORDER BY version`, tenant, kind, name)
		return err
	})
	switch {
	case err != nil:
		return nil, fail("list item versions", err)
	case !found:
		return nil, backend.ItemNotFound(kind, name)
	}
	return out, nil
}

// DeleteItemVersion implements [store.ItemStore].
//
// One transaction locks the item's row, checks the version is a draft,
// deletes it, and the item with it when it was the last.
func (s *Store) DeleteItemVersion(ctx context.Context, kind, name string, version int64) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	if err := backend.CheckItemRef(kind, name, version); err != nil {
		return err
	}
	return retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if _, found, err := getItem(ctx, tx, tenant, kind, name, true); err != nil {
				return fail("delete item version", err)
			} else if !found {
				return backend.ItemVersionNotFound(kind, name, version)
			}
			vs, err := queryVersions(ctx, tx, `WHERE tenant = $1 AND kind = $2 AND name = $3 AND version = $4`, tenant, kind, name, version)
			switch {
			case err != nil:
				return fail("delete item version", err)
			case len(vs) == 0:
				return backend.ItemVersionNotFound(kind, name, version)
			case !vs[0].PublishedAt.IsZero():
				return backend.ItemVersionPublished(kind, name, version)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM item_versions WHERE tenant = $1 AND kind = $2 AND name = $3 AND version = $4`,
				tenant, kind, name, version); err != nil {
				return fail("delete item version", err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM items WHERE tenant = $1 AND kind = $2 AND name = $3
				AND NOT EXISTS (SELECT 1 FROM item_versions v WHERE v.tenant = $1 AND v.kind = $2 AND v.name = $3)`,
				tenant, kind, name); err != nil {
				return fail("delete item version", err)
			}
			return nil
		})
	})
}

// getItem reads one item, locking its row until the transaction ends when
// lock is set; found is false when there is none. A kind or name the server
// cannot hold names no item.
func getItem(ctx context.Context, q querier, tenant, kind, name string, lock bool) (it store.Item, found bool, err error) {
	if !backend.TextOK(kind) || !backend.TextOK(name) {
		return store.Item{}, false, nil
	}
	where := `WHERE tenant = $1 AND kind = $2 AND name = $3`
	if lock {
		where += ` FOR UPDATE`
	}
	its, err := queryItems(ctx, q, where, tenant, kind, name)
	if err != nil || len(its) == 0 {
		return store.Item{}, false, err
	}
	return its[0], true, nil
}

// queryItems reads the items a WHERE clause (and what follows it) selects.
// It returns an empty slice, not nil, for none.
func queryItems(ctx context.Context, q querier, where string, args ...any) ([]store.Item, error) {
	rows, err := q.Query(ctx, `SELECT `+itemColumns+` FROM items `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Item{}
	for rows.Next() {
		var it store.Item
		var created, updated *int64
		if err := rows.Scan(&it.Kind, &it.Name, &it.Latest, &it.Published, &created, &updated); err != nil {
			return nil, err
		}
		it.CreatedAt, it.UpdatedAt = loadTime(created), loadTime(updated)
		out = append(out, it)
	}
	return out, rows.Err()
}

// queryVersions reads the versions a WHERE clause (and what follows it)
// selects. It returns an empty slice, not nil, for none.
func queryVersions(ctx context.Context, q querier, where string, args ...any) ([]store.ItemVersion, error) {
	rows, err := q.Query(ctx, `SELECT `+versionColumns+` FROM item_versions `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.ItemVersion{}
	for rows.Next() {
		var v store.ItemVersion
		var data string
		var created, published *int64
		if err := rows.Scan(&v.Kind, &v.Name, &v.Version, &data, &v.Author, &v.Message, &created, &published); err != nil {
			return nil, err
		}
		v.Data = []byte(data)
		v.CreatedAt = loadTime(created)
		if published != nil && *published != 0 {
			v.PublishedAt = loadTime(published)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
