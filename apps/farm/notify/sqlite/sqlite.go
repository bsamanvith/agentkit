// Package sqlite keeps the farm app's message recipients, outbox and
// deliveries in a SQLite file (the app's own, beside its farms).
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the driver: pure Go

	"github.com/bsamanvith/agentkit/apps/farm/notify"
)

var _ notify.Store = (*Store)(nil)

// Store is a notify.Store on a SQLite file.
type Store struct{ db *sql.DB }

const schema = `CREATE TABLE IF NOT EXISTS notify_recipients (
	id         TEXT PRIMARY KEY,
	phone      TEXT NOT NULL,
	email      TEXT NOT NULL,
	telegram   TEXT NOT NULL,
	link_code  TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	data       TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS notify_recipients_phone ON notify_recipients (phone);
CREATE INDEX IF NOT EXISTS notify_recipients_email ON notify_recipients (email);
CREATE INDEX IF NOT EXISTS notify_recipients_telegram ON notify_recipients (telegram);
CREATE INDEX IF NOT EXISTS notify_recipients_link ON notify_recipients (link_code);
CREATE TABLE IF NOT EXISTS notify_outbox (
	id         TEXT PRIMARY KEY,
	recipient  TEXT NOT NULL,
	notice     TEXT NOT NULL,
	status     TEXT NOT NULL,
	attempts   INTEGER NOT NULL,
	next_at    INTEGER NOT NULL,
	last_error TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	data       TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS notify_outbox_due ON notify_outbox (status, next_at);
CREATE INDEX IF NOT EXISTS notify_outbox_recipient ON notify_outbox (recipient, created_at);
CREATE INDEX IF NOT EXISTS notify_outbox_notice ON notify_outbox (notice);
CREATE TABLE IF NOT EXISTS notify_deliveries (
	message     TEXT NOT NULL,
	lane        TEXT NOT NULL,
	recipient   TEXT NOT NULL,
	status      TEXT NOT NULL,
	provider_id TEXT NOT NULL,
	cost        INTEGER NOT NULL,
	error       TEXT NOT NULL,
	retryable   INTEGER NOT NULL,
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL,
	PRIMARY KEY (message, lane)
) STRICT;
CREATE INDEX IF NOT EXISTS notify_deliveries_provider ON notify_deliveries (lane, provider_id);
CREATE INDEX IF NOT EXISTS notify_deliveries_spend ON notify_deliveries (lane, created_at);
CREATE INDEX IF NOT EXISTS notify_deliveries_recipient ON notify_deliveries (recipient, created_at)`

// Open opens the file at path, creating it and its tables if needed.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("notify sqlite: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String())
	if err != nil {
		return nil, fmt.Errorf("notify sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, errors.Join(fmt.Errorf("notify sqlite: open %s: %w", path, err), db.Close())
	}
	return &Store{db: db}, nil
}

// Close closes the file.
func (s *Store) Close() error { return s.db.Close() }

func wrap(op string, err error) error { return fmt.Errorf("notify sqlite: %s: %w", op, err) }

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

// --- recipients

// PutRecipient implements notify.Store.
func (s *Store) PutRecipient(ctx context.Context, r notify.Recipient) error {
	if err := r.Check(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("put recipient", err)
	}
	defer func() { _ = tx.Rollback() }()
	var created int64
	switch err := tx.QueryRowContext(ctx, `SELECT created_at FROM notify_recipients WHERE id = ?`, r.ID).Scan(&created); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return wrap("put recipient", err)
	default:
		r.CreatedAt = fromMS(created)
	}
	if err := putRecipient(ctx, tx, r); err != nil {
		return err
	}
	return wrap2("put recipient", tx.Commit())
}

func wrap2(op string, err error) error {
	if err == nil {
		return nil
	}
	return wrap(op, err)
}

type execer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

