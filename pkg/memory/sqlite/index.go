package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/keyword"
)

var _ memory.Index = (*Index)(nil)

// indexSchemaVersion is the index schema this package writes. It is kept in
// memory_index_meta, not in PRAGMA user_version (the Store's), so a Store
// and an Index can share a file.
const indexSchemaVersion = 1

// The index's tables: the items, and an FTS5 table over their words, whose
// rowid is the item's seq. Each word is stored hex-encoded, so FTS5's ascii
// tokenizer splits the column exactly where memory.Tokenize split the text,
// whatever the script, and a phrase query matches the same words in the
// same order as the in-memory index's.
const indexSchema = `
CREATE TABLE IF NOT EXISTS memory_index_meta (
	key   TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS memory_items (
	seq      INTEGER PRIMARY KEY,
	tenant   TEXT    NOT NULL,
	user_id  TEXT    NOT NULL,
	id       TEXT    NOT NULL,
	text     TEXT    NOT NULL,
	metadata TEXT    NOT NULL,
	time     INTEGER,
	words    INTEGER NOT NULL,
	UNIQUE (tenant, user_id, id)
);
CREATE VIRTUAL TABLE IF NOT EXISTS memory_items_fts USING fts5(words, tokenize = 'ascii');
INSERT INTO memory_index_meta (key, value) VALUES ('schema', 1);
`

// Index is a memory.Index in a SQLite file: keyword search through an FTS5
// index, ranked by BM25 over the scope's own statistics (never another
// tenant's), so its results are the in-memory index's. Close it when done.
// Writes are IMMEDIATE transactions on one connection; a search reads one
// state of the file.
type Index struct {
	cfg memory.IndexConfig
	w   *sql.DB
	r   *sql.DB
}

// OpenIndex opens (creating it if needed) the index in the file at path,
// which may also hold a [Store]. It fails with memory.ErrInvalid for a bad
// cfg, and with ErrSchemaTooNew for an index written by a newer version.
func OpenIndex(ctx context.Context, path string, cfg memory.IndexConfig) (*Index, error) {
	cfg, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	w, err := sql.Open("sqlite", dsn(abs, false))
	if err != nil {
		return nil, fmt.Errorf("memory/sqlite: open %s: %w", path, err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)
	if err := migrateIndex(ctx, w); err != nil {
		return nil, errors.Join(fmt.Errorf("memory/sqlite: open index %s: %w", path, err), w.Close())
	}
	r, err := sql.Open("sqlite", dsn(abs, true))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("memory/sqlite: open %s: %w", path, err), w.Close())
	}
	n := max(2, runtime.GOMAXPROCS(0))
	r.SetMaxOpenConns(n)
	r.SetMaxIdleConns(n)
	return &Index{cfg: cfg, w: w, r: r}, nil
}

