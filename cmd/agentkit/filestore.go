package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

// fileStore keeps flow checkpoints and agent transcripts in one JSON file, so
// that a flow stopped at an approval can be resumed by another process. It is
// for the demo only: the file is rewritten on every save, and one process at a
// time may use it. The real store is E05's.
type fileStore struct {
	path string
	mu   sync.Mutex
	data fileData
}

type fileData struct {
	Checkpoints map[string]flow.Checkpoint `json:"checkpoints"`
	// Transcripts maps run id, then step path, to the saved transcript.
	Transcripts map[string]map[string]harness.Transcript `json:"transcripts"`
}

// openFileStore reads the store at path, or starts an empty one if the file
// does not exist yet.
func openFileStore(path string) (*fileStore, error) {
	s := &fileStore{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("state file %s: %w", path, err)
		}
	}
	if s.data.Checkpoints == nil {
		s.data.Checkpoints = map[string]flow.Checkpoint{}
	}
	if s.data.Transcripts == nil {
		s.data.Transcripts = map[string]map[string]harness.Transcript{}
	}
	return s, nil
}

// Save implements flow.Checkpointer.
func (s *fileStore) Save(ctx context.Context, id string, c flow.Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, had := s.data.Checkpoints[id]
	s.data.Checkpoints[id] = c.Clone()
	if err := s.flush(); err != nil {
		if had {
			s.data.Checkpoints[id] = old
		} else {
			delete(s.data.Checkpoints, id)
		}
		return err
	}
	return nil
}

// Load implements flow.Checkpointer.
func (s *fileStore) Load(ctx context.Context, id string) (flow.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return flow.Checkpoint{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data.Checkpoints[id]
	if !ok {
		return flow.Checkpoint{}, fmt.Errorf("%w: run %q", flow.ErrNotFound, id)
	}
	return c.Clone(), nil
}

// transcripts returns the store's agentstep.TranscriptSaver (its methods'
// names collide with flow.Checkpointer's).
func (s *fileStore) transcripts() fileTranscripts { return fileTranscripts{s} }

type fileTranscripts struct{ s *fileStore }

// Load implements agentstep.TranscriptSaver.
func (t fileTranscripts) Load(ctx context.Context, runID, path string) (harness.Transcript, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	return t.s.data.Transcripts[runID][path].Clone(), nil
}

// Append implements agentstep.TranscriptSaver.
func (t fileTranscripts) Append(ctx context.Context, runID, path string, msgs []llm.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	run := t.s.data.Transcripts[runID]
	if run == nil {
		run = map[string]harness.Transcript{}
		t.s.data.Transcripts[runID] = run
	}
	old, had := run[path]
	run[path] = append(old.Clone(), harness.Transcript(msgs).Clone()...)
	if err := t.s.flush(); err != nil {
		if had {
			run[path] = old
		} else {
			delete(run, path)
		}
		return err
	}
	return nil
}

// flush writes the whole store to a temporary file and renames it over the
// old one, so a crash leaves either the old file or the new one.
func (s *fileStore) flush() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // after a successful rename there is nothing to remove
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