func putRecipient(ctx context.Context, tx execer, r notify.Recipient) error {
	data, err := json.Marshal(r)
	if err != nil {
		return wrap("put recipient", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO notify_recipients (id, phone, email, telegram, link_code, created_at, data) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET phone = excluded.phone, email = excluded.email, telegram = excluded.telegram,
		link_code = excluded.link_code, data = excluded.data`,
		r.ID, r.Phone, strings.ToLower(r.Email), r.Telegram, r.LinkCode, ms(r.CreatedAt), string(data))
	return wrap2("put recipient", err)
}

func scanRecipients(rows *sql.Rows) ([]notify.Recipient, error) {
	defer rows.Close()
	var out []notify.Recipient
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, wrap("recipients", err)
		}
		var r notify.Recipient
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			return nil, wrap("recipients", err)
		}
		out = append(out, r)
	}
	return out, wrap2("recipients", rows.Err())
}

func (s *Store) one(ctx context.Context, q string, args ...any) (notify.Recipient, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return notify.Recipient{}, wrap("recipient", err)
	}
	rs, err := scanRecipients(rows)
	if err != nil {
		return notify.Recipient{}, err
	}
	if len(rs) == 0 {
		return notify.Recipient{}, notify.ErrNotFound
	}
	return rs[0], nil
}

// GetRecipient implements notify.Store.
func (s *Store) GetRecipient(ctx context.Context, id string) (notify.Recipient, error) {
	return s.one(ctx, `SELECT data FROM notify_recipients WHERE id = ?`, id)
}

// ListRecipients implements notify.Store.
func (s *Store) ListRecipients(ctx context.Context) ([]notify.Recipient, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM notify_recipients ORDER BY id`)
	if err != nil {
		return nil, wrap("list recipients", err)
	}
	return scanRecipients(rows)
}

// DeleteRecipient implements notify.Store.
func (s *Store) DeleteRecipient(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM notify_recipients WHERE id = ?`, id)
	if err != nil {
		return wrap("delete recipient", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notify.ErrNotFound
	}
	return nil
}

// RecipientsFor implements notify.Store.
func (s *Store) RecipientsFor(ctx context.Context, farm string) ([]notify.Recipient, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM notify_recipients
		WHERE coalesce(json_array_length(data, '$.farms'), 0) = 0
		   OR EXISTS (SELECT 1 FROM json_each(data, '$.farms') WHERE value = ?)
		ORDER BY id`, farm)
	if err != nil {
		return nil, wrap("recipients for", err)
	}
	return scanRecipients(rows)
}

// FindRecipient implements notify.Store.
func (s *Store) FindRecipient(ctx context.Context, k notify.AddressKind, addr string) (notify.Recipient, error) {
	if addr == "" {
		return notify.Recipient{}, notify.ErrNotFound
	}
	col := map[notify.AddressKind]string{notify.AddrPhone: "phone", notify.AddrEmail: "email", notify.AddrTelegram: "telegram", "link": "link_code"}[k]
	if col == "" {
		return notify.Recipient{}, fmt.Errorf("%w: find by %q", notify.ErrInvalid, k)
	}
	if k == notify.AddrEmail {
		addr = strings.ToLower(addr)
	}
	return s.one(ctx, `SELECT data FROM notify_recipients WHERE `+col+` = ? ORDER BY id LIMIT 1`, addr)
}

// Update implements notify.Store.
func (s *Store) Update(ctx context.Context, id string, f func(*notify.Recipient) error) (notify.Recipient, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return notify.Recipient{}, wrap("update recipient", err)
	}
	defer func() { _ = tx.Rollback() }()
	var data string
	switch err := tx.QueryRowContext(ctx, `SELECT data FROM notify_recipients WHERE id = ?`, id).Scan(&data); {
	case errors.Is(err, sql.ErrNoRows):
		return notify.Recipient{}, notify.ErrNotFound
	case err != nil:
		return notify.Recipient{}, wrap("update recipient", err)
	}
	var r notify.Recipient
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		return notify.Recipient{}, wrap("update recipient", err)
	}
	created := r.CreatedAt
	if err := f(&r); err != nil {
		return notify.Recipient{}, err
	}
	r.ID, r.CreatedAt = id, created
	if err := r.Check(); err != nil {
		return notify.Recipient{}, err
	}
	if err := putRecipient(ctx, tx, r); err != nil {
		return notify.Recipient{}, err
	}
	return r, wrap2("update recipient", tx.Commit())
}

// --- outbox

