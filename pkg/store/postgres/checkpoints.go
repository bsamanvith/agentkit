package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
)

// Save implements [flow.Checkpointer]: the checkpoint is one JSON document,
// with its shape in a column of its own, replacing any earlier one.
func (s *Store) Save(ctx context.Context, id string, c flow.Checkpoint) error {
	tenant, err := scope(ctx)
	if err != nil {
		return err
	}
	shape := c.Shape
	c.Shape = "" // in its column; the field is omitempty
	body, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("postgres: save checkpoint of run %q: %w", id, err)
	}
	return retry(ctx, func() error {
		_, err := s.pool.Exec(ctx, `INSERT INTO checkpoints (tenant, run_id, shape, body) VALUES ($1, $2, $3, $4)
			ON CONFLICT (tenant, run_id) DO UPDATE SET shape = excluded.shape, body = excluded.body`,
			tenant, id, shape, string(body))
		if err != nil {
			return fail(fmt.Sprintf("save checkpoint of run %q", id), err)
		}
		return nil
	})
}

// Load implements [flow.Checkpointer].
func (s *Store) Load(ctx context.Context, id string) (flow.Checkpoint, error) {
	tenant, err := scope(ctx)
	if err != nil {
		return flow.Checkpoint{}, err
	}
	if !backend.TextOK(id) {
		return flow.Checkpoint{}, fmt.Errorf("%w: checkpoint of run %q", flow.ErrNotFound, id)
	}
	var shape, body string
	err = s.pool.QueryRow(ctx, `SELECT shape, body FROM checkpoints WHERE tenant = $1 AND run_id = $2`,
		tenant, id).Scan(&shape, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return flow.Checkpoint{}, fmt.Errorf("%w: checkpoint of run %q", flow.ErrNotFound, id)
	}
	if err != nil {
		return flow.Checkpoint{}, fail(fmt.Sprintf("load checkpoint of run %q", id), err)
	}
	var c flow.Checkpoint
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		return flow.Checkpoint{}, fmt.Errorf("postgres: load checkpoint of run %q: %w", id, err)
	}
	c.Shape = shape
	return c, nil
}
