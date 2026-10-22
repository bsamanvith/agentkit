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
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/keyword"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/semantic"
)

var (
	_ memory.Index      = (*Index)(nil)
	_ memory.Reembedder = (*Index)(nil)
)

// indexSchemaVersion is the index schema this package writes. It is kept in
// memory_index_meta, not in PRAGMA user_version (the Store's), so a Store
// and an Index can share a file.
const indexSchemaVersion = 2

// The index's tables: the items, an FTS5 table over their words, whose
// rowid is the item's seq, and the items' vectors (version 2), each with the
// model and dimension that made it. Each word is stored hex-encoded, so FTS5's ascii
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
` + vectorSchema + `
INSERT INTO memory_index_meta (key, value) VALUES ('schema', 2);
`

// vectorSchema is what version 2 adds: a vector per item (its seq), as
// little-endian float32s of unit length. The triggers keep a vector only
// while its item has the text it was made of, whoever writes the file: a
// process that opened it at version 1 before another upgraded it still
// writes items without vectors, and its deletes and edits must not leave a
// vector to the item (or a reused seq) that it no longer describes. The
// item then has no vector, which a semantic search reports.
const vectorSchema = `
CREATE TABLE IF NOT EXISTS memory_vectors (
	seq   INTEGER PRIMARY KEY,
	model TEXT    NOT NULL,
	dim   INTEGER NOT NULL,
	vec   BLOB    NOT NULL
);
CREATE TRIGGER IF NOT EXISTS memory_vectors_item_deleted AFTER DELETE ON memory_items
BEGIN DELETE FROM memory_vectors WHERE seq = old.seq; END;
CREATE TRIGGER IF NOT EXISTS memory_vectors_text_changed AFTER UPDATE OF text ON memory_items
WHEN new.text IS NOT old.text
BEGIN DELETE FROM memory_vectors WHERE seq = old.seq; END;`

// Index is a memory.Index in a SQLite file: keyword search through an FTS5
// index, ranked by BM25 over the scope's own statistics (never another
// tenant's), so its results are the in-memory index's. With an embedder
// (memory.IndexConfig.Embedder), each item's vector is stored with it, and a
// semantic search compares the query's with every vector of the scope (brute
// force); a vector of another model or dimension than the embedder's (a
// file written with another embedder) is memory.ErrEmbedderMismatch until
// [Index.Reembed]. Close it when done.
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
		case v == indexSchemaVersion:
			return nil
		}
		// Version 1 (T059) had no vectors: its items have none until they
		// are put again or re-embedded.
		if _, err := tx.ExecContext(ctx, vectorSchema+`
			UPDATE memory_index_meta SET value = 2 WHERE key = 'schema';`); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, indexSchema); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes the file.
func (x *Index) Close() error { return errors.Join(x.r.Close(), x.w.Close()) }

// Modes implements memory.Index: the configured modes.
func (x *Index) Modes() []memory.Mode { return slices.Clone(x.cfg.Modes) }

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
	var vec *semantic.Vector
	if x.cfg.Embedder != nil {
		// No embedding (a paid call) for an item that cannot be stored.
		if err := x.checkRoom(ctx, o, it.ID); err != nil {
			return err
		}
		vs, err := semantic.Embed(ctx, x.cfg.Embedder, []string{it.Text}, memory.PurposeDocument)
		if err != nil {
			return err
		}
		vec = &vs[0]
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
	// Without an embedder the old vector (of the old text) goes.
	if err := writeVector(ctx, tx, seq, vec); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	return nil
}

// Search implements memory.Index. In ModeKeyword, FTS5 finds the scope's
// items that match; the scope's statistics come from its own rows;
// keyword.Rank scores them. In ModeSemantic, every vector of the scope is
// read and compared with the query's (semantic.Rank). A hybrid search fuses
// both, read in one transaction.
func (x *Index) Search(ctx context.Context, q memory.Query) ([]memory.Result, error) {
	o, err := x.owner(ctx)
	if err != nil {
		return nil, err
	}
	q, err = memory.ResolveQuery(q, x.Modes())
	if err != nil {
		return nil, err
	}
	var kq *memory.KeywordQuery
	switch q.Mode {
	case memory.ModeKeyword:
		p, err := memory.ParseKeywordQuery(q.Text)
		if err != nil {
			return nil, err
		}
		kq = &p
	case memory.ModeHybrid:
		if text := memory.HybridKeywordText(q.Text); text != "" {
			p, err := memory.ParseKeywordQuery(text)
			if err != nil {
				return nil, err
			}
			kq = &p
		}
	}
	var qv semantic.Vector
	if q.Mode != memory.ModeKeyword {
		vs, err := semantic.Embed(ctx, x.cfg.Embedder, []string{q.Text}, memory.PurposeQuery)
		if err != nil {
			return nil, err
		}
		qv = vs[0]
	}
	tx, err := x.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	switch q.Mode {
	case memory.ModeKeyword:
		return keywordSearch(ctx, tx, o, *kq, q.Filter, q.K)
	case memory.ModeSemantic:
		return semanticSearch(ctx, tx, o, qv, q.Filter, q.K)
	}
	sem, err := semanticSearch(ctx, tx, o, qv, q.Filter, memory.HybridDepth)
	if err != nil {
		return nil, err
	}
	var kw []memory.Result
	if kq != nil {
		if kw, err = keywordSearch(ctx, tx, o, *kq, q.Filter, memory.HybridDepth); err != nil {
			return nil, err
		}
	}
	return semantic.Fuse(kw, sem, q.K), nil
}

// keywordSearch ranks the scope's items for kq. A query with a phrase also
// counts the items with each of its parts, one FTS5 query each; one without
// takes those counts from its candidates.
func keywordSearch(ctx context.Context, tx *sql.Tx, o memory.Owner, kq memory.KeywordQuery, filter map[string]string, k int) ([]memory.Result, error) {
	const inScope = `FROM memory_items_fts f JOIN memory_items i ON i.seq = f.rowid
		WHERE memory_items_fts MATCH ? AND i.tenant = ? AND i.user_id = ?`
	parts := keyword.Parts(kq)
	st := keyword.Stats{DF: make([]int, len(parts))}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(words), 0) FROM memory_items WHERE tenant = ? AND user_id = ?`,
		o.Tenant, o.User).Scan(&st.Docs, &st.Tokens); err != nil {
		return nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	// With a phrase, the candidates are the items with every phrase, so a
	// part's count needs its own query; with none, they are the items with
	// any term, and the counts come from them below.
	if len(kq.Phrases) > 0 {
		for i, p := range parts {
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) `+inScope, ftsPhrase(p), o.Tenant, o.User).Scan(&st.DF[i]); err != nil {
				return nil, fmt.Errorf("memory/sqlite: %w", err)
			}
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
		d := keyword.Doc{Item: it, Tokens: memory.Tokenize(it.Text)}
		if len(kq.Phrases) == 0 {
			for i, p := range parts {
				if keyword.Count(d.Tokens, p) > 0 {
					st.DF[i]++
				}
			}
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	return keyword.Rank(kq, docs, st, filter, k), nil
}

// semanticSearch ranks every item of the scope by its vector's similarity
// to qv, as semantic.Rank does. Each vector is scored as its row is read
// (semantic.DotBlob, on the driver's bytes), so the scan holds the items of
// the filter and their scores, never the scope's vectors.
func semanticSearch(ctx context.Context, tx *sql.Tx, o memory.Owner, qv semantic.Vector, filter map[string]string, k int) ([]memory.Result, error) {
	rows, err := tx.QueryContext(ctx, `SELECT i.id, i.text, i.metadata, i.time, v.model, v.dim, v.vec
		FROM memory_items i LEFT JOIN memory_vectors v ON v.seq = i.seq
		WHERE i.tenant = ? AND i.user_id = ?`, o.Tenant, o.User)
	if err != nil {
		return nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	defer rows.Close()
	var cands []semantic.Scored
	for rows.Next() {
		var it memory.Item
		var meta string
		var at, dim sql.NullInt64
		var model sql.NullString
		var blob sql.RawBytes // valid until the next row: scored at once
		if err := rows.Scan(&it.ID, &it.Text, &meta, &at, &model, &dim, &blob); err != nil {
			return nil, fmt.Errorf("memory/sqlite: %w", err)
		}
		if err := semantic.Check(qv, it.ID, model.String, int(dim.Int64)); err != nil {
			return nil, err
		}
		score, err := semantic.DotBlob(qv.V, blob)
		if err != nil {
			return nil, fmt.Errorf("memory/sqlite: item %q: %w", it.ID, err)
		}
		if it, err = decodeItem(it, meta, at); err != nil {
			return nil, err
		}
		if memory.MatchesFilter(it.Metadata, filter) {
			cands = append(cands, semantic.Scored{Item: it, Score: score})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	return semantic.Top(cands, k), nil
}

// checkRoom fails with the items LimitError when the scope is full and has
// no item id.
func (x *Index) checkRoom(ctx context.Context, o memory.Owner, id string) error {
	var exists, n int
	if err := x.r.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM memory_items WHERE tenant = ? AND user_id = ? AND id = ?),
		(SELECT COUNT(*) FROM memory_items WHERE tenant = ? AND user_id = ?)`,
		o.Tenant, o.User, id, o.Tenant, o.User).Scan(&exists, &n); err != nil {
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	if exists == 0 && n >= x.cfg.MaxItems {
		return &memory.LimitError{Limit: "items", Max: int64(x.cfg.MaxItems), Want: int64(n + 1)}
	}
	return nil
}

// writeVector stores the vector of the item seq, or removes it for nil.
func writeVector(ctx context.Context, tx *sql.Tx, seq int64, v *semantic.Vector) error {
	var err error
	if v == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM memory_vectors WHERE seq = ?`, seq)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO memory_vectors (seq, model, dim, vec) VALUES (?, ?, ?, ?)`,
			seq, v.Model, v.Dim, semantic.Encode(v.V))
	}
	if err != nil {
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	return nil
}