// Enqueue implements notify.Store.
func (s *Store) Enqueue(ctx context.Context, m notify.Message, at time.Time) (bool, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return false, wrap("enqueue", err)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO notify_outbox (id, recipient, notice, status, attempts, next_at, last_error, created_at, updated_at, data)
		VALUES (?, ?, ?, ?, 0, ?, '', ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		m.ID, m.To, m.Notice, notify.ItemPending, ms(at), ms(m.CreatedAt), ms(at), string(data))
	if err != nil {
		return false, wrap("enqueue", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

const itemCols = `data, status, attempts, next_at, last_error, updated_at`

func scanItems(rows *sql.Rows) ([]notify.Item, error) {
	defer rows.Close()
	var out []notify.Item
	for rows.Next() {
		var data string
		var it notify.Item
		var next, updated int64
		if err := rows.Scan(&data, &it.Status, &it.Attempts, &next, &it.LastError, &updated); err != nil {
			return nil, wrap("items", err)
		}
		if err := json.Unmarshal([]byte(data), &it.Message); err != nil {
			return nil, wrap("items", err)
		}
		it.NextAt, it.UpdatedAt = fromMS(next), fromMS(updated)
		out = append(out, it)
	}
	return out, wrap2("items", rows.Err())
}

// Claim implements notify.Store.
func (s *Store) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]notify.Item, error) {
	if limit <= 0 {
		limit = 32
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrap("claim", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT `+itemCols+` FROM notify_outbox WHERE status = ? AND next_at <= ? ORDER BY next_at, id LIMIT ?`,
		notify.ItemPending, ms(now), limit)
	if err != nil {
		return nil, wrap("claim", err)
	}
	items, err := scanItems(rows)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].NextAt = now.Add(lease)
		if _, err := tx.ExecContext(ctx, `UPDATE notify_outbox SET next_at = ? WHERE id = ?`, ms(items[i].NextAt), items[i].Message.ID); err != nil {
			return nil, wrap("claim", err)
		}
	}
	return items, wrap2("claim", tx.Commit())
}

// Reschedule implements notify.Store.
func (s *Store) Reschedule(ctx context.Context, id string, at time.Time, attempts int, lastErr string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE notify_outbox SET next_at = ?, attempts = ?, last_error = ?, updated_at = ? WHERE id = ? AND status = ?`,
		ms(at), attempts, lastErr, ms(time.Now()), id, notify.ItemPending)
	if err != nil {
		return wrap("reschedule", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notify.ErrNotFound
	}
	return nil
}

// Finish implements notify.Store.
func (s *Store) Finish(ctx context.Context, id, status string, attempts int, lastErr string, at time.Time) error {
	if status == notify.ItemPending {
		return fmt.Errorf("%w: finish with a final status", notify.ErrInvalid)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE notify_outbox SET status = ?, attempts = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		status, attempts, lastErr, ms(at), id)
	if err != nil {
		return wrap("finish", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notify.ErrNotFound
	}
	return nil
}

// GetItem implements notify.Store.
func (s *Store) GetItem(ctx context.Context, id string) (notify.Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemCols+` FROM notify_outbox WHERE id = ?`, id)
	if err != nil {
		return notify.Item{}, wrap("item", err)
	}
	items, err := scanItems(rows)
	if err != nil {
		return notify.Item{}, err
	}
	if len(items) == 0 {
		return notify.Item{}, notify.ErrNotFound
	}
	return items[0], nil
}

func limitOf(n int) int {
	if n <= 0 {
		return 50
	}
	return min(n, 1000)
}

// ListItems implements notify.Store.
func (s *Store) ListItems(ctx context.Context, f notify.ItemFilter) ([]notify.Item, error) {
	q, args := `SELECT `+itemCols+` FROM notify_outbox WHERE 1 = 1`, []any{}
	if f.Recipient != "" {
		q, args = q+` AND recipient = ?`, append(args, f.Recipient)
	}
	if f.Notice != "" {
		q, args = q+` AND notice = ?`, append(args, f.Notice)
	}
	if f.Status != "" {
		q, args = q+` AND status = ?`, append(args, f.Status)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at DESC, id DESC LIMIT ?`, append(args, limitOf(f.Limit))...)
	if err != nil {
		return nil, wrap("list items", err)
	}
	return scanItems(rows)
}

// --- deliveries

const deliveryCols = `message, lane, recipient, status, provider_id, cost, error, retryable, created_at, updated_at`

func scanDeliveries(rows *sql.Rows) ([]notify.Delivery, error) {
	defer rows.Close()
	var out []notify.Delivery
	for rows.Next() {
		var d notify.Delivery
		var retry int
		var created, updated int64
		if err := rows.Scan(&d.Message, &d.Lane, &d.Recipient, &d.Status, &d.ProviderID, &d.Cost, &d.Error, &retry, &created, &updated); err != nil {
			return nil, wrap("deliveries", err)
		}
		d.Retryable, d.CreatedAt, d.UpdatedAt = retry == 1, fromMS(created), fromMS(updated)
		out = append(out, d)
	}
	return out, wrap2("deliveries", rows.Err())
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// BeginDelivery implements notify.Store.
func (s *Store) BeginDelivery(ctx context.Context, d notify.Delivery) (notify.Delivery, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return notify.Delivery{}, false, wrap("begin delivery", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT `+deliveryCols+` FROM notify_deliveries WHERE message = ? AND lane = ?`, d.Message, d.Lane)
	if err != nil {
		return notify.Delivery{}, false, wrap("begin delivery", err)
	}
	prev, err := scanDeliveries(rows)
	if err != nil {
		return notify.Delivery{}, false, err
	}
	if len(prev) == 1 && !(prev[0].Status == notify.StatusFailed && prev[0].Retryable) {
		return prev[0], false, nil
	}
	d.Status = notify.StatusSending
	_, err = tx.ExecContext(ctx, `INSERT INTO notify_deliveries (`+deliveryCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
		ON CONFLICT (message, lane) DO UPDATE SET status = excluded.status, provider_id = '', error = '', retryable = 0,
		cost = excluded.cost, created_at = excluded.created_at, updated_at = excluded.updated_at`,
		d.Message, d.Lane, d.Recipient, d.Status, "", d.Cost, "", ms(d.CreatedAt), ms(d.UpdatedAt))
	if err != nil {
		return notify.Delivery{}, false, wrap("begin delivery", err)
	}
	return d, true, wrap2("begin delivery", tx.Commit())
}

// FinishDelivery implements notify.Store.
func (s *Store) FinishDelivery(ctx context.Context, d notify.Delivery) error {
	res, err := s.db.ExecContext(ctx, `UPDATE notify_deliveries SET status = ?, provider_id = ?, cost = ?, error = ?, retryable = ?, updated_at = ?
		WHERE message = ? AND lane = ?`,
		d.Status, d.ProviderID, d.Cost, d.Error, b2i(d.Retryable), ms(d.UpdatedAt), d.Message, d.Lane)
	if err != nil {
		return wrap("finish delivery", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notify.ErrNotFound
	}
	return nil
}

// SetStatus implements notify.Store.
func (s *Store) SetStatus(ctx context.Context, lane, providerID, status, errText string, at time.Time) (bool, error) {
	if providerID == "" {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, wrap("set status", err)
	}
	defer func() { _ = tx.Rollback() }()
	var cur string
	switch err := tx.QueryRowContext(ctx, `SELECT status FROM notify_deliveries WHERE lane = ? AND provider_id = ?`, lane, providerID).Scan(&cur); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, wrap("set status", err)
	}
	if notify.Rank(status) <= notify.Rank(cur) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET status = ?, error = ?, updated_at = ? WHERE lane = ? AND provider_id = ?`,
		status, errText, ms(at), lane, providerID); err != nil {
		return false, wrap("set status", err)
	}
	return true, wrap2("set status", tx.Commit())
}

// ListDeliveries implements notify.Store.
func (s *Store) ListDeliveries(ctx context.Context, f notify.DeliveryFilter) ([]notify.Delivery, error) {
	q, args := `SELECT `+deliveryCols+` FROM notify_deliveries WHERE 1 = 1`, []any{}
	if f.Message != "" {
		q, args = q+` AND message = ?`, append(args, f.Message)
	}
	if f.Recipient != "" {
		q, args = q+` AND recipient = ?`, append(args, f.Recipient)
	}
	if f.Lane != "" {
		q, args = q+` AND lane = ?`, append(args, f.Lane)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at DESC, lane LIMIT ?`, append(args, limitOf(f.Limit))...)
	if err != nil {
		return nil, wrap("list deliveries", err)
	}
	return scanDeliveries(rows)
}

// Spend implements notify.Store.
func (s *Store) Spend(ctx context.Context, lane string, since time.Time) (int, int64, error) {
	var n int
	var cost int64
	err := s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(cost), 0) FROM notify_deliveries
		WHERE lane = ? AND created_at >= ? AND status IN (?, ?, ?, ?)`,
		lane, ms(since), notify.StatusSending, notify.StatusSent, notify.StatusDelivered, notify.StatusRead).Scan(&n, &cost)
	if err != nil {
		return 0, 0, wrap("spend", err)
	}
	return n, cost, nil
}

// Prune implements notify.Store.
func (s *Store) Prune(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM notify_outbox WHERE status != ? AND updated_at < ?`, notify.ItemPending, ms(before)); err != nil {
		return wrap("prune", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM notify_deliveries WHERE updated_at < ?`, ms(before)); err != nil {
		return wrap("prune", err)
	}
	return nil
}
