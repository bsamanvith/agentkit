package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

const commandColumns = `seq, idempotency_key, type, data, created_at`

// SendCommand implements [store.CommandLog].
//
// One transaction, which holds the write lock from its start: look the key
// up, check the run, then INSERT at the next seq.
func (s *Store) SendCommand(ctx context.Context, runID string, c store.Command) (store.Command, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return store.Command{}, err
	}
	if err := backend.CheckID("run", runID); err != nil {
		return store.Command{}, err
	}
	c, err = backend.CheckCommand(c)
	if err != nil {
		return store.Command{}, err
	}
	var out store.Command
	err = s.write(ctx, func(tx *txn) error {
		stored, err := queryCommands(ctx, tx, `WHERE tenant = ? AND run_id = ? AND idempotency_key = ?`, tenant, runID, c.Key)
		if err != nil {
			return fmt.Errorf("sqlite: send command: %w", err)
		}
		if len(stored) == 1 {
			out, err = backend.SentCommand(runID, c, stored[0])
			return err
		}
		r, err := getRun(ctx, tx, tenant, runID)
		if err != nil {
			return err
		}
		if r.Status.Final() {
			return fmt.Errorf("%w: run %q is %s", store.ErrFinal, runID, r.Status)
		}
		var last int64
		var lastCreated sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT seq, created_at FROM commands
			WHERE tenant = ? AND run_id = ? ORDER BY seq DESC LIMIT 1`, tenant, runID).Scan(&last, &lastCreated)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("sqlite: send command: %w", err)
		}
		created := s.now()
		if prev := loadTime(lastCreated); created.Before(prev) {
			created = prev // times never go backwards along the log
		}
		at, err := storeTime(created)
		if err != nil {
			return fmt.Errorf("sqlite: send command: the clock reads %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO commands (tenant, run_id, seq, idempotency_key, type, data, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, tenant, runID, last+1, c.Key, c.Type, string(c.Data), at); err != nil {
			return fmt.Errorf("sqlite: send command: %w", err)
		}
		c.Seq, c.CreatedAt = last+1, created.UTC()
		out = c
		return nil
	})
	if err != nil {
		return store.Command{}, err
	}
	return out, nil
}

// PendingCommands implements [store.CommandLog].
func (s *Store) PendingCommands(ctx context.Context, runID string, limit int) ([]store.Command, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	if err := backend.CheckID("run", runID); err != nil {
		return nil, err
	}
	n, err := backend.Limit(store.Page{Limit: limit})
	if err != nil {
		return nil, err
	}
	out, err := queryCommands(ctx, s.r, `WHERE tenant = ? AND run_id = ? AND consumed_at IS NULL ORDER BY seq LIMIT CAST(? AS INTEGER)`,
		tenant, runID, n)
	if err != nil {
		return nil, fmt.Errorf("sqlite: pending commands: %w", err)
	}
	return out, nil
}

// ConsumeCommands implements [store.CommandLog].
func (s *Store) ConsumeCommands(ctx context.Context, runID, owner string, through int64) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	if err := backend.CheckID("run", runID); err != nil {
		return err
	}
	if err := backend.CheckConsume(owner, through); err != nil {
		return err
	}
	return s.write(ctx, func(tx *txn) error {
		r, err := getRun(ctx, tx, tenant, runID)
		if err != nil {
			return err
		}
		var last int64
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(seq), 0) FROM commands WHERE tenant = ? AND run_id = ?`,
			tenant, runID).Scan(&last); err != nil {
			return fmt.Errorf("sqlite: consume commands: %w", err)
		}
		now := s.now()
		if err := backend.ConsumeError(r, owner, through, last, now); err != nil {
			return err
		}
		at, err := storeTime(now)
		if err != nil {
			return fmt.Errorf("sqlite: consume commands: the clock reads %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE commands SET consumed_at = ?
			WHERE tenant = ? AND run_id = ? AND seq <= ? AND consumed_at IS NULL`, at, tenant, runID, through); err != nil {
			return fmt.Errorf("sqlite: consume commands: %w", err)
		}
		return nil
	})
}

// queryCommands reads the commands a WHERE clause (and what follows it)
// selects.
func queryCommands(ctx context.Context, q querier, where string, args ...any) ([]store.Command, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+commandColumns+` FROM commands `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Command
	for rows.Next() {
		var c store.Command
		var data string
		var created sql.NullInt64
		if err := rows.Scan(&c.Seq, &c.Key, &c.Type, &data, &created); err != nil {
			return nil, err
		}
		if data != "" {
			c.Data = []byte(data)
		}
		c.CreatedAt = loadTime(created)
		out = append(out, c)
	}
	return out, rows.Err()
}