func migrateIndex(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var tables int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'memory_index_meta'`).Scan(&tables); err != nil {
		return err
	}
	if tables > 0 {
		var v int
		err := tx.QueryRowContext(ctx, `SELECT value FROM memory_index_meta WHERE key = 'schema'`).Scan(&v)
		switch {
		case err != nil:
			return err
		case v > indexSchemaVersion:
			return fmt.Errorf("%w: index version %d, this binary knows %d", ErrSchemaTooNew, v, indexSchemaVersion)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, indexSchema); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes the file.
func (x *Index) Close() error { return errors.Join(x.r.Close(), x.w.Close()) }

// Modes implements memory.Index: keyword only.
func (x *Index) Modes() []memory.Mode { return []memory.Mode{memory.ModeKeyword} }

func (x *Index) owner(ctx context.Context) (memory.Owner, error) {
	if err := ctx.Err(); err != nil {
		return memory.Owner{}, err
	}
	return memory.OwnerOf(ctx, x.cfg.Scope)
}

// ftsWords is the FTS5 column of words: each hex-encoded, space-separated.
func ftsWords(words []string) string {
	enc := make([]string, len(words))
	for i, w := range words {
		enc[i] = hex.EncodeToString([]byte(w))
	}
	return strings.Join(enc, " ")
}

// ftsPhrase is an FTS5 string matching part: its hex words in order.
func ftsPhrase(part []string) string { return `"` + ftsWords(part) + `"` }

// Put implements memory.Index.
func (x *Index) Put(ctx context.Context, it memory.Item) error {
	o, err := x.owner(ctx)
	if err != nil {
		return err
	}
	it, err = memory.CheckItem(it, x.cfg)
	if err != nil {
		return err
	}
	words := memory.Tokenize(it.Text)
	meta, err := json.Marshal(it.Metadata)
	if err != nil {
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	var at sql.NullInt64
	if !it.Time.IsZero() {
		at = sql.NullInt64{Int64: it.Time.UnixNano(), Valid: true}
	}
	tx, err := x.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var seq int64
	err = tx.QueryRowContext(ctx, `SELECT seq FROM memory_items WHERE tenant = ? AND user_id = ? AND id = ?`,
		o.Tenant, o.User, it.ID).Scan(&seq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_items WHERE tenant = ? AND user_id = ?`,
			o.Tenant, o.User).Scan(&n); err != nil {
			return fmt.Errorf("memory/sqlite: %w", err)
		}
		if n >= x.cfg.MaxItems {
			return &memory.LimitError{Limit: "items", Max: int64(x.cfg.MaxItems), Want: int64(n + 1)}
		}
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO memory_items (tenant, user_id, id, text, metadata, time, words) VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING seq`,
			o.Tenant, o.User, it.ID, it.Text, string(meta), at, len(words)).Scan(&seq); err != nil {
			return fmt.Errorf("memory/sqlite: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_items_fts (rowid, words) VALUES (?, ?)`, seq, ftsWords(words)); err != nil {
			return fmt.Errorf("memory/sqlite: %w", err)
		}
	case err != nil:
		return fmt.Errorf("memory/sqlite: %w", err)
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE memory_items SET text = ?, metadata = ?, time = ?, words = ? WHERE seq = ?`,
			it.Text, string(meta), at, len(words), seq); err != nil {
			return fmt.Errorf("memory/sqlite: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE memory_items_fts SET words = ? WHERE rowid = ?`, ftsWords(words), seq); err != nil {
			return fmt.Errorf("memory/sqlite: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	return nil
}

// Search implements memory.Index. FTS5 finds the scope's items that match;
// the scope's statistics come from its own rows; keyword.Rank scores them.
func (x *Index) Search(ctx context.Context, q memory.Query) ([]memory.Result, error) {
	o, err := x.owner(ctx)
	if err != nil {
		return nil, err
	}
	q, err = memory.ResolveQuery(q, x.Modes())
	if err != nil {
		return nil, err
	}
	kq, err := memory.ParseKeywordQuery(q.Text)
	if err != nil {
		return nil, err
	}
	tx, err := x.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const inScope = `FROM memory_items_fts f JOIN memory_items i ON i.seq = f.rowid
		WHERE memory_items_fts MATCH ? AND i.tenant = ? AND i.user_id = ?`
	parts := keyword.Parts(kq)
	st := keyword.Stats{DF: make([]int, len(parts))}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(words), 0) FROM memory_items WHERE tenant = ? AND user_id = ?`,
		o.Tenant, o.User).Scan(&st.Docs, &st.Tokens); err != nil {
		return nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	for i, p := range parts {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) `+inScope, ftsPhrase(p), o.Tenant, o.User).Scan(&st.DF[i]); err != nil {
			return nil, fmt.Errorf("memory/sqlite: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT i.id, i.text, i.metadata, i.time `+inScope, matchExpr(kq), o.Tenant, o.User)
	if err != nil {
		return nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	defer rows.Close()
	var docs []keyword.Doc
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		docs = append(docs, keyword.Doc{Item: it, Tokens: memory.Tokenize(it.Text)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	return keyword.Rank(kq, docs, st, q.Filter, q.K), nil
}

// matchExpr is the FTS5 query of the items that match q: every phrase, or
// with none, any term.
func matchExpr(q memory.KeywordQuery) string {
	var s []string
	if len(q.Phrases) > 0 {
		for _, p := range q.Phrases {
			s = append(s, ftsPhrase(p))
		}
		return strings.Join(s, " AND ")
	}
	for _, t := range q.Terms {
		s = append(s, ftsPhrase([]string{t}))
	}
	return strings.Join(s, " OR ")
}

type scanner interface{ Scan(dest ...any) error }

func scanItem(r scanner) (memory.Item, error) {
	var it memory.Item
	var meta string
	var at sql.NullInt64
	if err := r.Scan(&it.ID, &it.Text, &meta, &at); err != nil {
		return memory.Item{}, err
	}
	if err := json.Unmarshal([]byte(meta), &it.Metadata); err != nil {
		return memory.Item{}, fmt.Errorf("memory/sqlite: item %q: metadata: %w", it.ID, err)
	}
	if len(it.Metadata) == 0 {
		it.Metadata = nil
	}
	if at.Valid {
		it.Time = time.Unix(0, at.Int64).UTC()
	}
	return it, nil
}

// Get implements memory.Index.
func (x *Index) Get(ctx context.Context, id string) (memory.Item, error) {
	o, err := x.owner(ctx)
	if err != nil {
		return memory.Item{}, err
	}
	if err := memory.CheckID(id); err != nil {
		return memory.Item{}, err
	}
	it, err := scanItem(x.r.QueryRowContext(ctx,
		`SELECT id, text, metadata, time FROM memory_items WHERE tenant = ? AND user_id = ? AND id = ?`,
		o.Tenant, o.User, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return memory.Item{}, memory.ErrItemNotFound
	case err != nil:
		return memory.Item{}, fmt.Errorf("memory/sqlite: %w", err)
	}
	return it, nil
}

// Delete implements memory.Index.
func (x *Index) Delete(ctx context.Context, id string) error {
	o, err := x.owner(ctx)
	if err != nil {
		return err
	}
	if err := memory.CheckID(id); err != nil {
		return err
	}
	tx, err := x.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var seq int64
	err = tx.QueryRowContext(ctx, `DELETE FROM memory_items WHERE tenant = ? AND user_id = ? AND id = ? RETURNING seq`,
		o.Tenant, o.User, id).Scan(&seq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return memory.ErrItemNotFound
	case err != nil:
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_items_fts WHERE rowid = ?`, seq); err != nil {
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	return nil
}