// reembedBatch is how many items Reembed embeds per call of the embedder.
const reembedBatch = 64

// Reembed implements memory.Reembedder: it embeds again, in every scope of
// the file, each item whose vector is missing or was made by another model
// or dimension than the index's embedder, reembedBatch texts per call, and
// returns how many it updated. An item changed meanwhile keeps the vector
// its Put wrote. It is safe to run while the index is in use, and to run
// again after a failure: what it finished stays done.
func (x *Index) Reembed(ctx context.Context) (int, error) {
	if x.cfg.Embedder == nil {
		return 0, fmt.Errorf("%w: memory/sqlite: re-embed needs an embedder", memory.ErrInvalid)
	}
	name, dim := x.cfg.Embedder.Model()
	done := 0
	var after int64
	for {
		seqs, texts, err := x.stale(ctx, after, name, dim)
		if err != nil {
			return done, err
		}
		if len(seqs) == 0 {
			return done, nil
		}
		after = seqs[len(seqs)-1]
		vs, err := semantic.Embed(ctx, x.cfg.Embedder, texts, memory.PurposeDocument)
		if err != nil {
			return done, err
		}
		n, err := x.writeVectors(ctx, seqs, texts, vs)
		done += n
		if err != nil {
			return done, err
		}
	}
}

// stale returns up to reembedBatch items after seq whose vector is missing,
// not of the model name with dimension dim, or not 4*dim bytes long (a
// corrupt blob, which a search refuses).
func (x *Index) stale(ctx context.Context, after int64, name string, dim int) ([]int64, []string, error) {
	rows, err := x.r.QueryContext(ctx, `SELECT i.seq, i.text FROM memory_items i LEFT JOIN memory_vectors v ON v.seq = i.seq
		WHERE i.seq > ? AND (v.seq IS NULL OR v.model != ? OR v.dim != ? OR length(v.vec) != ?)
		ORDER BY i.seq LIMIT CAST(? AS INTEGER)`,
		after, name, dim, 4*dim, reembedBatch)
	if err != nil {
		return nil, nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	defer rows.Close()
	var seqs []int64
	var texts []string
	for rows.Next() {
		var seq int64
		var text string
		if err := rows.Scan(&seq, &text); err != nil {
			return nil, nil, fmt.Errorf("memory/sqlite: %w", err)
		}
		seqs, texts = append(seqs, seq), append(texts, text)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("memory/sqlite: %w", err)
	}
	return seqs, texts, nil
}

// writeVectors stores vs for the items seqs whose text is still texts.
func (x *Index) writeVectors(ctx context.Context, seqs []int64, texts []string, vs []semantic.Vector) (int, error) {
	tx, err := x.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("memory/sqlite: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	n := 0
	for i, seq := range seqs {
		var text string
		err := tx.QueryRowContext(ctx, `SELECT text FROM memory_items WHERE seq = ?`, seq).Scan(&text)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			continue // deleted meanwhile
		case err != nil:
			return 0, fmt.Errorf("memory/sqlite: %w", err)
		case text != texts[i]:
			continue // put again meanwhile, with its own vector
		}
		if err := writeVector(ctx, tx, seq, &vs[i]); err != nil {
			return 0, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("memory/sqlite: %w", err)
	}
	return n, nil
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
	return decodeItem(it, meta, at)
}

// decodeItem fills in it's metadata and time from their columns.
func decodeItem(it memory.Item, meta string, at sql.NullInt64) (memory.Item, error) {
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
	if err := writeVector(ctx, tx, seq, nil); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory/sqlite: %w", err)
	}
	return nil
}
