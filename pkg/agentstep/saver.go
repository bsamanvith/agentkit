package agentstep

import (
	"context"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

// TranscriptSaver keeps the transcript of each agent step of each flow run,
// keyed by the run id and the step's path, so that a step run again continues
// where it stopped.
//
// Implementations must be safe for concurrent use (the agents of a
// [flow.Parallel] save at the same time, under different paths), must honor
// ctx, and must not keep or modify the values passed in or returned. Run the
// conformance suite in package agentsteptest against every implementation.
type TranscriptSaver interface {
	// Load returns the transcript saved for the step at path in run runID,
	// empty if nothing was saved.
	Load(ctx context.Context, runID, path string) (harness.Transcript, error)
	// Append adds msgs to the end of that transcript. It is atomic: after an
	// error or a crash, either all of msgs were saved or none.
	Append(ctx context.Context, runID, path string, msgs []llm.Message) error
}

// MemorySaver is an in-process [TranscriptSaver]. It does not survive the
// process; it is for tests, short-lived runs and as the reference
// implementation.
type MemorySaver struct {
	mu sync.Mutex
	m  map[[2]string]harness.Transcript
}

// NewMemorySaver returns an empty MemorySaver.
func NewMemorySaver() *MemorySaver {
	return &MemorySaver{m: map[[2]string]harness.Transcript{}}
}

// Load implements [TranscriptSaver].
func (s *MemorySaver) Load(ctx context.Context, runID, path string) (harness.Transcript, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[[2]string{runID, path}].Clone(), nil
}

// Append implements [TranscriptSaver].
func (s *MemorySaver) Append(ctx context.Context, runID, path string, msgs []llm.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{runID, path}
	s.m[k] = append(s.m[k], harness.Transcript(msgs).Clone()...)
	return nil
}
