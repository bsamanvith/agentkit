package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

const commandColumns = `seq, idempotency_key, type, data, created_at`

// lockRun locks the run's row until the transaction ends, so that sends and
// consumes of its commands, and its transitions and leases, happen one at a
// time. It returns [store.ErrNotFound] for a run that does not exist.
func lockRun(ctx context.Context, tx pgx.Tx, tenant, runID string) (store.Run, error) {
	if backend.TextOK(runID) {
		var one int
		err := tx.QueryRow(ctx, `SELECT 1 FROM runs WHERE tenant = $1 AND id = $2 FOR UPDATE`, tenant, runID).Scan(&one)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return store.Run{}, err
		}
	}
	return getRun(ctx, tx, tenant, runID)
}

// SendCommand implements [store.CommandLog].
//
// One transaction locks the run's row, so senders of one run go one at a
// time: look the key up, check the run, then insert at the next seq.
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
	err = retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			r, err := lockRun(ctx, tx, tenant, runID)
			if err != nil {
				return err
			}
			stored, err := queryCommands(ctx, tx, `WHERE tenant = $1 AND run_id = $2 AND idempotency_key = $3`, tenant, runID, c.Key)
			if err != nil {
				return fail("send command", err)
			}
			if len(stored) == 1 {
				out, err = backend.SentCommand(runID, c, stored[0])
				return err
			}
			if r.Status.Final() {
				return fmt.Errorf("%w: run %q is %s", store.ErrFinal, runID, r.Status)
			}
			var last int64
			var lastCreated *int64
			err = tx.QueryRow(ctx, `SELECT seq, created_at FROM commands
				WHERE tenant = $1 AND run_id = $2 ORDER BY seq DESC LIMIT 1`, tenant, runID).Scan(&last, &lastCreated)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fail("send command", err)
			}
			created := s.now()
			if prev := loadTime(lastCreated); created.Before(prev) {
				created = prev // times never go backwards along the log
			}
			at, err := storeTime(created)
			if err != nil {
				return fmt.Errorf("postgres: send command: the clock reads %w", err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO commands (tenant, run_id, seq, idempotency_key, type, data, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, tenant, runID, last+1, c.Key, c.Type, string(c.Data), at); err != nil {
				return fail("send command", err)
			}
			c.Seq, c.CreatedAt = last+1, created.UTC()
			out = c
			return nil
		})
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
	out, err := queryCommands(ctx, s.pool, `WHERE tenant = $1 AND run_id = $2 AND consumed_at IS NULL ORDER BY seq LIMIT $3`,
		tenant, runID, n)
	if err != nil {
		return nil, fail("pending commands", err)
	}
	return out, nil
}

// ConsumeCommands implements [store.CommandLog].
//
// One transaction locks the run's row, checks the lease against the store's
// clock, and marks the commands.
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
	return retry(ctx, func() error {
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			r, err := lockRun(ctx, tx, tenant, runID)
			if err != nil {
				return err
			}
			var last int64
			if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM commands WHERE tenant = $1 AND run_id = $2`,
				tenant, runID).Scan(&last); err != nil {
				return fail("consume commands", err)
			}
			now := s.now()
			if err := backend.ConsumeError(r, owner, through, last, now); err != nil {
				return err
			}
			at, err := storeTime(now)
			if err != nil {
				return fmt.Errorf("postgres: consume commands: the clock reads %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE commands SET consumed_at = $4
				WHERE tenant = $1 AND run_id = $2 AND seq <= $3 AND consumed_at IS NULL`, tenant, runID, through, at); err != nil {
				return fail("consume commands", err)
			}
			return nil
		})
	})
}

// queryCommands reads the commands a WHERE clause (and what follows it)
// selects.
func queryCommands(ctx context.Context, q querier, where string, args ...any) ([]store.Command, error) {
	rows, err := q.Query(ctx, `SELECT `+commandColumns+` FROM commands `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Command
	for rows.Next() {
		var c store.Command
		var data string
		var created *int64
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
