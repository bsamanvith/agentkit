package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/flow"
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
		return fmt.Errorf("sqlite: save checkpoint of run %q: %w", id, err)
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO checkpoints (tenant, run_id, shape, body) VALUES (?, ?, ?, ?)
			ON CONFLICT (tenant, run_id) DO UPDATE SET shape = excluded.shape, body = excluded.body`,
			tenant, id, shape, string(body))
		if err != nil {
			return fmt.Errorf("sqlite: save checkpoint of run %q: %w", id, err)
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
	var shape string
	var body []byte
	err = s.r.QueryRowContext(ctx, `SELECT shape, body FROM checkpoints WHERE tenant = ? AND run_id = ?`,
		tenant, id).Scan(&shape, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return flow.Checkpoint{}, fmt.Errorf("%w: checkpoint of run %q", flow.ErrNotFound, id)
	}
	if err != nil {
		return flow.Checkpoint{}, fmt.Errorf("sqlite: load checkpoint of run %q: %w", id, err)
	}
	var c flow.Checkpoint
	if err := json.Unmarshal(body, &c); err != nil {
		return flow.Checkpoint{}, fmt.Errorf("sqlite: load checkpoint of run %q: %w", id, err)
	}
	c.Shape = shape
	return c, nil
}
