package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// CheckpointVersion is the version of the [Checkpoint] format. [Resume] refuses
// any other.
const CheckpointVersion = 1

// Status says where a run stands.
type Status string

// The statuses of a run.
const (
	StatusRunning   Status = "running"
	StatusSuspended Status = "suspended"
	StatusFailed    Status = "failed"
	StatusRejected  Status = "rejected"
	StatusCompleted Status = "completed"
)

// Checkpoint is everything needed to resume a run. It is plain JSON, so a
// store can keep it as one document.
type Checkpoint struct {
	Version int    `json:"version"`
	RunID   string `json:"run_id"`
	Status  Status `json:"status"`
	// Initial is the state the run started with.
	Initial json.RawMessage `json:"initial"`
	// Steps maps the path of each completed step to the state as it was when
	// that step completed (for a step inside a Parallel branch, the branch's
	// state). Records of a step's children are dropped when the step itself
	// completes, so this holds only the frontier of the run.
	Steps map[string]json.RawMessage `json:"steps"`
	// Rejected maps the path of each rejected approval to its reason.
	Rejected map[string]string `json:"rejected,omitempty"`
	// Pending lists the approvals the run is waiting for, sorted.
	Pending []string `json:"pending,omitempty"`
}

// Clone returns a deep copy.
func (c Checkpoint) Clone() Checkpoint {
	out := c
	out.Initial = slices.Clone(c.Initial)
	out.Steps = make(map[string]json.RawMessage, len(c.Steps))
	for k, v := range c.Steps {
		out.Steps[k] = slices.Clone(v)
	}
	out.Rejected = maps.Clone(c.Rejected)
	out.Pending = slices.Clone(c.Pending)
	return out
}

// Checkpointer stores one [Checkpoint] per run. Implementations must be safe
// for concurrent use, must honor ctx, and must not keep or modify the values
// passed in or returned (copy them). The runner serializes the saves of one
// run, so a later Save of a run always carries newer progress than an earlier
// one. Run the conformance suite in package flowtest against every
// implementation.
type Checkpointer interface {
	// Save stores c as the checkpoint of run id, replacing any earlier one.
	Save(ctx context.Context, id string, c Checkpoint) error
	// Load returns the checkpoint of run id, or an error matching ErrNotFound.
	Load(ctx context.Context, id string) (Checkpoint, error)
}

// Memory is an in-process [Checkpointer]. It does not survive the process; it
// is for tests, short-lived runs and as the reference implementation.
type Memory struct {
	mu   sync.Mutex
	runs map[string]Checkpoint
}

// NewMemory returns an empty in-memory checkpointer.
func NewMemory() *Memory { return &Memory{runs: map[string]Checkpoint{}} }

// Save implements [Checkpointer].
func (m *Memory) Save(ctx context.Context, id string, c Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[id] = c.Clone()
	return nil
}

// Load implements [Checkpointer].
func (m *Memory) Load(ctx context.Context, id string) (Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return Checkpoint{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.runs[id]
	if !ok {
		return Checkpoint{}, fmt.Errorf("%w: run %q", ErrNotFound, id)
	}
	return c.Clone(), nil
}
