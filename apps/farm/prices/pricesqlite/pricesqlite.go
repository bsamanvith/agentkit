// Package pricesqlite keeps the farm app's price history (prices.History)
// in a SQLite file: one row per day, market, commodity, variety and grade,
// with the names also kept folded so queries match them the way
// prices.Query does.
package pricesqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	_ "modernc.org/sqlite" // the driver: pure Go, so the binary stays static

	"github.com/bsamanvith/agentkit/apps/farm/prices"
)

var _ prices.History = (*Store)(nil)

// Store is a prices.History on a SQLite file.
type Store struct{ db *sql.DB }

const schema = `CREATE TABLE IF NOT EXISTS quotes (
	key       TEXT PRIMARY KEY,
	day       TEXT NOT NULL,
	state     TEXT NOT NULL,
	district  TEXT NOT NULL,
	market    TEXT NOT NULL,
	commodity TEXT NOT NULL,
	variety   TEXT NOT NULL,
	grade     TEXT NOT NULL,
	min       REAL NOT NULL,
	max       REAL NOT NULL,
	modal     REAL NOT NULL,
	source    TEXT NOT NULL,
	fstate    TEXT NOT NULL,
	fcommodity TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS quotes_by_commodity ON quotes (fcommodity, fstate, day);
CREATE INDEX IF NOT EXISTS quotes_by_state ON quotes (fstate, day)`

// Open opens the file at path, creating it and its table if needed.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("price sqlite: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String())
	if err != nil {
		return nil, fmt.Errorf("price sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, errors.Join(fmt.Errorf("price sqlite: open %s: %w", path, err), db.Close())
	}
	return &Store{db: db}, nil
}

// Close closes the file.
func (s *Store) Close() error { return s.db.Close() }

// Save implements prices.History.
func (s *Store) Save(ctx context.Context, qs []prices.Quote) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("price sqlite: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO quotes
		(key, day, state, district, market, commodity, variety, grade, min, max, modal, source, fstate, fcommodity)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET min = excluded.min, max = excluded.max, modal = excluded.modal, source = excluded.source`)
	if err != nil {
		return 0, fmt.Errorf("price sqlite: %w", err)
	}
	defer stmt.Close()
	n := 0
	for _, x := range qs {
		if x.Valid() != nil {
			continue
		}
		k := prices.QuoteKey(x)
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM quotes WHERE key = ?`, k).Scan(&exists); err != nil {
			return 0, fmt.Errorf("price sqlite: %w", err)
		}
		if _, err := stmt.ExecContext(ctx, k, x.Date.String(), x.State, x.District, x.Market, x.Commodity, x.Variety, x.Grade,
			x.Min, x.Max, x.Modal, x.Source, prices.Fold(x.State), prices.Fold(prices.Commodity(x.Commodity))); err != nil {
			return 0, fmt.Errorf("price sqlite: save: %w", err)
		}
		if exists == 0 {
			n++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("price sqlite: %w", err)
	}
	return n, nil
}

// Quotes implements prices.History. The database narrows by state, date
// and the commodity's first letters; Go applies the query's matching
// rules (spellings of districts and markets, commodity names) to the rest.
func (s *Store) Quotes(ctx context.Context, q prices.Query) ([]prices.Quote, error) {
	q, err := q.Normalized()
	if err != nil {
		return nil, err
	}
	var where []string
	var args []any
	if f := prices.Fold(q.State); f != "" {
		where = append(where, "fstate = ?")
		args = append(args, f)
	}
	if f := prices.Fold(q.Commodity); f != "" {
		// SameCommodity matches one name starting the other: narrow by the
		// shorter of the two, the first five letters.
		where = append(where, "fcommodity LIKE ?")
		args = append(args, f[:min(len(f), 5)]+"%")
	}
	if !q.From.IsZero() {
		where = append(where, "day >= ?")
		args = append(args, q.From.String())
	}
	if !q.To.IsZero() {
		where = append(where, "day <= ?")
		args = append(args, q.To.String())
	}
	sqlq := `SELECT day, state, district, market, commodity, variety, grade, min, max, modal, source FROM quotes`
	if len(where) > 0 {
		sqlq += " WHERE " + strings.Join(where, " AND ")
	}
	sqlq += " ORDER BY day DESC, market, variety"
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, fmt.Errorf("price sqlite: %w", err)
	}
	defer rows.Close()
	var out []prices.Quote
	for rows.Next() {
		var x prices.Quote
		var day string
		if err := rows.Scan(&day, &x.State, &x.District, &x.Market, &x.Commodity, &x.Variety, &x.Grade, &x.Min, &x.Max, &x.Modal, &x.Source); err != nil {
			return nil, fmt.Errorf("price sqlite: %w", err)
		}
		if x.Date, err = prices.ParseDay(day); err != nil {
			return nil, fmt.Errorf("price sqlite: %w", err)
		}
		if !q.Matches(x) {
			continue
		}
		out = append(out, x)
		if len(out) >= q.Limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("price sqlite: %w", err)
	}
	return slices.Clip(out), nil
}
