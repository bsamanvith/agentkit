package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

const (
	itemColumns    = `kind, name, latest, published, created_at, updated_at`
	versionColumns = `kind, name, version, data, author, message, created_at, published_at`
)

// SaveItem implements [store.ItemStore].
//
// One transaction, which holds the write lock from its start: read the
// item's latest version, compare it, then insert the version and write the
// item.
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
	err = s.write(ctx, func(tx *txn) error {
		old, found, err := getItem(ctx, tx, tenant, v.Kind, v.Name)
		if err != nil {
			return fmt.Errorf("sqlite: save item: %w", err)
		}
		if err := backend.SaveItemError(v.Kind, v.Name, ifLatest, old.Latest); err != nil {
			return err
		}
		v.Version, v.CreatedAt = old.Latest+1, now
		if !found {
			_, err = tx.ExecContext(ctx, `INSERT INTO items (tenant, kind, name, latest, published, created_at, updated_at)
				VALUES (?, ?, ?, ?, 0, ?, ?)`, tenant, v.Kind, v.Name, v.Version, now.UnixNano(), now.UnixNano())
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE items SET latest = ?, updated_at = max(updated_at, ?)
				WHERE tenant = ? AND kind = ? AND name = ?`, v.Version, now.UnixNano(), tenant, v.Kind, v.Name)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO item_versions (tenant, kind, name, version, data, author, message, created_at, published_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`, tenant, v.Kind, v.Name, v.Version, string(v.Data), v.Author, v.Message, now.UnixNano())
		}
		if err != nil {
			return fmt.Errorf("sqlite: save item: %w", err)
		}
		return nil
	})
	if err != nil {
		return store.ItemVersion{}, err
	}
	v.CreatedAt = v.CreatedAt.UTC()
	return v, nil
}

// PublishItem implements [store.ItemStore].
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
	err = s.write(ctx, func(tx *txn) error {
		it, found, err := getItem(ctx, tx, tenant, kind, name)
		if err != nil {
			return fmt.Errorf("sqlite: publish item: %w", err)
		}
		if !found {
			return backend.ItemVersionNotFound(kind, name, version)
		}
		vs, err := queryVersions(ctx, tx, `WHERE tenant = ? AND kind = ? AND name = ? AND version = ?`, tenant, kind, name, version)
		if err != nil {
			return fmt.Errorf("sqlite: publish item: %w", err)
		}
		if len(vs) == 0 {
			return backend.ItemVersionNotFound(kind, name, version)
		}
		if it.Published == version {
			out = it
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE item_versions SET published_at = ?
			WHERE tenant = ? AND kind = ? AND name = ? AND version = ? AND published_at = 0`, now.UnixNano(), tenant, kind, name, version); err != nil {
			return fmt.Errorf("sqlite: publish item: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE items SET published = ?, updated_at = max(updated_at, ?)
			WHERE tenant = ? AND kind = ? AND name = ?`, version, now.UnixNano(), tenant, kind, name); err != nil {
			return fmt.Errorf("sqlite: publish item: %w", err)
		}
		it.Published = version
		if now.After(it.UpdatedAt) {
			it.UpdatedAt = now.UTC()
		}
		out = it
		return nil
	})
	return out, err
}

// GetItem implements [store.ItemStore].
func (s *Store) GetItem(ctx context.Context, kind, name string) (store.Item, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Item{}, err
	}
	it, found, err := getItem(ctx, s.r, tenant, kind, name)
	if err != nil {
		return store.Item{}, fmt.Errorf("sqlite: get item: %w", err)
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
	out, err := queryItems(ctx, s.r, `WHERE tenant = ? AND kind = ? ORDER BY name`, tenant, kind)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list items: %w", err)
	}
	return out, nil
}

// GetItemVersion implements [store.ItemStore].
func (s *Store) GetItemVersion(ctx context.Context, kind, name string, version int64) (store.ItemVersion, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.ItemVersion{}, err
	}
	vs, err := queryVersions(ctx, s.r, `WHERE tenant = ? AND kind = ? AND name = ? AND version = ?`, tenant, kind, name, version)
	if err != nil {
		return store.ItemVersion{}, fmt.Errorf("sqlite: get item version: %w", err)
	}
	if len(vs) == 0 {
		return store.ItemVersion{}, backend.ItemVersionNotFound(kind, name, version)
	}
	return vs[0], nil
}

// ListItemVersions implements [store.ItemStore].
func (s *Store) ListItemVersions(ctx context.Context, kind, name string) ([]store.ItemVersion, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.ItemVersion
	// One read transaction, so the item and its versions agree.
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("sqlite: list item versions: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, found, err := getItem(ctx, tx, tenant, kind, name)
	if err == nil && found {
		out, err = queryVersions(ctx, tx, `WHERE tenant = ? AND kind = ? AND name = ? ORDER BY version`, tenant, kind, name)
	}
	switch {
	case err != nil:
		return nil, fmt.Errorf("sqlite: list item versions: %w", err)
	case !found:
		return nil, backend.ItemNotFound(kind, name)
	}
	return out, nil
}

// DeleteItemVersion implements [store.ItemStore].
func (s *Store) DeleteItemVersion(ctx context.Context, kind, name string, version int64) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	if err := backend.CheckItemRef(kind, name, version); err != nil {
		return err
	}
	return s.write(ctx, func(tx *txn) error {
		vs, err := queryVersions(ctx, tx, `WHERE tenant = ? AND kind = ? AND name = ? AND version = ?`, tenant, kind, name, version)
		switch {
		case err != nil:
			return fmt.Errorf("sqlite: delete item version: %w", err)
		case len(vs) == 0:
			return backend.ItemVersionNotFound(kind, name, version)
		case !vs[0].PublishedAt.IsZero():
			return backend.ItemVersionPublished(kind, name, version)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM item_versions WHERE tenant = ? AND kind = ? AND name = ? AND version = ?`,
			tenant, kind, name, version); err != nil {
			return fmt.Errorf("sqlite: delete item version: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM items WHERE tenant = ? AND kind = ? AND name = ?
			AND NOT EXISTS (SELECT 1 FROM item_versions v WHERE v.tenant = items.tenant AND v.kind = items.kind AND v.name = items.name)`,
			tenant, kind, name); err != nil {
			return fmt.Errorf("sqlite: delete item version: %w", err)
		}
		return nil
	})
}

// getItem reads one item; found is false when there is none.
func getItem(ctx context.Context, q querier, tenant, kind, name string) (it store.Item, found bool, err error) {
	its, err := queryItems(ctx, q, `WHERE tenant = ? AND kind = ? AND name = ?`, tenant, kind, name)
	if err != nil || len(its) == 0 {
		return store.Item{}, false, err
	}
	return its[0], true, nil
}

// queryItems reads the items a WHERE clause (and what follows it) selects.
// It returns an empty slice, not nil, for none.
func queryItems(ctx context.Context, q querier, where string, args ...any) ([]store.Item, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+itemColumns+` FROM items `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Item{}
	for rows.Next() {
		var it store.Item
		var created, updated sql.NullInt64
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
	rows, err := q.QueryContext(ctx, `SELECT `+versionColumns+` FROM item_versions `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.ItemVersion{}
	for rows.Next() {
		var v store.ItemVersion
		var data string
		var created, published sql.NullInt64
		if err := rows.Scan(&v.Kind, &v.Name, &v.Version, &data, &v.Author, &v.Message, &created, &published); err != nil {
			return nil, err
		}
		v.Data = []byte(data)
		v.CreatedAt = loadTime(created)
		if published.Valid && published.Int64 != 0 {
			v.PublishedAt = loadTime(published)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
